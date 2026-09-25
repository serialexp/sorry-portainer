package secrets

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const (
	maxStateSize  = 64 << 10
	maxConfigSize = 8 << 20
)

// ociState is the part of the OCI runtime state a hook receives on stdin.
type ociState struct {
	ID          string            `json:"id"`
	Pid         int               `json:"pid"`
	Bundle      string            `json:"bundle"`
	Annotations map[string]string `json:"annotations"`
}

type ociConfig struct {
	Root struct {
		Path string `json:"path"`
	} `json:"root"`
	Annotations map[string]string `json:"annotations"`
}

// HookOptions configures RunHook.
type HookOptions struct {
	HostID string
	Socket string
	// ProcRoot is "/proc" except in tests.
	ProcRoot string
}

// RunHook is the OCI createRuntime hook. It reads the container state from
// stdin, asks the agent for the container's secrets, and writes them into
// the container's /run/secrets tmpfs. Any error must make the hook exit
// non-zero, so the runtime refuses to start the container without them.
func RunHook(ctx context.Context, stdin io.Reader, options HookOptions) error {
	if options.ProcRoot == "" {
		options.ProcRoot = "/proc"
	}
	stateBytes, err := io.ReadAll(io.LimitReader(stdin, maxStateSize+1))
	if err != nil {
		return fmt.Errorf("read OCI state: %w", err)
	}
	if len(stateBytes) > maxStateSize {
		return errors.New("OCI state too large")
	}
	var state ociState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		return fmt.Errorf("decode OCI state: %w", err)
	}
	if state.Pid <= 0 || state.Bundle == "" || state.ID == "" {
		return errors.New("OCI state lacks pid, bundle, or id")
	}
	config, err := readConfig(state.Bundle)
	if err != nil {
		return err
	}
	// The config's annotations are authoritative; runc passes them in the state too.
	annotations := config.Annotations
	if len(annotations) == 0 {
		annotations = state.Annotations
	}
	if annotations[AnnotationAgent] != options.HostID {
		return fmt.Errorf("container is for agent %q, this hook serves %q", annotations[AnnotationAgent], options.HostID)
	}
	stack := annotations[AnnotationStack]
	if !protocol.ValidStackName(stack) {
		return fmt.Errorf("container has invalid stack annotation %q", stack)
	}
	mounts, err := DecodeMounts(annotations[AnnotationSecrets])
	if err != nil {
		return err
	}
	rootfs := config.Root.Path
	if rootfs == "" {
		return errors.New("bundle config has no root.path")
	}
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(state.Bundle, rootfs)
	}

	values, err := request(ctx, options.Socket, hookRequest{HostID: options.HostID, ContainerID: state.ID, Stack: stack, Names: Sources(mounts)})
	if err != nil {
		return err
	}
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	return inject(options.ProcRoot, state.Pid, rootfs, mounts, values)
}

func readConfig(bundle string) (ociConfig, error) {
	f, err := os.Open(filepath.Join(bundle, "config.json"))
	if err != nil {
		return ociConfig{}, fmt.Errorf("open bundle config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return ociConfig{}, err
	}
	if len(data) > maxConfigSize {
		return ociConfig{}, errors.New("bundle config too large")
	}
	var config ociConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return ociConfig{}, fmt.Errorf("decode bundle config: %w", err)
	}
	return config, nil
}

// inject writes values into the container's secrets tmpfs.
//
// At createRuntime the container has its mounts but has not pivoted yet, so
// its tmpfs is at <rootfs>/run/secrets inside /proc/<pid>/root. Runtimes that
// pivot earlier expose it at /run/secrets. The rootfs path is tried first and
// the pivoted path only when the rootfs is absent from the container's view,
// so the host's own /run/secrets is never used.
func inject(procRoot string, pid int, rootfs string, mounts []Mount, values map[string][]byte) error {
	dir, err := openSecretsDir(procRoot, pid, rootfs, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	ids, err := newIDMapper(procRoot, pid)
	if err != nil {
		return err
	}
	for _, m := range mounts {
		if err := writeSecret(dir, m, values[m.Source], ids); err != nil {
			return fmt.Errorf("write secret %s: %w", m.Target, err)
		}
	}
	digest, err := Digest(mounts, values)
	if err != nil {
		return err
	}
	if err := writeDigest(dir, digest, ids); err != nil {
		return fmt.Errorf("write %s: %w", DigestFile, err)
	}
	return nil
}

const tmpfsMagic = 0x01021994

// openSecretsDir opens the /run/secrets directory of the container whose
// process is pid, with openFlags (O_RDONLY or O_PATH). It refuses anything
// but a tmpfs mounted exactly there, and never follows a symlink inside the
// container. rootfs may be empty for an already pivoted container.
func openSecretsDir(procRoot string, pid int, rootfs string, openFlags int) (int, error) {
	root, err := unix.Open(filepath.Join(procRoot, strconv.Itoa(pid), "root"), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open container root: %w", err)
	}
	defer unix.Close(root)

	base := root
	if rootfs != "" {
		// The rootfs is a host path Podman chose; it may pass through host
		// symlinks, which resolve inside this mount namespace's root.
		fd, err := unix.Openat2(root, strings.TrimPrefix(rootfs, "/"), &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
		})
		switch {
		case err == nil:
			defer unix.Close(fd)
			base = fd
		case errors.Is(err, unix.ENOENT):
			// Already pivoted: the container root is the rootfs.
		default:
			return -1, fmt.Errorf("open container rootfs: %w", err)
		}
	}
	if base == root {
		// Using the process root directly is only right after pivot_root. If it
		// is still our own root, "run/secrets" would be the host's directory.
		if err := checkNotOwnRoot(procRoot, root); err != nil {
			return -1, err
		}
	}

	// "run" and "run/secrets" are image-controlled paths: no symlinks, and
	// nothing outside the rootfs. RESOLVE_NO_XDEV is not used for "run",
	// which may itself be a mount.
	run, err := unix.Openat2(base, "run", &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_BENEATH,
	})
	if err != nil {
		return -1, fmt.Errorf("open container /run: %w", err)
	}
	defer unix.Close(run)
	dir, err := unix.Openat2(run, "secrets", &unix.OpenHow{
		Flags:   uint64(openFlags) | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_BENEATH,
	})
	if err != nil {
		return -1, fmt.Errorf("open container %s (is its tmpfs mounted?): %w", Dir, err)
	}
	if err := checkTmpfsMount(run, dir); err != nil {
		unix.Close(dir)
		return -1, err
	}
	return dir, nil
}

func checkNotOwnRoot(procRoot string, root int) error {
	var containerRoot, ownRoot unix.Stat_t
	if err := unix.Fstat(root, &containerRoot); err != nil {
		return err
	}
	if err := unix.Stat(filepath.Join(procRoot, "self", "root")+"/", &ownRoot); err != nil {
		return err
	}
	if containerRoot.Dev == ownRoot.Dev && containerRoot.Ino == ownRoot.Ino {
		return errors.New("container rootfs not found in its mount namespace; refusing to use the host's /run/secrets")
	}
	return nil
}

// checkTmpfsMount refuses dir unless it is a tmpfs mounted on top of its
// parent. Without a tmpfs a secret would land in the container's writable
// layer, which is on disk.
func checkTmpfsMount(parent, dir int) error {
	var fsStat unix.Statfs_t
	if err := unix.Fstatfs(dir, &fsStat); err != nil {
		return err
	}
	if fsStat.Type != tmpfsMagic {
		return fmt.Errorf("container %s is not a tmpfs; refusing to write secrets to disk", Dir)
	}
	var dirStat, parentStat unix.Stat_t
	if err := unix.Fstat(dir, &dirStat); err != nil {
		return err
	}
	if err := unix.Fstat(parent, &parentStat); err != nil {
		return err
	}
	if dirStat.Dev == parentStat.Dev {
		return fmt.Errorf("container %s is not its own mount; refusing to write secrets", Dir)
	}
	return nil
}

func writeSecret(dir int, m Mount, value []byte, ids idMapper) error {
	uid, err := ids.uid(m.UID)
	if err != nil {
		return err
	}
	gid, err := ids.gid(m.GID)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(dir, m.Target, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), m.Target)
	defer f.Close()
	if _, err := f.Write(value); err != nil {
		return err
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return fmt.Errorf("chown to %d:%d: %w", m.UID, m.GID, err)
	}
	return unix.Fchmod(fd, m.Mode)
}

// idMapper turns container uids/gids into the hook's own user namespace. For
// Podman's default rootless mode the container shares the hook's namespace
// and IDs pass through; a container in a nested namespace (for example
// --userns=auto) is translated through its uid_map/gid_map.
type idMapper struct {
	same           bool
	uidMap, gidMap []idRange
}

type idRange struct{ inside, outside, length int64 }

func newIDMapper(procRoot string, pid int) (idMapper, error) {
	self, err := os.Stat(filepath.Join(procRoot, "self", "ns", "user"))
	if err != nil {
		return idMapper{}, err
	}
	target, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid), "ns", "user"))
	if err != nil {
		return idMapper{}, err
	}
	if os.SameFile(self, target) {
		return idMapper{same: true}, nil
	}
	// Read from a parent namespace, the second column is in the reader's IDs.
	uidMap, err := readIDMap(filepath.Join(procRoot, strconv.Itoa(pid), "uid_map"))
	if err != nil {
		return idMapper{}, err
	}
	gidMap, err := readIDMap(filepath.Join(procRoot, strconv.Itoa(pid), "gid_map"))
	if err != nil {
		return idMapper{}, err
	}
	return idMapper{uidMap: uidMap, gidMap: gidMap}, nil
}

func readIDMap(path string) ([]idRange, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []idRange
	scanner := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed %s", path)
		}
		var r idRange
		var errs [3]error
		r.inside, errs[0] = strconv.ParseInt(fields[0], 10, 64)
		r.outside, errs[1] = strconv.ParseInt(fields[1], 10, 64)
		r.length, errs[2] = strconv.ParseInt(fields[2], 10, 64)
		if err := errors.Join(errs[:]...); err != nil {
			return nil, fmt.Errorf("malformed %s: %w", path, err)
		}
		out = append(out, r)
	}
	return out, scanner.Err()
}

func mapID(ranges []idRange, id int) (int, error) {
	for _, r := range ranges {
		if int64(id) >= r.inside && int64(id) < r.inside+r.length {
			return int(r.outside + int64(id) - r.inside), nil
		}
	}
	return 0, fmt.Errorf("id %d is not mapped in the container", id)
}

func (m idMapper) uid(id int) (int, error) {
	if m.same {
		return id, nil
	}
	return mapID(m.uidMap, id)
}

func (m idMapper) gid(id int) (int, error) {
	if m.same {
		return id, nil
	}
	return mapID(m.gidMap, id)
}
