package enrollment

import (
	"crypto/ecdsa"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const (
	DefaultTokenTTL = time.Hour
	MaxTokenTTL     = 24 * time.Hour
	MinTokenTTL     = time.Minute
	// MaxTokens bounds the tokens held in memory, used or not.
	MaxTokens = 64
)

// Error is a failure with a stable code and the HTTP status it maps to.
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(code string, status int, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}

const (
	CodeInvalidRequest = "invalid_request"
	CodeInvalidToken   = "invalid_token"
	CodeWrongHost      = "wrong_host"
	CodeUnsupportedKey = "unsupported_key"
	CodeUnavailable    = "unavailable"
	CodeNotEnrolled    = "not_enrolled"
	CodeTooManyTokens  = "too_many_tokens"
	CodeNotFound       = "not_found"
)

// invalidToken is one answer for unknown, expired, wrong-secret and
// already-used tokens, so a caller learns nothing about which.
func invalidToken() *Error {
	return fail(CodeInvalidToken, http.StatusUnauthorized, "enrollment token is invalid, expired or already used")
}

type tokenEntry struct {
	hostID     string
	secretHash [32]byte
	expires    time.Time
	// Set once an enrollment committed with the token, so that the same
	// agent retrying with the same key gets the same certificate.
	usedKey *ecdsa.PublicKey
	issued  *x509.Certificate
}

// Service enrolls, renews and admits agents. Tokens live only in memory.
type Service struct {
	ca       *pki.CA
	registry *Registry
	now      func() time.Time
	// disconnect closes a host's live session, if any. It is called after
	// the registry change that requires it has been published.
	disconnect func(hostID string)

	// mu serializes token changes and every registry change, so that a
	// commit sees the token and host state its checks saw.
	mu          sync.Mutex
	tokens      map[string]*tokenEntry // by token ID
	tokenByHost map[string]string

	// renewChecked, when set by tests, runs between a renewal's lock-free
	// check and its commit.
	renewChecked func()
}

// NewService creates the service. now may be nil for time.Now.
func NewService(ca *pki.CA, registry *Registry, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{ca: ca, registry: registry, now: now, disconnect: func(string) {}, tokens: map[string]*tokenEntry{}, tokenByHost: map[string]string{}}
}

// SetDisconnect installs the function that closes a host's live session.
func (s *Service) SetDisconnect(disconnect func(hostID string)) { s.disconnect = disconnect }

func (s *Service) Registry() *Registry { return s.registry }

// CreateToken issues a token for hostID, replacing any earlier one for it.
func (s *Service) CreateToken(hostID string, ttl time.Duration) (string, time.Time, error) {
	if !protocol.ValidHostID(hostID) {
		return "", time.Time{}, fail(CodeInvalidRequest, http.StatusBadRequest, "invalid host ID %q", hostID)
	}
	if ttl == 0 {
		ttl = DefaultTokenTTL
	}
	if ttl < MinTokenTTL || ttl > MaxTokenTTL {
		return "", time.Time{}, fail(CodeInvalidRequest, http.StatusBadRequest, "token lifetime must be between %s and %s", MinTokenTTL, MaxTokenTTL)
	}
	token, err := newToken(s.ca.Fingerprint())
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	s.revokeLocked(hostID)
	if len(s.tokens) >= MaxTokens {
		// Used tokens are only kept so a lost response can be retried; they
		// give way before a new token is refused.
		for id, entry := range s.tokens {
			if entry.issued != nil {
				s.dropLocked(id)
			}
		}
	}
	if len(s.tokens) >= MaxTokens {
		return "", time.Time{}, fail(CodeTooManyTokens, http.StatusConflict, "%d enrollment tokens are outstanding; revoke one or let them expire", MaxTokens)
	}
	expires := now.Add(ttl)
	s.tokens[token.ID] = &tokenEntry{hostID: hostID, secretHash: hashSecret(token.Secret), expires: expires}
	s.tokenByHost[hostID] = token.ID
	return token.String(), expires, nil
}

// TokenInfo describes an outstanding token, never its secret.
type TokenInfo struct {
	HostID    string    `json:"host_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Tokens lists unused, unexpired tokens ordered by host.
func (s *Service) Tokens() []TokenInfo {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	out := []TokenInfo{}
	for _, host := range slices.Sorted(maps.Keys(s.tokenByHost)) {
		entry := s.tokens[s.tokenByHost[host]]
		if entry.issued == nil {
			out = append(out, TokenInfo{HostID: host, ExpiresAt: entry.expires})
		}
	}
	return out
}

// RevokeToken drops hostID's token; it reports whether there was one.
func (s *Service) RevokeToken(hostID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeLocked(hostID)
}

func (s *Service) revokeLocked(hostID string) bool {
	id, ok := s.tokenByHost[hostID]
	if ok {
		s.dropLocked(id)
	}
	return ok
}

func (s *Service) dropLocked(id string) {
	entry := s.tokens[id]
	if entry == nil {
		return
	}
	delete(s.tokens, id)
	if s.tokenByHost[entry.hostID] == id {
		delete(s.tokenByHost, entry.hostID)
	}
}

func (s *Service) purgeLocked(now time.Time) {
	for id, entry := range s.tokens {
		if !now.Before(entry.expires) {
			s.dropLocked(id)
		}
	}
}

// EnrollRequest is the body of POST /control/enroll.
type EnrollRequest struct {
	Token  string `json:"token"`
	HostID string `json:"host_id"`
	Prefix string `json:"prefix"`
	CSR    string `json:"csr"`
}

// EnrollResponse is its answer.
type EnrollResponse struct {
	Certificate string `json:"certificate"`
	CA          string `json:"ca"`
}

// Enrolled describes a successful enrollment for the caller's log; it never
// carries the token secret.
type Enrolled struct {
	HostID  string
	TokenID string
	Retry   bool
}

// Enroll checks a token and a certificate request and, if both are good,
// signs a certificate and records it as the host's current one. A wrong host
// or a bad request does not spend the token; a retry with the same key after
// a lost response gets the same certificate.
func (s *Service) Enroll(req EnrollRequest) (EnrollResponse, Enrolled, error) {
	token, err := ParseToken(req.Token)
	if err != nil {
		return EnrollResponse{}, Enrolled{}, fail(CodeInvalidRequest, http.StatusBadRequest, "%v", err)
	}
	done := Enrolled{HostID: req.HostID, TokenID: token.ID}
	if !protocol.ValidHostID(req.HostID) {
		return EnrollResponse{}, done, fail(CodeInvalidRequest, http.StatusBadRequest, "invalid host ID")
	}
	if !protocol.ValidHostPrefix(req.Prefix) {
		return EnrollResponse{}, done, fail(CodeInvalidRequest, http.StatusBadRequest, "invalid host prefix")
	}
	now := s.now()
	s.mu.Lock()
	entry := s.tokens[token.ID]
	if entry == nil || !now.Before(entry.expires) {
		s.mu.Unlock()
		return EnrollResponse{}, done, invalidToken()
	}
	if hash := hashSecret(token.Secret); subtle.ConstantTimeCompare(hash[:], entry.secretHash[:]) != 1 {
		s.mu.Unlock()
		return EnrollResponse{}, done, invalidToken()
	}
	if req.HostID != entry.hostID {
		s.mu.Unlock()
		return EnrollResponse{}, done, fail(CodeWrongHost, http.StatusForbidden, "enrollment token is for another host")
	}
	pub, err := pki.ParseCSR([]byte(req.CSR))
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, pki.ErrUnsupportedKey) {
			return EnrollResponse{}, done, fail(CodeUnsupportedKey, http.StatusBadRequest, "%v", err)
		}
		return EnrollResponse{}, done, fail(CodeInvalidRequest, http.StatusBadRequest, "%v", err)
	}
	if entry.issued != nil {
		s.mu.Unlock()
		if !entry.usedKey.Equal(pub) {
			return EnrollResponse{}, done, invalidToken()
		}
		done.Retry = true
		return s.enrollResponse(entry.issued), done, nil
	}
	cert, err := s.ca.IssueAgent(pub, req.HostID, now)
	if err != nil {
		s.mu.Unlock()
		return EnrollResponse{}, done, err
	}
	err = s.registry.update(func(hosts map[string]Host) error {
		hosts[req.HostID] = Host{EnrolledAt: now.UTC(), Prefix: req.Prefix, Current: refOf(cert, false)}
		return nil
	})
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, errPublished) {
			// The published entry no longer accepts the earlier certificate.
			s.disconnect(req.HostID)
		}
		return EnrollResponse{}, done, fail(CodeUnavailable, http.StatusServiceUnavailable, "cannot record the enrollment: %v", err)
	}
	entry.usedKey, entry.issued = pub, cert
	s.mu.Unlock()
	// The host's earlier certificate, if any, is no longer accepted.
	s.disconnect(req.HostID)
	return s.enrollResponse(cert), done, nil
}

func (s *Service) enrollResponse(cert *x509.Certificate) EnrollResponse {
	return EnrollResponse{Certificate: string(pki.EncodeCertificate(cert)), CA: string(pki.EncodeCertificate(s.ca.Cert))}
}

// Standing is how a presented certificate relates to its host's entry.
type Standing int

const (
	StandingCurrent Standing = iota + 1
	StandingPending
)

// Check reports whether cert may be used for hostID right now, without
// locking or waiting for the disk.
func (s *Service) Check(hostID string, cert *x509.Certificate) (Standing, error) {
	now := s.now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return 0, fail(CodeNotEnrolled, http.StatusForbidden, "certificate for %s is not valid now", hostID)
	}
	host, ok := s.registry.Get(hostID)
	if !ok {
		return 0, fail(CodeNotEnrolled, http.StatusForbidden, "host %s is not enrolled", hostID)
	}
	fp := pki.FingerprintOf(cert)
	switch {
	case fp.Matches(host.Current.SHA256):
		return StandingCurrent, nil
	case host.Pending.SHA256 != "" && fp.Matches(host.Pending.SHA256):
		return StandingPending, nil
	}
	return 0, fail(CodeNotEnrolled, http.StatusForbidden, "certificate for %s has been replaced or revoked", hostID)
}

// Promote makes a pending certificate current, durably, and drops the one it
// replaces. The TLS handshake that presented it proved the agent holds the
// new key and has installed it.
func (s *Service) Promote(hostID string, cert *x509.Certificate) error {
	fp := pki.FingerprintOf(cert).String()
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.registry.update(func(hosts map[string]Host) error {
		host, ok := hosts[hostID]
		if !ok || host.Pending.SHA256 != fp {
			// Removed, re-enrolled or promoted meanwhile; the caller's
			// admission check decides.
			return errNoChange
		}
		host.Current = CertRef{SHA256: host.Pending.SHA256, NotAfter: host.Pending.NotAfter}
		host.Pending = CertRef{}
		hosts[hostID] = host
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	if err != nil {
		return fail(CodeUnavailable, http.StatusServiceUnavailable, "cannot record the renewed certificate: %v", err)
	}
	return nil
}

var errNoChange = errors.New("no change")

// Authorize is the final admission check, run while the relay registers the
// session: only the host's current certificate is admitted.
func (s *Service) Authorize(hostID string, cert *x509.Certificate) error {
	standing, err := s.Check(hostID, cert)
	if err != nil {
		return err
	}
	if standing != StandingCurrent {
		return fail(CodeNotEnrolled, http.StatusForbidden, "certificate for %s has not been promoted", hostID)
	}
	return nil
}

// Renew signs a successor for the host's current certificate and records it
// as pending. A repeated request for the same key returns the same
// certificate.
func (s *Service) Renew(hostID string, presented *x509.Certificate, csrPEM []byte) (*x509.Certificate, error) {
	standing, err := s.Check(hostID, presented)
	if err != nil {
		return nil, err
	}
	if standing != StandingCurrent {
		return nil, fail(CodeNotEnrolled, http.StatusForbidden, "renew with the current certificate of %s", hostID)
	}
	pub, err := pki.ParseCSR(csrPEM)
	if err != nil {
		if errors.Is(err, pki.ErrUnsupportedKey) {
			return nil, fail(CodeUnsupportedKey, http.StatusBadRequest, "%v", err)
		}
		return nil, fail(CodeInvalidRequest, http.StatusBadRequest, "%v", err)
	}
	authorizedBy := pki.FingerprintOf(presented).String()
	if s.renewChecked != nil {
		s.renewChecked()
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.registry.Get(hostID)
	if !ok || host.Current.SHA256 != authorizedBy {
		return nil, fail(CodeNotEnrolled, http.StatusForbidden, "certificate for %s was replaced or revoked during renewal", hostID)
	}
	if host.Pending != (CertRef{}) {
		pending, err := pki.ParseCertificatePEM([]byte(host.Pending.Certificate))
		if err == nil && pub.Equal(pending.PublicKey) {
			return pending, nil
		}
	}
	cert, err := s.ca.IssueAgent(pub, hostID, now)
	if err != nil {
		return nil, err
	}
	err = s.registry.update(func(hosts map[string]Host) error {
		host, ok := hosts[hostID]
		if !ok || host.Current.SHA256 != authorizedBy {
			return fail(CodeNotEnrolled, http.StatusForbidden, "certificate for %s was replaced or revoked during renewal", hostID)
		}
		host.Pending = refOf(cert, true)
		hosts[hostID] = host
		return nil
	})
	var coded *Error
	if errors.As(err, &coded) {
		return nil, coded
	}
	if err != nil {
		return nil, fail(CodeUnavailable, http.StatusServiceUnavailable, "cannot record the renewed certificate: %v", err)
	}
	return cert, nil
}

// Remove forgets a host and revokes its token, then closes its session.
func (s *Service) Remove(hostID string) error {
	s.mu.Lock()
	hadToken := s.revokeLocked(hostID)
	_, enrolled := s.registry.Get(hostID)
	var err error
	if enrolled {
		err = s.registry.update(func(hosts map[string]Host) error {
			delete(hosts, hostID)
			return nil
		})
	}
	s.mu.Unlock()
	if err != nil {
		if errors.Is(err, errPublished) {
			// The host is gone from the published registry, so its session
			// must end even though the removal may not be durable.
			s.disconnect(hostID)
		}
		return fail(CodeUnavailable, http.StatusServiceUnavailable, "cannot record the removal: %v", err)
	}
	if !enrolled && !hadToken {
		return fail(CodeNotFound, http.StatusNotFound, "host %s is not enrolled and has no token", hostID)
	}
	s.disconnect(hostID)
	return nil
}
