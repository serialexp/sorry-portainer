package registryvault

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
)

// TestTimeout bounds one sign-in test.
const TestTimeout = 30 * time.Second

// LoginRunner runs `podman login` for a test. The production runner is
// PodmanLogin; tests substitute one.
type LoginRunner func(ctx context.Context, authFile, key, username string, password []byte) (output string, err error)

// PodmanLogin signs in exactly as Podman does, which also validates a
// Basic-auth registry's credentials (go-containerregistry's transport only
// discovers the challenge). The password goes on stdin, never in arguments.
func PodmanLogin(ctx context.Context, authFile, key, username string, password []byte) (string, error) {
	command := exec.CommandContext(ctx, "podman", "login", "--authfile", authFile, "--username", username, "--password-stdin", "--", key)
	command.Stdin = bytes.NewReader(password)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.String(), err
}

// Test signs in to login's registry with it, using a throwaway auth file in
// the vault's tmpfs directory that is removed afterwards. Nothing is kept.
// The answer's message is built here from the failure kind; Podman's output
// is only classified, never passed on.
func (v *Vault) Test(ctx context.Context, login protocol.RegistryLogin, run LoginRunner) (protocol.RegistryTestResult, error) {
	if err := registries.Check([]protocol.RegistryLogin{login}); err != nil {
		return protocol.RegistryTestResult{}, err
	}
	if run == nil {
		run = PodmanLogin
	}
	login.Username = registries.Username(login.Key, login.Username)
	dirFD, err := v.openDir()
	if err != nil {
		return protocol.RegistryTestResult{}, err
	}
	defer unix.Close(dirFD)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return protocol.RegistryTestResult{}, err
	}
	name := ".test-" + hex.EncodeToString(suffix) + ".json"
	fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return protocol.RegistryTestResult{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	_, err = file.WriteString(`{"auths":{}}`)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	defer unix.Unlinkat(dirFD, name, 0)
	if err != nil {
		return protocol.RegistryTestResult{}, err
	}
	testCtx, cancel := context.WithTimeout(ctx, TestTimeout)
	defer cancel()
	output, err := run(testCtx, filepath.Join(v.dir, name), login.Key, login.Username, login.Password)
	if err == nil {
		v.options.Logf("registry logins: test of %s signed in", registries.Describe(login))
		return protocol.RegistryTestResult{OK: true}, nil
	}
	if ctx.Err() != nil {
		return protocol.RegistryTestResult{}, ctx.Err()
	}
	registry := registries.RegistryOf(login.Key)
	result := classify(registry, output)
	if errors.Is(testCtx.Err(), context.DeadlineExceeded) {
		result = protocol.RegistryTestResult{Failure: protocol.RegistryTestUnreachable, Message: fmt.Sprintf("%s didn’t answer within %s", registry, TestTimeout)}
	}
	scrubbed := registries.NewScrubber([]protocol.RegistryLogin{login}).Scrub(strings.TrimSpace(output))
	v.options.Logf("registry logins: test of %s failed (%s): %v; podman said: %s", registries.Describe(login), result.Failure, err, scrubbed)
	return result, nil
}

// classify reads what kind of failure Podman's output describes.
func classify(registry, output string) protocol.RegistryTestResult {
	text := strings.ToLower(output)
	switch {
	case containsAny(text, "x509:", "certificate", "tls: "):
		return protocol.RegistryTestResult{Failure: protocol.RegistryTestTLS, Message: "couldn’t verify the TLS certificate of " + registry}
	case registries.Refused(output):
		return protocol.RegistryTestResult{Failure: protocol.RegistryTestDenied, Message: registry + " refused the login"}
	default:
		return protocol.RegistryTestResult{Failure: protocol.RegistryTestUnreachable, Message: "couldn’t reach " + registry}
	}
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// Relay serves the relay's registries operations from a vault. Run signs
// in for Test; nil selects PodmanLogin.
type Relay struct {
	Vault *Vault
	Run   LoginRunner
}

// Sync replaces the vault's set.
func (r Relay) Sync(set protocol.RegistrySync) (protocol.RegistrySynced, error) {
	return r.Vault.Sync(set)
}

// Test signs in with login without keeping it.
func (r Relay) Test(ctx context.Context, login protocol.RegistryLogin) (protocol.RegistryTestResult, error) {
	return r.Vault.Test(ctx, login, r.Run)
}
