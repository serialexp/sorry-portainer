package agentcred

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	requestTimeout  = 30 * time.Second
	maxResponseBody = 64 << 10
)

// controlURL turns the relay URL (wss://host:port/control/agent) into the
// URL of another control route on the same listener.
func controlURL(serverURL, path string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "wss" || u.Host == "" {
		return "", errors.New("server URL must be wss://host[:port]/…")
	}
	return (&url.URL{Scheme: "https", Host: u.Host, Path: path}).String(), nil
}

// ServerName is the name the master's certificate is checked for when none
// is configured: the server URL's host, without its port.
func ServerName(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	if u.Hostname() == "" {
		return "", errors.New("server URL has no host")
	}
	return u.Hostname(), nil
}

// ControlError is a refusal from the master, with its stable code.
type ControlError struct {
	Status  int
	Code    string
	Message string
}

func (e *ControlError) Error() string {
	return fmt.Sprintf("master refused (%d %s): %s", e.Status, e.Code, e.Message)
}

// post sends one JSON request over a fresh connection that is closed
// afterwards, so a connection made with an old identity is never reused.
// Redirects are refused.
func post(ctx context.Context, tlsConfig *tls.Config, target string, body, out any) error {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the master's control listener must not redirect")
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return err
	}
	if len(payload) > maxResponseBody {
		return errors.New("master's response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(payload, &failure)
		return &ControlError{Status: resp.StatusCode, Code: failure.Code, Message: failure.Error}
	}
	return json.Unmarshal(payload, out)
}
