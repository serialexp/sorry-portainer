package secrets

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"

	"golang.org/x/sys/unix"
)

// DigestFile sits next to the secret files in a container's /run/secrets
// tmpfs. It holds a fingerprint of the values the hook wrote, so the agent can
// tell, after it restarts with an empty vault, whether a running container
// already has the values the master pushes again. Secret targets must start
// with a letter or digit, so the leading dot cannot clash with one.
const DigestFile = ".sorry-portainer-digest"

// maxDigestSize bounds what ReadDigest reads from a container.
const maxDigestSize = 128

const digestPrefix = "sha256:"

// Digest fingerprints the files mounts describe, filled from values. Every
// field is length-prefixed, so different mount lists cannot produce the same
// input. A container's own root can read the file, but it can read the values
// themselves too, so the fingerprint discloses nothing new.
func Digest(mounts []Mount, values map[string][]byte) (string, error) {
	h := sha256.New()
	writeField(h, []byte("sorry-portainer secret digest v1"))
	for _, m := range mounts {
		value, ok := values[m.Source]
		if !ok {
			return "", fmt.Errorf("secret %s has no value", m.Source)
		}
		writeField(h, []byte(m.Source))
		writeField(h, []byte(m.Target))
		var owner [12]byte
		binary.BigEndian.PutUint32(owner[0:4], uint32(m.UID))
		binary.BigEndian.PutUint32(owner[4:8], uint32(m.GID))
		binary.BigEndian.PutUint32(owner[8:12], m.Mode)
		h.Write(owner[:])
		writeField(h, value)
	}
	return digestPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

func writeField(h hash.Hash, field []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(field)))
	h.Write(size[:])
	h.Write(field)
}

// writeDigest stores digest in the secrets directory, owned by the
// container's root and readable only by it. In Podman's default rootless mode
// that is the agent user, so the agent can read it back.
func writeDigest(dir int, digest string, ids idMapper) error {
	uid, err := ids.uid(0)
	if err != nil {
		return err
	}
	gid, err := ids.gid(0)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(dir, DigestFile, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), DigestFile)
	defer f.Close()
	if _, err := f.WriteString(digest); err != nil {
		return err
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return fmt.Errorf("chown digest: %w", err)
	}
	return unix.Fchmod(fd, 0o400)
}

// ReadDigest returns the digest the hook wrote into the running container
// with process pid. It returns "" and no error when the container has no
// digest file, for example because it started before digests existed; the
// caller then treats the container as out of date.
func ReadDigest(procRoot string, pid int) (string, error) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	dir, err := openSecretsDir(procRoot, pid, "", unix.O_PATH)
	if err != nil {
		return "", err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat2(dir, DigestFile, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_BENEATH,
	})
	if errors.Is(err, unix.ENOENT) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("open %s: %w", DigestFile, err)
	}
	f := os.NewFile(uintptr(fd), DigestFile)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDigestSize {
		return "", fmt.Errorf("%s is not a small regular file", DigestFile)
	}
	buf := make([]byte, maxDigestSize+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", fmt.Errorf("read %s: %w", DigestFile, err)
	}
	if n > maxDigestSize {
		return "", fmt.Errorf("%s is too large", DigestFile)
	}
	return string(buf[:n]), nil
}
