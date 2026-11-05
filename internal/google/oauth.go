// Package google handles the profile's Google OAuth login and builds the API
// clients on top of the saved token.
package google

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/tasks/v1"

	"github.com/vitalbh/elan/internal/config"
)

// Scopes are requested together in one login, so adding an integration later
// does not need a second consent.
var Scopes = []string{
	drive.DriveReadonlyScope,
	calendar.CalendarScope,
	tasks.TasksScope,
}

var (
	// ErrNoCredentials means the profile has no OAuth client file.
	ErrNoCredentials = errors.New("credencial OAuth ausente")
	// ErrNotLoggedIn means the profile has no token yet.
	ErrNotLoggedIn = errors.New("sem login no Google: rode `elan google-login`")
)

func oauthConfig(cfg config.Google) (*oauth2.Config, error) {
	b, err := os.ReadFile(cfg.CredentialsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: salve o JSON do client Desktop em %s", ErrNoCredentials, cfg.CredentialsFile)
	}
	if err != nil {
		return nil, err
	}
	oc, err := googleoauth.ConfigFromJSON(b, Scopes...)
	if err != nil {
		return nil, fmt.Errorf("credencial OAuth inválida (precisa ser client Desktop): %w", err)
	}
	return oc, nil
}

// Login runs the loopback OAuth flow with PKCE: it opens the consent page
// through openURL, waits for the redirect on 127.0.0.1 and saves the token.
func Login(ctx context.Context, cfg config.Google, openURL func(string) error) error {
	oc, err := oauthConfig(cfg)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	oc.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	state, err := randomString()
	if err != nil {
		return err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := oc.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce, // always returns a refresh token
		oauth2.S256ChallengeOption(verifier),
	)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(r result) { once.Do(func() { done <- r }) }

	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			switch {
			case q.Get("state") != state:
				http.Error(w, "state inválido", http.StatusBadRequest)
				finish(result{err: errors.New("state inválido no retorno do Google")})
			case q.Get("error") != "":
				fmt.Fprintf(w, "Login cancelado: %s. Pode fechar esta aba.", html.EscapeString(q.Get("error")))
				finish(result{err: fmt.Errorf("Google recusou o login: %s", q.Get("error"))})
			default:
				fmt.Fprint(w, "Login concluído. Pode fechar esta aba.")
				finish(result{code: q.Get("code")})
			}
		}),
	}
	go srv.Serve(ln) //nolint:errcheck // closed below
	defer srv.Close()

	if err := openURL(authURL); err != nil {
		return fmt.Errorf("abrir o navegador: %w", err)
	}

	var r result
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r = <-done:
	}
	if r.err != nil {
		return r.err
	}
	tok, err := oc.Exchange(ctx, r.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("trocar o código pelo token: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("o Google não devolveu refresh token")
	}
	return saveToken(cfg.TokenFile, tok)
}

// HTTPClient returns a client authorized with the profile's token. Refreshed
// tokens are written back to the token file.
func HTTPClient(ctx context.Context, cfg config.Google) (*http.Client, error) {
	oc, err := oauthConfig(cfg)
	if err != nil {
		return nil, err
	}
	tok, err := loadToken(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	ts := &savingTokenSource{
		base: oc.TokenSource(ctx, tok),
		path: cfg.TokenFile,
		last: tok.AccessToken,
	}
	return oauth2.NewClient(ctx, oauth2.ReuseTokenSource(tok, ts)), nil
}

type savingTokenSource struct {
	base oauth2.TokenSource
	path string

	mu   sync.Mutex
	last string
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return nil, fmt.Errorf("token do Google revogado ou expirado: rode `elan google-login`: %w", err)
		}
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.AccessToken != s.last {
		if err := saveToken(s.path, tok); err != nil {
			return nil, err
		}
		s.last = tok.AccessToken
	}
	return tok, nil
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("token do Google ilegível em %s: %w", path, err)
	}
	return &tok, nil
}

// saveToken writes the token atomically with owner-only permissions.
func saveToken(path string, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func randomString() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
