package netatmo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestCallbackAddr(t *testing.T) {
	cases := []struct {
		redirect, addr, path string
		wantErr              bool
	}{
		{"http://localhost:8080/auth/callback", "localhost:8080", "/auth/callback", false},
		{"http://127.0.0.1:9000/cb", "127.0.0.1:9000", "/cb", false},
		{"http://localhost/cb", "localhost:80", "/cb", false},
		{"http://192.168.1.5:8081/cb", ":8081", "/cb", false},
		{"http://myhost.example:8081", ":8081", "/", false},
		{"https://localhost:8080/cb", "", "", true},
		{"localhost:8080/cb", "", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		addr, path, err := callbackAddr(c.redirect)
		if (err != nil) != c.wantErr {
			t.Errorf("callbackAddr(%q) err = %v, wantErr %v", c.redirect, err, c.wantErr)
			continue
		}
		if !c.wantErr && (addr != c.addr || path != c.path) {
			t.Errorf("callbackAddr(%q) = (%q, %q), want (%q, %q)", c.redirect, addr, path, c.addr, c.path)
		}
	}
}

func TestRandomStateIsUnpredictable(t *testing.T) {
	a, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomState()
	if len(a) != 32 || a == b {
		t.Errorf("states %q, %q: want distinct 32-char values", a, b)
	}
}

func TestTokenStoreSaveIsPrivateAndAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "tokens.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileTokenStore(path)
	if err := store.Save(&oauth2.Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token file mode = %o, want 600 even over a pre-existing 644 file", mode)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only tokens.json (no temp leftovers)", len(entries))
	}
	got, err := store.Load()
	if err != nil || got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Errorf("Load = (%+v, %v)", got, err)
	}
}

type seqSource struct {
	toks []*oauth2.Token
	err  error
	n    int
}

func (s *seqSource) Token() (*oauth2.Token, error) {
	if s.err != nil {
		return nil, s.err
	}
	tok := s.toks[min(s.n, len(s.toks)-1)]
	s.n++
	return tok, nil
}

func TestPersistingTokenSourceSavesOnlyChangedTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := NewFileTokenStore(path)
	a := &oauth2.Token{AccessToken: "A", RefreshToken: "rA"}
	b := &oauth2.Token{AccessToken: "B", RefreshToken: "rB"}
	p := &persistingTokenSource{src: &seqSource{toks: []*oauth2.Token{a, a, b}}, store: store, lastSaved: "A"}

	for i := 0; i < 2; i++ {
		if _, err := p.Token(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("token file written for an unchanged token (stat err = %v)", err)
	}

	if _, err := p.Token(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.AccessToken != "B" || got.RefreshToken != "rB" {
		t.Errorf("after refresh Load = (%+v, %v), want the rotated token B/rB", got, err)
	}
}

func TestPersistingTokenSourcePropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	p := &persistingTokenSource{src: &seqSource{err: boom}, store: NewFileTokenStore(filepath.Join(t.TempDir(), "t.json"))}
	if _, err := p.Token(); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func tokenServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at-new","refresh_token":"rt-new","token_type":"bearer","expires_in":10800}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetHTTPClientPersistsATokenRefreshedAtStartup(t *testing.T) {
	var hits atomic.Int32
	ts := tokenServer(t, &hits)
	path := filepath.Join(t.TempDir(), "tokens.json")
	expired := &oauth2.Token{AccessToken: "at-old", RefreshToken: "rt-old", Expiry: time.Now().Add(-time.Hour)}
	if err := NewFileTokenStore(path).Save(expired); err != nil {
		t.Fatal(err)
	}

	a := NewAuthenticator("id", "secret", "http://localhost:8080/cb", path)
	a.cfg.Endpoint.TokenURL = ts.URL
	if _, err := a.GetHTTPClient(context.Background()); err != nil {
		t.Fatal(err)
	}

	if hits.Load() != 1 {
		t.Errorf("token endpoint hit %d times, want 1", hits.Load())
	}
	got, err := NewFileTokenStore(path).Load()
	if err != nil || got.AccessToken != "at-new" || got.RefreshToken != "rt-new" {
		t.Errorf("stored token = (%+v, %v), want the refreshed one", got, err)
	}
}

func TestGetHTTPClientUsesAValidStoredTokenWithoutNetwork(t *testing.T) {
	var hits atomic.Int32
	ts := tokenServer(t, &hits)
	path := filepath.Join(t.TempDir(), "tokens.json")
	valid := &oauth2.Token{AccessToken: "at-ok", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if err := NewFileTokenStore(path).Save(valid); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	t.Cleanup(api.Close)

	a := NewAuthenticator("id", "secret", "http://localhost:8080/cb", path)
	a.cfg.Endpoint.TokenURL = ts.URL
	client, err := a.GetHTTPClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer at-ok" {
		t.Errorf("Authorization = %q, want the stored token", gotAuth)
	}
	if hits.Load() != 0 {
		t.Errorf("token endpoint hit %d times for a valid token, want 0", hits.Load())
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type authRun struct {
	redirect string
	state    string
	done     chan authResult
}

type authResult struct {
	tok *oauth2.Token
	err error
}

// startInteractiveAuth runs the interactive flow against a fake token
// endpoint and returns once the callback listener is up.
func startInteractiveAuth(t *testing.T) *authRun {
	t.Helper()
	return startInteractiveAuthWith(t, nil)
}

// startInteractiveAuthWith is startInteractiveAuth, going through Authorize
// (which also stores the token) when a store is given.
func startInteractiveAuthWith(t *testing.T, store TokenStore) *authRun {
	t.Helper()
	var hits atomic.Int32
	ts := tokenServer(t, &hits)

	redirect := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", freePort(t))
	var a *Authenticator
	if store != nil {
		a = NewAuthenticatorWithStore("id", "secret", redirect, store)
	} else {
		a = NewAuthenticator("id", "secret", redirect, filepath.Join(t.TempDir(), "tokens.json"))
	}
	a.cfg.Endpoint.TokenURL = ts.URL

	announced := make(chan string, 1)
	a.announce = func(authURL, callbackURL string) { announced <- authURL }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	run := &authRun{redirect: redirect, done: make(chan authResult, 1)}
	go func() {
		var tok *oauth2.Token
		var err error
		if store != nil {
			tok, err = a.Authorize(ctx)
		} else {
			tok, err = a.interactiveAuth(ctx)
		}
		run.done <- authResult{tok, err}
	}()

	select {
	case authURL := <-announced:
		u, err := url.Parse(authURL)
		if err != nil {
			t.Fatal(err)
		}
		run.state = u.Query().Get("state")
	case res := <-run.done:
		t.Fatalf("interactiveAuth returned early: %v", res.err)
	case <-time.After(5 * time.Second):
		t.Fatal("interactiveAuth did not announce")
	}
	return run
}

func httpGet(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestInteractiveAuthIgnoresStrayRequestsAndCompletes(t *testing.T) {
	run := startInteractiveAuth(t)

	if len(run.state) != 32 {
		t.Fatalf("state = %q, want 32 random hex chars", run.state)
	}

	// None of these come from the flow we started; none may abort it.
	for _, u := range []string{
		run.redirect,
		run.redirect + "?state=guess&code=x",
		run.redirect + "?code=x",
		run.redirect + "?state=" + run.state, // right state, but no code
	} {
		if status, _ := httpGet(t, u); status != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", u, status)
		}
	}
	select {
	case res := <-run.done:
		t.Fatalf("flow ended after stray requests: %+v", res)
	default:
	}

	if status, body := httpGet(t, run.redirect+"?state="+run.state+"&code=abc"); status != http.StatusOK || !strings.Contains(body, "successful") {
		t.Errorf("legitimate callback = %d %q", status, body)
	}
	select {
	case res := <-run.done:
		if res.err != nil || res.tok.AccessToken != "at-new" {
			t.Errorf("result = (%+v, %v)", res.tok, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flow did not finish")
	}
}

func TestInteractiveAuthReportsADeniedAuthorisation(t *testing.T) {
	run := startInteractiveAuth(t)

	httpGet(t, run.redirect+"?state="+run.state+"&error=access_denied")

	select {
	case res := <-run.done:
		if res.err == nil || !strings.Contains(res.err.Error(), "access_denied") {
			t.Errorf("err = %v, want one naming access_denied", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flow did not finish")
	}
}

func TestInteractiveAuthFailsFastWhenThePortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	redirect := fmt.Sprintf("http://127.0.0.1:%d/cb", ln.Addr().(*net.TCPAddr).Port)
	a := NewAuthenticator("id", "secret", redirect, filepath.Join(t.TempDir(), "t.json"))
	a.announce = func(string, string) { t.Error("announced although the callback port is unavailable") }

	_, err = a.interactiveAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("err = %v, want a listen error", err)
	}
}

// memTokenStore is a TokenStore with no file behind it, as a database-backed
// store would be.
type memTokenStore struct {
	tok     *oauth2.Token
	loadErr error
	saves   int
}

func (m *memTokenStore) Load() (*oauth2.Token, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.tok == nil {
		return nil, fmt.Errorf("token: %w", fs.ErrNotExist)
	}
	cp := *m.tok
	return &cp, nil
}

func (m *memTokenStore) Save(t *oauth2.Token) error {
	cp := *t
	m.tok = &cp
	m.saves++
	return nil
}

func TestStoredHTTPClientNeverStartsAnInteractiveFlow(t *testing.T) {
	store := &memTokenStore{}
	a := NewAuthenticatorWithStore("id", "secret", "http://127.0.0.1:1/cb", store)
	a.announce = func(string, string) { t.Error("interactive flow was started") }

	_, err := a.StoredHTTPClient(context.Background())

	if !errors.Is(err, ErrReauthorize) {
		t.Errorf("err = %v, want ErrReauthorize for an empty store", err)
	}
}

func TestStoredHTTPClientDoesNotMistakeAnOutageForAMissingToken(t *testing.T) {
	boom := errors.New("dynamodb: throttled")
	a := NewAuthenticatorWithStore("id", "secret", "http://127.0.0.1:1/cb", &memTokenStore{loadErr: boom})

	_, err := a.StoredHTTPClient(context.Background())

	if err == nil || errors.Is(err, ErrReauthorize) {
		t.Fatalf("err = %v: a storage failure must not be reported as 'authorise again'", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the storage error", err)
	}
}

func TestStoredHTTPClientRefreshesAndPersistsThroughANonFileStore(t *testing.T) {
	var hits atomic.Int32
	ts := tokenServer(t, &hits)
	store := &memTokenStore{tok: &oauth2.Token{AccessToken: "at-old", RefreshToken: "rt-old", Expiry: time.Now().Add(-time.Hour)}}
	a := NewAuthenticatorWithStore("id", "secret", "http://127.0.0.1:1/cb", store)
	a.cfg.Endpoint.TokenURL = ts.URL

	client, err := a.StoredHTTPClient(context.Background())
	if err != nil || client == nil {
		t.Fatalf("StoredHTTPClient = (%v, %v)", client, err)
	}
	if store.tok.AccessToken != "at-new" || store.tok.RefreshToken != "rt-new" {
		t.Errorf("stored token = %+v, want the refreshed (rotated) one", store.tok)
	}
	if hits.Load() != 1 {
		t.Errorf("token endpoint hit %d times, want 1", hits.Load())
	}
}

func TestStoredHTTPClientAsksToReauthorizeWhenTheRefreshTokenIsRevoked(t *testing.T) {
	revoked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(revoked.Close)
	store := &memTokenStore{tok: &oauth2.Token{AccessToken: "x", RefreshToken: "dead", Expiry: time.Now().Add(-time.Hour)}}
	a := NewAuthenticatorWithStore("id", "secret", "http://127.0.0.1:1/cb", store)
	a.cfg.Endpoint.TokenURL = revoked.URL

	_, err := a.StoredHTTPClient(context.Background())

	if !errors.Is(err, ErrReauthorize) {
		t.Errorf("err = %v, want ErrReauthorize", err)
	}
}

func TestStoredHTTPClientRejectsAnExpiredTokenWithNoRefreshToken(t *testing.T) {
	store := &memTokenStore{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(-time.Hour)}}
	a := NewAuthenticatorWithStore("id", "secret", "http://127.0.0.1:1/cb", store)

	if _, err := a.StoredHTTPClient(context.Background()); !errors.Is(err, ErrReauthorize) {
		t.Errorf("err = %v, want ErrReauthorize", err)
	}
}

func TestAuthorizeStoresTheTokenInTheConfiguredStore(t *testing.T) {
	store := &memTokenStore{}
	run := startInteractiveAuthWith(t, store)

	httpGet(t, run.redirect+"?state="+run.state+"&code=abc")

	select {
	case res := <-run.done:
		if res.err != nil {
			t.Fatalf("Authorize: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flow did not finish")
	}
	if store.tok == nil || store.tok.AccessToken != "at-new" || store.saves != 1 {
		t.Errorf("store = %+v after %d saves, want the new token saved once", store.tok, store.saves)
	}
}

func TestFileTokenStoreReportsAMissingFileAsNotExist(t *testing.T) {
	_, err := NewFileTokenStore(filepath.Join(t.TempDir(), "nope.json")).Load()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}
