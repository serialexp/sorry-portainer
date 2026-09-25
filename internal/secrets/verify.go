package secrets

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Verify checks that the running container with process pid has every
// mount's target in its /run/secrets tmpfs. The agent calls it after starting
// a container: a container that started without the hook (for example
// because containers.conf lacks the hooks directory) has an empty tmpfs. The
// agent owns the rootless user namespace, so it may look into
// /proc/<pid>/root without being inside it.
func Verify(procRoot string, pid int, mounts []Mount) error {
	if procRoot == "" {
		procRoot = "/proc"
	}
	dir, err := openSecretsDir(procRoot, pid, "", unix.O_PATH)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var missing []string
	for _, m := range mounts {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, m.Target, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			missing = append(missing, m.Target)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("secrets %v were not injected (is the sorry-portainer OCI hook configured in containers.conf hooks_dir?)", missing)
	}
	return nil
}

// SwapActive reports whether the host has any active swap area. tmpfs pages
// and agent memory can be written to swap, so the UI warns about such hosts.
func SwapActive() (bool, error) {
	f, err := os.Open("/proc/swaps")
	if err != nil {
		return false, err
	}
	defer f.Close()
	return swapActive(f)
}

func swapActive(r io.Reader) (bool, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	lines := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			lines++
		}
	}
	// The first line is the header.
	return lines > 1, scanner.Err()
}
