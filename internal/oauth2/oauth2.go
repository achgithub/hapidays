// Package oauth2 fetches access tokens for the grant types that matter for
// testing an API: client_credentials, password, and authorization_code.
// The authorization_code flow runs its own localhost callback listener —
// deliberately never Postman's hosted https://oauth.pstmn.io redirect —
// so hapidays has zero dependency on any third party to complete it.
//
// authorization_code is inherently two-phase: the caller needs the
// authorization URL back immediately (to open a browser tab) before
// waiting for the redirect, so it's split into Start (returns the URL,
// starts listening in the background) and Wait (blocks for the result).
package oauth2

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Params struct {
	GrantType      string // "client_credentials" | "password" | "authorization_code"
	AccessTokenURL string
	AuthURL        string // required for authorization_code
	ClientID       string
	ClientSecret   string
	Username       string // required for password grant
	Password       string
	Scope          string
}

type Result struct {
	AccessToken string `json:"accessToken"`
	TokenType   string `json:"tokenType,omitempty"`
	ExpiresIn   int    `json:"expiresIn,omitempty"`
}

// FetchToken handles the single-request grant types. For
// "authorization_code" use Manager.Start / Manager.Wait instead.
func FetchToken(ctx context.Context, p Params) (*Result, error) {
	switch p.GrantType {
	case "client_credentials":
		return exchangeForm(ctx, p.AccessTokenURL, url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {p.ClientID},
			"client_secret": {p.ClientSecret},
			"scope":         {p.Scope},
		})
	case "password":
		return exchangeForm(ctx, p.AccessTokenURL, url.Values{
			"grant_type":    {"password"},
			"client_id":     {p.ClientID},
			"client_secret": {p.ClientSecret},
			"username":      {p.Username},
			"password":      {p.Password},
			"scope":         {p.Scope},
		})
	default:
		return nil, fmt.Errorf("unsupported grant type %q (use Manager for authorization_code)", p.GrantType)
	}
}

func exchangeForm(ctx context.Context, tokenURL string, form url.Values) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("token endpoint returned non-JSON response (status %d)", resp.StatusCode)
	}
	if body.Error != "" {
		return nil, fmt.Errorf("%s: %s", body.Error, body.ErrorDesc)
	}
	if body.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned no access_token (status %d)", resp.StatusCode)
	}
	return &Result{AccessToken: body.AccessToken, TokenType: body.TokenType, ExpiresIn: body.ExpiresIn}, nil
}

// Manager tracks in-flight authorization_code exchanges between the Start
// call (which opens the local listener and returns the browser URL) and
// the Wait call (which blocks for the redirect + token exchange).
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	done   chan struct{}
	result *Result
	err    error
	server *http.Server
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*session{}}
}

// Start opens a one-shot localhost listener, builds the authorization URL
// against it as the redirect_uri, and returns (sessionID, authURL). The
// caller opens authURL in a browser, then calls Wait with sessionID.
func (m *Manager) Start(p Params) (sessionID, authURL string, err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("start local callback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	state := randomHex(16)
	authURL, err = buildAuthURL(p.AuthURL, p.ClientID, redirectURI, p.Scope, state)
	if err != nil {
		listener.Close()
		return "", "", err
	}

	sess := &session{done: make(chan struct{})}
	sessionID = randomHex(16)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		finish := func(res *Result, err error) {
			sess.result, sess.err = res, err
			close(sess.done)
		}
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			finish(nil, fmt.Errorf("state mismatch on callback — possible CSRF, aborting"))
			return
		}
		if msg := q.Get("error"); msg != "" {
			http.Error(w, msg, http.StatusOK)
			finish(nil, fmt.Errorf("authorization server returned error: %s", msg))
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			finish(nil, fmt.Errorf("callback had no code"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body style="font-family:sans-serif;padding:2rem">
			Authorization received — you can close this tab and return to hapidays.</body></html>`))

		result, exErr := exchangeForm(r.Context(), p.AccessTokenURL, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {redirectURI},
			"client_id":     {p.ClientID},
			"client_secret": {p.ClientSecret},
		})
		finish(result, exErr)
	})
	sess.server = &http.Server{Handler: mux}
	go func() { _ = sess.server.Serve(listener) }()

	m.mu.Lock()
	m.sessions[sessionID] = sess
	m.mu.Unlock()

	return sessionID, authURL, nil
}

// Wait blocks until the browser redirect (and subsequent token exchange)
// completes, ctx is cancelled, or two minutes pass.
func (m *Manager) Wait(ctx context.Context, sessionID string) (*Result, error) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown or expired oauth2 session %q", sessionID)
	}
	defer func() {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		_ = sess.server.Close()
	}()

	select {
	case <-sess.done:
		return sess.result, sess.err
	case <-time.After(2 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for browser authorization")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func buildAuthURL(authURL, clientID, redirectURI, scope, state string) (string, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", fmt.Errorf("parse authorization URL: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	if scope != "" {
		q.Set("scope", scope)
	}
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
