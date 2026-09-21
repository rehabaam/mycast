package netatmo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
)

var netatmoEndpoint = oauth2.Endpoint{
	AuthURL:  "https://api.netatmo.com/oauth2/authorize",
	TokenURL: "https://api.netatmo.com/oauth2/token",
}

// TokenStore persists OAuth2 tokens to disk.
type TokenStore struct {
	path string
}

func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

func (s *TokenStore) Load() (*oauth2.Token, error) {
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

func (s *TokenStore) Save(tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Authenticator manages OAuth2 authentication with Netatmo.
type Authenticator struct {
	cfg   *oauth2.Config
	store *TokenStore
}

func NewAuthenticator(clientID, clientSecret, redirectURL, tokenFile string) *Authenticator {
	return &Authenticator{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Scopes:       []string{"read_station"},
			Endpoint:     netatmoEndpoint,
		},
		store: NewTokenStore(tokenFile),
	}
}

// GetHTTPClient returns an HTTP client with a valid token, running the OAuth2
// flow interactively if no stored token exists.
func (a *Authenticator) GetHTTPClient(ctx context.Context) (*http.Client, error) {
	tok, err := a.store.Load()
	if err == nil && tok.Valid() {
		return a.cfg.Client(ctx, tok), nil
	}

	// Token expired but we have a refresh token — let oauth2 handle refresh automatically.
	if err == nil && tok.RefreshToken != "" {
		ts := a.cfg.TokenSource(ctx, tok)
		newTok, refreshErr := ts.Token()
		if refreshErr == nil {
			_ = a.store.Save(newTok)
			return oauth2.NewClient(ctx, ts), nil
		}
		log.Printf("Token refresh failed (%v), re-authenticating", refreshErr)
	}

	// No valid token — start browser-based flow.
	tok, err = a.interactiveAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}
	if saveErr := a.store.Save(tok); saveErr != nil {
		log.Printf("Warning: could not save token: %v", saveErr)
	}
	return a.cfg.Client(ctx, tok), nil
}

func (a *Authenticator) interactiveAuth(ctx context.Context) (*oauth2.Token, error) {
	state := fmt.Sprintf("mycast-%d", time.Now().UnixNano())
	authURL := a.cfg.AuthCodeURL(state, oauth2.AccessTypeOffline)

	fmt.Println("\n=== Netatmo Authentication Required ===")
	fmt.Printf("Open this URL in your browser:\n\n  %s\n\n", authURL)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	srv := &http.Server{Addr: ":8080"}
	http.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- fmt.Errorf("no code in callback")
			return
		}
		fmt.Fprintln(w, "Authentication successful — you can close this tab.")
		codeCh <- code
	})

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)

	return a.cfg.Exchange(ctx, code)
}
