package agentsetup

import "fmt"

// The production agent layout `setup` installs.
const (
	SystemAgentBinary = "/usr/local/bin/sorry-portainer-agent"
	SystemAgentUnit   = "/etc/systemd/system/sorry-portainer-agent.service"
	// SystemAgentDir holds the agent's configuration, credentials and state.
	// The agent user owns it, because the agent replaces its own certificate.
	SystemAgentDir = "/var/lib/sorry-portainer-agent"
)

// EnrollCommand is what an administrator runs on a set-up host to enroll it.
// The token is pasted when the command asks for it, so it never appears on a
// command line.
func EnrollCommand(user, serverURL, hostID string) string {
	return fmt.Sprintf("sudo runuser -u %s -- %s enroll --server-url %s --host-id %s --dir %s --token-file -",
		user, SystemAgentBinary, serverURL, hostID, SystemAgentDir)
}
