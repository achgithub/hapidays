// Package api wires hapidays's HTTP endpoints: CRUD over collections and
// environments, Postman import, request execution, history and settings.
// The server is meant to be bound to 127.0.0.1 only (see cmd/hapidays) —
// this package still checks Origin on state-changing/execute requests as
// defense in depth, since any page open in the same browser could
// otherwise drive it and read saved secrets.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"hapidays/internal/client"
	"hapidays/internal/model"
	"hapidays/internal/oauth2"
	"hapidays/internal/postman"
	"hapidays/internal/runner"
	"hapidays/internal/store"
)

type Server struct {
	store  *store.Store
	mux    *http.ServeMux
	oauth2 *oauth2.Manager
}

func New(st *store.Store) *Server {
	s := &Server{store: st, mux: http.NewServeMux(), oauth2: oauth2.NewManager()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/collections", s.originGuard(s.listCollections))
	s.mux.HandleFunc("POST /api/collections/import", s.originGuard(s.importCollection))
	s.mux.HandleFunc("GET /api/collections/{id}", s.originGuard(s.getCollection))
	s.mux.HandleFunc("PUT /api/collections/{id}", s.originGuard(s.saveCollection))
	s.mux.HandleFunc("DELETE /api/collections/{id}", s.originGuard(s.deleteCollection))

	s.mux.HandleFunc("GET /api/environments", s.originGuard(s.listEnvironments))
	s.mux.HandleFunc("POST /api/environments/import", s.originGuard(s.importEnvironment))
	s.mux.HandleFunc("GET /api/environments/{id}", s.originGuard(s.getEnvironment))
	s.mux.HandleFunc("PUT /api/environments/{id}", s.originGuard(s.saveEnvironment))
	s.mux.HandleFunc("DELETE /api/environments/{id}", s.originGuard(s.deleteEnvironment))

	s.mux.HandleFunc("POST /api/send", s.originGuard(s.send))
	s.mux.HandleFunc("GET /api/history", s.originGuard(s.listHistory))

	s.mux.HandleFunc("GET /api/settings", s.originGuard(s.getSettings))
	s.mux.HandleFunc("PUT /api/settings", s.originGuard(s.saveSettings))

	s.mux.HandleFunc("GET /api/cookies", s.originGuard(s.listCookies))
	s.mux.HandleFunc("PUT /api/cookies", s.originGuard(s.setCookie))
	s.mux.HandleFunc("DELETE /api/cookies", s.originGuard(s.clearCookies))
	s.mux.HandleFunc("DELETE /api/cookies/{domain}/{name}", s.originGuard(s.deleteCookie))

	s.mux.HandleFunc("POST /api/run", s.originGuard(s.runCollection))

	s.mux.HandleFunc("POST /api/oauth2/token", s.originGuard(s.oauth2Token))
	s.mux.HandleFunc("POST /api/oauth2/authorize/start", s.originGuard(s.oauth2AuthorizeStart))
	s.mux.HandleFunc("POST /api/oauth2/authorize/wait", s.originGuard(s.oauth2AuthorizeWait))
}

// originGuard rejects cross-origin browser requests (a request carrying an
// Origin header that isn't our own loopback origin). Non-browser clients
// (curl, no Origin header) are allowed through — they aren't the threat
// model here, a hostile page open in the same browser is.
//
// r.Host is checked against a fixed loopback allowlist rather than trusted
// as-is: a page on a hostile domain that DNS-resolves to 127.0.0.1 (DNS
// rebinding) would otherwise make Origin and Host agree with each other
// while both are the attacker's hostname, sailing straight through a
// same-origin check that only compares them to one another.
func (s *Server) originGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "host not recognized as loopback", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "[::1]", "localhost":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ---- collections ----

func (s *Server) listCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.store.ListCollections()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, cols)
}

func (s *Server) importCollection(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	col, err := postman.ImportCollection(data, store.NewID)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	col.UpdatedAt = time.Now()
	if err := s.store.SaveCollection(col); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, col)
}

func (s *Server) getCollection(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.LoadCollection(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, col)
}

func (s *Server) saveCollection(w http.ResponseWriter, r *http.Request) {
	var col model.Collection
	if err := json.NewDecoder(r.Body).Decode(&col); err != nil {
		writeErr(w, 400, err)
		return
	}
	col.ID = r.PathValue("id")
	col.UpdatedAt = time.Now()
	if err := s.store.SaveCollection(&col); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, col)
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteCollection(r.PathValue("id")); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

// ---- environments ----

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.store.ListEnvironments()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, envs)
}

func (s *Server) importEnvironment(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	env, err := postman.ImportEnvironment(data, store.NewID)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	env.UpdatedAt = time.Now()
	if err := s.store.SaveEnvironment(env); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, env)
}

func (s *Server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	env, err := s.store.LoadEnvironment(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, env)
}

func (s *Server) saveEnvironment(w http.ResponseWriter, r *http.Request) {
	var env model.Environment
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeErr(w, 400, err)
		return
	}
	env.ID = r.PathValue("id")
	env.UpdatedAt = time.Now()
	if err := s.store.SaveEnvironment(&env); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, env)
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteEnvironment(r.PathValue("id")); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

// ---- send ----

type sendRequest struct {
	Request            model.RequestSpec `json:"request"`
	CollectionID       string            `json:"collectionId,omitempty"`
	EnvironmentID      string            `json:"environmentId,omitempty"`
	InsecureSkipVerify *bool             `json:"insecureSkipVerify,omitempty"`
	// ExtraVars are merged on top of the collection+environment vars,
	// taking precedence — the same "one iteration-data row" override
	// runner.Run applies internally via mergeVars, exposed here so the
	// step-through runner (built on this endpoint, not /api/run) can
	// replay a single row without its own var-resolution path.
	ExtraVars map[string]string `json:"extraVars,omitempty"`
}

func (s *Server) resolveVars(collectionID, environmentID string) map[string]string {
	vars := map[string]string{}
	if collectionID != "" {
		if col, err := s.store.LoadCollection(collectionID); err == nil {
			for _, kv := range col.Variables {
				if !kv.Disabled {
					vars[kv.Key] = kv.Value
				}
			}
		}
	}
	if environmentID != "" {
		if env, err := s.store.LoadEnvironment(environmentID); err == nil {
			for _, kv := range env.Values {
				if !kv.Disabled {
					vars[kv.Key] = kv.Value
				}
			}
		}
	}
	return vars
}

// collectionAuth loads a collection's auth for AuthInherit resolution.
// Returns the zero Auth (type "") if collectionID is empty or the
// collection can't be loaded, which applyAuth treats as a no-op — the
// same as AuthNone.
func (s *Server) collectionAuth(collectionID string) model.Auth {
	if collectionID == "" {
		return model.Auth{}
	}
	col, err := s.store.LoadCollection(collectionID)
	if err != nil {
		return model.Auth{}
	}
	return col.Auth
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}

	settings, err := s.store.LoadSettings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	vars := s.resolveVars(req.CollectionID, req.EnvironmentID)
	for k, v := range req.ExtraVars {
		vars[k] = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 65*time.Second)
	defer cancel()

	result, err := client.Execute(ctx, req.Request, vars, client.Options{
		Settings:           settings,
		InsecureSkipVerify: req.InsecureSkipVerify,
		Cookies:            s.store,
		CollectionAuth:     s.collectionAuth(req.CollectionID),
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	if len(result.Captured) > 0 && req.EnvironmentID != "" {
		if env, err := s.store.LoadEnvironment(req.EnvironmentID); err == nil {
			for k, v := range result.Captured {
				updated := false
				for i, kv := range env.Values {
					if kv.Key == k {
						env.Values[i].Value = v
						updated = true
						break
					}
				}
				if !updated {
					env.Values = append(env.Values, model.KV{Key: k, Value: v})
				}
			}
			_ = s.store.SaveEnvironment(env)
		}
	}

	_ = s.store.AppendHistory(model.HistoryEntry{
		ID:            store.NewID(),
		Timestamp:     time.Now(),
		Method:        req.Request.Method,
		URL:           client.Resolve(req.Request.URLRaw, vars),
		Status:        result.Status,
		DurationMS:    result.DurationMS,
		SizeBytes:     result.SizeBytes,
		Request:       req.Request,
		CollectionID:  req.CollectionID,
		EnvironmentID: req.EnvironmentID,
	})

	writeJSON(w, 200, result)
}

func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListHistory()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, entries)
}

// ---- settings ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, settings)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var settings store.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.SaveSettings(settings); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, settings)
}

// ---- cookies ----

func (s *Server) listCookies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.store.ListCookies())
}

// setCookie is the manual "add a cookie" path — e.g. seeding a session
// cookie value you obtained some other way, rather than only ever
// accumulating cookies as a side effect of sending requests.
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request) {
	var rec model.CookieRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeErr(w, 400, err)
		return
	}
	if rec.Domain == "" || rec.Name == "" {
		writeErr(w, 400, fmt.Errorf("domain and name are required"))
		return
	}
	if err := s.store.SetCookie(rec); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) clearCookies(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearCookies(); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) deleteCookie(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteCookie(r.PathValue("domain"), r.PathValue("name")); err != nil {
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

// ---- collection runner ----

type runRequest struct {
	CollectionID       string              `json:"collectionId"`
	EnvironmentID      string              `json:"environmentId,omitempty"`
	FolderID           string              `json:"folderId,omitempty"` // scope to one folder; empty = whole collection
	DataRows           []map[string]string `json:"dataRows,omitempty"`
	DelayMS            int                 `json:"delayMs,omitempty"`
	InsecureSkipVerify *bool               `json:"insecureSkipVerify,omitempty"`
}

func (s *Server) runCollection(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	col, err := s.store.LoadCollection(req.CollectionID)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	nodes := col.Root
	if req.FolderID != "" {
		folder := runner.FindNode(col.Root, req.FolderID)
		if folder == nil {
			writeErr(w, 404, fmt.Errorf("folder %q not found in collection", req.FolderID))
			return
		}
		nodes = folder.Children
	}

	settings, err := s.store.LoadSettings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	vars := s.resolveVars(req.CollectionID, req.EnvironmentID)

	// No fixed request-count cap here (a run is a bounded loop over the
	// caller's own collection/data), but do bound total wall-clock time so
	// a stuck endpoint can't hang the run forever.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	results := runner.Run(ctx, nodes, runner.Options{
		Vars:     vars,
		DataRows: req.DataRows,
		DelayMS:  req.DelayMS,
		Client: client.Options{
			Settings:           settings,
			InsecureSkipVerify: req.InsecureSkipVerify,
			Cookies:            s.store,
			CollectionAuth:     col.Auth,
		},
	})
	writeJSON(w, 200, results)
}

// ---- oauth2 ----

type oauth2TokenRequest struct {
	GrantType      string `json:"grantType"`
	AccessTokenURL string `json:"accessTokenUrl"`
	ClientID       string `json:"clientId"`
	ClientSecret   string `json:"clientSecret"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	Scope          string `json:"scope,omitempty"`
}

func (s *Server) oauth2Token(w http.ResponseWriter, r *http.Request) {
	var req oauth2TokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	result, err := oauth2.FetchToken(r.Context(), oauth2.Params{
		GrantType:      req.GrantType,
		AccessTokenURL: req.AccessTokenURL,
		ClientID:       req.ClientID,
		ClientSecret:   req.ClientSecret,
		Username:       req.Username,
		Password:       req.Password,
		Scope:          req.Scope,
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, result)
}

type oauth2AuthorizeStartRequest struct {
	AuthURL        string `json:"authUrl"`
	AccessTokenURL string `json:"accessTokenUrl"`
	ClientID       string `json:"clientId"`
	ClientSecret   string `json:"clientSecret"`
	Scope          string `json:"scope,omitempty"`
}

func (s *Server) oauth2AuthorizeStart(w http.ResponseWriter, r *http.Request) {
	var req oauth2AuthorizeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	sessionID, authURL, err := s.oauth2.Start(oauth2.Params{
		GrantType:      "authorization_code",
		AuthURL:        req.AuthURL,
		AccessTokenURL: req.AccessTokenURL,
		ClientID:       req.ClientID,
		ClientSecret:   req.ClientSecret,
		Scope:          req.Scope,
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"sessionId": sessionID, "authUrl": authURL})
}

func (s *Server) oauth2AuthorizeWait(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	result, err := s.oauth2.Wait(r.Context(), req.SessionID)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, result)
}
