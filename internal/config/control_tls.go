package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

type ControlTLS struct {
	ListenAddr string
	CAFile     string
	CertFile   string
	KeyFile    string
}

func ControlTLSFromEnv() (ControlTLS, error) {
	c := ControlTLS{ListenAddr: getenv("SORRY_PORTAINER_CONTROL_LISTEN", ":9443"), CAFile: os.Getenv("SORRY_PORTAINER_CONTROL_CA"), CertFile: os.Getenv("SORRY_PORTAINER_CONTROL_CERT"), KeyFile: os.Getenv("SORRY_PORTAINER_CONTROL_KEY")}
	if c.ListenAddr == ":80" || c.ListenAddr == ":443" {
		return ControlTLS{}, errors.New("control listener must not use port 80 or 443")
	}
	for name, value := range map[string]string{"SORRY_PORTAINER_CONTROL_CA": c.CAFile, "SORRY_PORTAINER_CONTROL_CERT": c.CertFile, "SORRY_PORTAINER_CONTROL_KEY": c.KeyFile} {
		if value == "" {
			return ControlTLS{}, fmt.Errorf("%s is required", name)
		}
	}
	return c, nil
}

func LoadServerTLS(c ControlTLS) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, err
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("control CA contains no certificates")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}, nil
}
