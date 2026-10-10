package netatmo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

var netatmoEndpoint = oauth2.Endpoint{
	AuthURL:  "https://api.netatmo.com/oauth2/authorize",
	TokenURL: "https://api.netatmo.com/oauth2/token",
}

// TokenStore persists the OAuth2 token between runs. Load reports a missing
// token with an error that satisfies errors.Is(err, fs.ErrNotExist), so "not
// authorised yet" can be told apart from "storage is unavailable".
type TokenStore interface {
	Load() (*oauth2.Token, error)
	Save(*oauth2.Token) error
}

// FileTokenStore persists the token as a JSON file.
type FileTokenStore struct {
	path string
}

func NewFileTokenStore(path string) *FileTokenStore {
	return &FileTokenStore{path: path}
}

func (s *FileTokenStore) Load() (*oauth2.Token, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// Save writes the token via a temp file and rename, so a crash mid-write can
// never leave a truncated token file, and the final file is always 0600
// regardless of what permissions a pre-existing file had.
func (s *FileTokenStore) Save(tok *oauth2.Token) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tokens-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// persistingTokenSource saves a token to the store whenever the wrapped
// source hands out a different one, so refresh-token rotation survives a
// restart.
type persistingTokenSource struct {
	src   oauth2.TokenSource
	store TokenStore

	mu        sync.Mutex
	lastSaved string // access token most recently written (or loaded)
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if tok.AccessToken != p.lastSaved {
		if err := p.store.Save(tok); err != nil {
			log.Printf("Warning: could not save refreshed token: %v", err)
		} else {
			p.lastSaved = tok.AccessToken
		}
	}
	return tok, nil
}

// Authenticator manages OAuth2 authentication with Netatmo.
type Authenticator struct {
	cfg   *oauth2.Config
	store TokenStore

	// announce tells the user where to authorise and where the redirect will
	// land. It prints to the terminal; tests replace it.
	announce func(authURL, callbackURL string)
}

// NewAuthenticator returns an Authenticator that keeps its token in a file.
func NewAuthenticator(clientID, clientSecret, redirectURL, tokenFile string) *Authenticator {
	return NewAuthenticatorWithStore(clientID, clientSecret, redirectURL, NewFileTokenStore(tokenFile))
}

// NewAuthenticatorWithStore returns an Authenticator that keeps its token in
// store, for deployments with no writable home directory.
func NewAuthenticatorWithStore(clientID, clientSecret, redirectURL string, store TokenStore) *Authenticator {
	return &Authenticator{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Scopes:       []string{"read_station"},
			Endpoint:     netatmoEndpoint,
		},
		store:    store,
		announce: printAuthPrompt,
	}
}

func printAuthPrompt(authURL, callbackURL string) {
	fmt.Println("\n=== Netatmo Authentication Required ===")
	fmt.Printf("Open this URL in your browser:\n\n  %s\n\n", authURL)
	fmt.Printf("Waiting for the redirect on %s ...\n", callbackURL)
}

// ErrReauthorize means there is no stored token that can be used, and the
// OAuth2 authorisation has to be run again (see Authorize).
var ErrReauthorize = errors.New("no usable stored token; authorise again")

// StoredHTTPClient returns an HTTP client built from the stored token, without
// ever starting an interactive flow: the right thing for unattended runs. A
// token that has expired is refreshed immediately, so a revoked refresh token
// shows up here rather than on the first API call. Anything the client later
// refreshes is written back to the store.
//
// It returns an error wrapping ErrReauthorize when there is no stored token,
// or it can no longer be refreshed; any other error is a failure to read the
// store, which says nothing about the token.
func (a *Authenticator) StoredHTTPClient(ctx context.Context) (*http.Client, error) {
	tok, err := a.store.Load()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: nothing stored yet", ErrReauthorize)
	case err != nil:
		return nil, fmt.Errorf("load token: %w", err)
	case !tok.Valid() && tok.RefreshToken == "":
		return nil, fmt.Errorf("%w: stored token has expired and has no refresh token", ErrReauthorize)
	}

	ts := a.persistingSource(ctx, tok)
	if tok.Valid() {
		return oauth2.NewClient(ctx, ts), nil
	}
	if _, err := ts.Token(); err != nil {
		return nil, fmt.Errorf("%w: refresh failed: %v", ErrReauthorize, err)
	}
	return oauth2.NewClient(ctx, ts), nil
}

// Authorize runs the interactive OAuth2 flow and stores the resulting token.
func (a *Authenticator) Authorize(ctx context.Context) (*oauth2.Token, error) {
	tok, err := a.interactiveAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}
	if err := a.store.Save(tok); err != nil {
		return nil, fmt.Errorf("save token: %w", err)
	}
	return tok, nil
}

// GetHTTPClient returns an HTTP client with a valid token, running the OAuth2
// flow interactively if no usable stored token exists. Any token the client
// later refreshes is written back to the store.
func (a *Authenticator) GetHTTPClient(ctx context.Context) (*http.Client, error) {
	client, err := a.StoredHTTPClient(ctx)
	if err == nil {
		return client, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("Stored token not usable (%v), re-authenticating", err)
	}

	tok, err := a.interactiveAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}
	if saveErr := a.store.Save(tok); saveErr != nil {
		log.Printf("Warning: could not save token: %v", saveErr)
	}
	return oauth2.NewClient(ctx, a.persistingSource(ctx, tok)), nil
}

func (a *Authenticator) persistingSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	return &persistingTokenSource{
		src:       a.cfg.TokenSource(ctx, tok),
		store:     a.store,
		lastSaved: tok.AccessToken,
	}
}

// callbackAddr derives the listen address and URL path for the OAuth callback
// from the configured redirect URL, so the listener always matches what was
// registered with Netatmo.
func callbackAddr(redirectURL string) (addr, path string, err error) {
	u, err := url.Parse(redirectURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid NETATMO_REDIRECT_URL: %w", err)
	}
	if u.Scheme != "http" || u.Hostname() == "" {
		return "", "", fmt.Errorf("NETATMO_REDIRECT_URL %q must be an http:// URL with a host", redirectURL)
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	path = u.Path
	if path == "" {
		path = "/"
	}

	// Only expose the callback beyond this machine when the redirect URL
	// itself points somewhere other than loopback.
	host := u.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return net.JoinHostPort(host, port), path, nil
	}
	return net.JoinHostPort("", port), path, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *Authenticator) interactiveAuth(ctx context.Context) (*oauth2.Token, error) {
	addr, path, err := callbackAddr(a.cfg.RedirectURL)
	if err != nil {
		return nil, err
	}
	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen for OAuth callback on %s: %w", addr, err)
	}

	a.announce(a.cfg.AuthCodeURL(state, oauth2.AccessTypeOffline), "http://"+ln.Addr().String()+path)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	deliver := func(r result) {
		select {
		case resCh <- r:
		default: // already delivered; ignore later requests
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Requests that don't carry our state are not from the flow we
		// started; reject them without disturbing it.
		if q.Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "authorization failed: "+e, http.StatusBadRequest)
			deliver(result{err: fmt.Errorf("authorization denied: %s", e)})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Authentication successful — you can close this tab.")
		deliver(result{code: code})
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			deliver(result{err: err})
		}
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		return a.cfg.Exchange(ctx, res.code)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
