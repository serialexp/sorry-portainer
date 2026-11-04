package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// ControlTLS configures the mTLS control listener. The server issues its own
// certificate from the CA for ServerNames and keeps it in CredentialFile.
type ControlTLS struct {
	ListenAddr     string
	CAFile         string
	CAKeyFile      string
	CredentialFile string
	// ServerNames are the DNS names and IP addresses agents use to reach the
	// control listener.
	ServerNames []string
}

func ControlTLSFromEnv() (ControlTLS, error) {
	c := ControlTLS{
		ListenAddr:     getenv("SORRY_PORTAINER_CONTROL_LISTEN", ":9443"),
		CAFile:         os.Getenv("SORRY_PORTAINER_CONTROL_CA"),
		CAKeyFile:      os.Getenv("SORRY_PORTAINER_CONTROL_CA_KEY"),
		CredentialFile: os.Getenv("SORRY_PORTAINER_CONTROL_CREDENTIAL"),
	}
	if names := os.Getenv("SORRY_PORTAINER_CONTROL_SERVER_NAMES"); names != "" {
		c.ServerNames = strings.Split(names, ",")
	}
	if err := validateControl(c); err != nil {
		return ControlTLS{}, err
	}
	return c, nil
}

var dnsNamePattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// ValidServerName reports whether name can go in a certificate: a DNS name
// or an IP address, never host:port.
func ValidServerName(name string) bool {
	if net.ParseIP(name) != nil {
		return true
	}
	return len(name) <= 253 && dnsNamePattern.MatchString(name)
}

// AgentURL is the relay URL agents dial: the first server name and the
// listener's port.
func (c ControlTLS) AgentURL() string {
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil || port == "" {
		port = "9443"
	}
	return "wss://" + net.JoinHostPort(c.ServerNames[0], port) + "/control/agent"
}

func validateControl(c ControlTLS) error {
	if c.ListenAddr == ":80" || c.ListenAddr == ":443" {
		return errors.New("control listener must not use port 80 or 443")
	}
	for n, v := range map[string]string{"control CA": c.CAFile, "control CA key": c.CAKeyFile, "control credential": c.CredentialFile} {
		if v == "" {
			return fmt.Errorf("%s is required", n)
		}
	}
	if len(c.ServerNames) == 0 {
		return errors.New("control server names are required")
	}
	for _, name := range c.ServerNames {
		if !ValidServerName(name) {
			return fmt.Errorf("control server name %q is not a DNS name or IP address", name)
		}
	}
	return nil
}
