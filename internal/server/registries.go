package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// Test states of the web API: a sign-in test's outcome, or no_agent when no
// online host answered.
const (
	TestOK      = secretstore.LoginTestOK
	TestFailed  = secretstore.LoginTestFailed
	TestNoAgent = "no_agent"
)

// LoginTestResult is the answer of a sign-in test.
type LoginTestResult struct {
	State   string `json:"state"`
	Failure string `json:"failure,omitempty"`
	Message string `json:"message,omitempty"`
	Host    string `json:"host,omitempty"`
}

type registriesResponse struct {
	State  secretstore.State   `json:"state"`
	SetID  string              `json:"set_id"`
	Logins []secretstore.Login `json:"logins"`
	Hosts  []LoginHostStatus   `json:"hosts"`
}

type loginRequest struct {
	Key      string `json:"key"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginKeyRequest struct {
	Key        string `json:"key"`
	Passphrase string `json:"passphrase,omitempty"`
}

type loginSaved struct {
	Login secretstore.Login `json:"login"`
	Test  LoginTestResult   `json:"test"`
}

type loginRevealed struct {
	Password string `json:"password"`
}

type retryRequest struct {
	Host string `json:"host"`
}

// loginStatus maps a registry login error to an HTTP status.
func loginStatus(err error) int {
	switch {
	case errors.Is(err, secretstore.ErrLoginExists):
		return http.StatusConflict
	case errors.Is(err, secretstore.ErrLoginNotFound):
		return http.StatusNotFound
	case errors.Is(err, registries.ErrKeyEmpty), errors.Is(err, registries.ErrKeyLong), errors.Is(err, registries.ErrKeyScheme),
		errors.Is(err, registries.ErrKeySpace), errors.Is(err, registries.ErrKeyRef), errors.Is(err, registries.ErrKeyPath),
		errors.Is(err, registries.ErrUsername), errors.Is(err, registries.ErrPassword), errors.Is(err, registries.ErrTooManyLogin):
		return http.StatusBadRequest
	default:
		return secretStatus(err)
	}
}

// registriesRoute serves /api/registries and /api/registries/{action}.
// Keys contain "/", so they travel in request bodies, never in the path.
// Bodies are never logged: they carry passwords and passphrases.
func (s *Server) registriesRoute(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		http.Error(w, "secret store not configured", http.StatusServiceUnavailable)
		return
	}
	action := ""
	if r.URL.Path != "/api/registries" {
		action = r.URL.Path[len("/api/registries/"):]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.listLogins(w)
	case r.Method != http.MethodPost:
		http.NotFound(w, r)
	case action == "":
		s.saveLogin(w, r, false)
	case action == "replace":
		s.saveLogin(w, r, true)
	case action == "test":
		var req loginKeyRequest
		if jsonDecode(w, r, &req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		result, err := s.testLogin(r.Context(), req.Key)
		if err != nil {
			http.Error(w, err.Error(), loginStatus(err))
			return
		}
		writeJSON(w, result)
	case action == "reveal":
		s.revealLogin(w, r)
	case action == "delete":
		var req loginKeyRequest
		if jsonDecode(w, r, &req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		// Deleting needs the key only to push the remaining set.
		if s.secrets.State() != secretstore.Unlocked {
			http.Error(w, secretstore.ErrLocked.Error(), http.StatusLocked)
			return
		}
		if err := s.secrets.DeleteLogin(req.Key); err != nil {
			http.Error(w, err.Error(), loginStatus(err))
			return
		}
		s.logins.changed()
		w.WriteHeader(http.StatusNoContent)
	case action == "retry":
		var req retryRequest
		if jsonDecode(w, r, &req) != nil || !protocol.ValidHostID(req.Host) {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		if s.secrets.State() != secretstore.Unlocked {
			http.Error(w, secretstore.ErrLocked.Error(), http.StatusLocked)
			return
		}
		s.logins.retryNow(req.Host)
		w.WriteHeader(http.StatusAccepted)
	default:
		http.NotFound(w, r)
	}
}

// knownHosts are the enrolled hosts and the connected ones, by ID.
func (s *Server) knownHosts() ([]string, map[string]bool) {
	online := map[string]bool{}
	var ids []string
	for _, info := range s.relay.Hosts() {
		online[info.HostID] = true
		ids = append(ids, info.HostID)
	}
	if s.enrollment != nil {
		for _, host := range s.enrollment.Registry().Statuses(nil) {
			if !online[host.HostID] {
				ids = append(ids, host.HostID)
			}
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), online
}

func (s *Server) listLogins(w http.ResponseWriter) {
	logins, err := s.secrets.Logins()
	if err != nil {
		http.Error(w, err.Error(), loginStatus(err))
		return
	}
	setID, err := s.secrets.RegistrySetID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if logins == nil {
		logins = []secretstore.Login{}
	}
	hosts, online := s.knownHosts()
	writeJSON(w, registriesResponse{State: s.secrets.State(), SetID: setID, Logins: logins, Hosts: s.logins.statuses(hosts, online, setID)})
}

// saveLogin adds or replaces a login, starts the push and tests the login.
// A failed or skipped test never undoes the save.
func (s *Server) saveLogin(w http.ResponseWriter, r *http.Request, replace bool) {
	var req loginRequest
	if jsonDecode(w, r, &req) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	password := []byte(req.Password)
	defer clear(password)
	var login secretstore.Login
	var err error
	if replace {
		login, err = s.secrets.ReplaceLogin(req.Key, req.Username, password, s.logins.now())
	} else {
		login, err = s.secrets.AddLogin(req.Key, req.Username, password, s.logins.now())
	}
	if err != nil {
		http.Error(w, err.Error(), loginStatus(err))
		return
	}
	log.Printf("registry logins: saved %s (user %s)", login.Key, login.Username)
	s.logins.changed()
	test, err := s.testLogin(r.Context(), login.Key)
	if err != nil {
		test = LoginTestResult{State: TestNoAgent, Message: err.Error()}
	}
	if test.State != TestNoAgent {
		// Read back, for the last test the save recorded.
		if logins, err := s.secrets.Logins(); err == nil {
			if i := slices.IndexFunc(logins, func(l secretstore.Login) bool { return l.Key == login.Key }); i >= 0 {
				login = logins[i]
			}
		}
	}
	writeJSON(w, loginSaved{Login: login, Test: test})
}

// testLogin has the online hosts, in host-ID order, sign in with the stored
// login until one answers, and keeps the answer with the login. A busy host,
// or one that doesn't answer within loginTestTimeout, is skipped. With no
// answer the result is no_agent, and the next host to connect tests it.
func (s *Server) testLogin(ctx context.Context, key string) (LoginTestResult, error) {
	login, version, err := s.secrets.LoginForTest(key)
	if err != nil {
		return LoginTestResult{}, err
	}
	defer clear(login.Password)
	for _, host := range onlineHosts(s.relay) {
		if result, ok := s.testOn(ctx, host, login, version); ok {
			return result, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return LoginTestResult{State: TestNoAgent}, nil
}

// testOn runs one sign-in test on host and records it; ok is false when the
// host gave no answer.
func (s *Server) testOn(ctx context.Context, host string, login protocol.RegistryLogin, version time.Time) (LoginTestResult, bool) {
	callCtx, cancel := context.WithTimeout(ctx, loginTestTimeout)
	answer, err := s.relay.TestRegistry(callCtx, host, login)
	cancel()
	if err != nil {
		log.Printf("registry logins: test of %s on %s: %v", login.Key, host, err)
		return LoginTestResult{}, false
	}
	result := LoginTestResult{State: TestOK, Host: host}
	if !answer.OK {
		result = LoginTestResult{State: TestFailed, Failure: answer.Failure, Message: answer.Message, Host: host}
	}
	test := secretstore.LoginTest{State: result.State, Failure: result.Failure, Message: result.Message, Host: host, At: s.logins.now()}
	if err := s.secrets.RecordLoginTest(login.Key, version, test); err != nil {
		log.Printf("registry logins: keep the test of %s: %v", login.Key, err)
	}
	return result, true
}

// testUntested tests, on a host that just connected, each login that has
// never been tested: one saved while no host was online.
func (s *Server) testUntested(ctx context.Context, host string) {
	if s.secrets.State() != secretstore.Unlocked {
		return
	}
	logins, err := s.secrets.Logins()
	if err != nil {
		log.Printf("registry logins: list: %v", err)
		return
	}
	for _, l := range logins {
		if l.LastTest != nil || !s.logins.startTest(l.Key) {
			continue
		}
		login, version, err := s.secrets.LoginForTest(l.Key)
		if err == nil {
			s.testOn(ctx, host, login, version)
			clear(login.Password)
		}
		s.logins.endTest(l.Key)
	}
}

func (p *loginPusher) startTest(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.testing[key] {
		return false
	}
	p.testing[key] = true
	return true
}

func (p *loginPusher) endTest(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.testing, key)
}

// revealLogin answers one stored password after checking the passphrase
// again, through the passphrase limiter. The log names the key and the
// client, never the password.
func (s *Server) revealLogin(w http.ResponseWriter, r *http.Request) {
	attempt, ok := s.takePassphraseAttempt(w, r)
	if !ok {
		return
	}
	var req loginKeyRequest
	if jsonDecode(w, r, &req) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	password, err := s.secrets.RevealLogin(req.Key, req.Passphrase)
	s.settlePassphraseAttempt(attempt, err)
	if errors.Is(err, secretstore.ErrWrongPassphrase) {
		log.Printf("registry logins: reveal of %s refused for %s: wrong passphrase", req.Key, attempt.client)
	}
	if err != nil {
		http.Error(w, err.Error(), loginStatus(err))
		return
	}
	defer clear(password)
	log.Printf("registry logins: revealed %s to %s", req.Key, attempt.client)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, loginRevealed{Password: string(password)})
}

// loginView is what the master knows about one host's logins, for
// explaining a registry's refusal on a stack page.
type loginView struct {
	keys []string
	// current is whether the host has the current set.
	current bool
}

// loginViewFor reads the saved keys and compares the host's set; ok is false
// without a secret store or when the store can't be read.
func (s *Server) loginViewFor(host string) (loginView, bool) {
	if s.secrets == nil {
		return loginView{}, false
	}
	logins, err := s.secrets.Logins()
	if err != nil {
		return loginView{}, false
	}
	setID, err := s.secrets.RegistrySetID()
	if err != nil {
		return loginView{}, false
	}
	keys := make([]string, len(logins))
	for i, login := range logins {
		keys[i] = login.Key
	}
	return loginView{keys: keys, current: s.logins.hostSetID(host) == setID}, true
}

// explain says which saved login matches ref and why it didn't work.
func (v loginView) explain(ref string) (key, state string) {
	key = registries.MatchIn(ref, v.keys)
	switch {
	case key == "":
		return "", protocol.LoginNone
	case !v.current:
		return key, protocol.LoginNotOnHost
	default:
		return key, protocol.LoginRefused
	}
}

// explainImages fills LoginKey and LoginState for each refused image.
func (s *Server) explainImages(host string, images *protocol.StackImages) {
	if !slices.ContainsFunc(images.Services, func(i protocol.ServiceImage) bool { return i.Failure == protocol.ImageFailureDenied }) {
		return
	}
	view, ok := s.loginViewFor(host)
	if !ok {
		return
	}
	for i := range images.Services {
		if image := &images.Services[i]; image.Failure == protocol.ImageFailureDenied {
			image.LoginKey, image.LoginState = view.explain(image.Image)
		}
	}
}

// explainWaiting fills WaitingLoginKey and WaitingLoginState for each stack
// waiting for a registry login.
func (s *Server) explainWaiting(host string, stacks []protocol.Stack) {
	if !slices.ContainsFunc(stacks, func(st protocol.Stack) bool { return st.WaitingLogin != "" }) {
		return
	}
	view, ok := s.loginViewFor(host)
	if !ok {
		return
	}
	for i := range stacks {
		if stack := &stacks[i]; stack.WaitingLogin != "" {
			stack.WaitingLoginKey, stack.WaitingLoginState = view.explain(stack.WaitingLogin)
		}
	}
}
