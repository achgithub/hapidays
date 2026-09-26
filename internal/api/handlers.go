// Package api wires hapidays's HTTP endpoints: CRUD over collections and
// environments, Postman import, request execution, history and settings.
// The server is meant to be bound to 127.0.0.1 only (see cmd/hapidays) —
// this package still checks Origin on state-changing/execute requests as
// defense in depth, since any page open in the same browser could
// otherwise drive it and read saved secrets.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hapidays/internal/client"
	"hapidays/internal/curlconv"
	"hapidays/internal/graphqlintro"
	"hapidays/internal/grpcintro"
	"hapidays/internal/importer"
	"hapidays/internal/model"
	"hapidays/internal/oauth2"
	"hapidays/internal/odata"
	"hapidays/internal/odatabatch"
	"hapidays/internal/openapi"
	"hapidays/internal/runner"
	"hapidays/internal/store"
	"hapidays/internal/wsdl"
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
	s.mux.HandleFunc("POST /api/collections/import-url", s.originGuard(s.importCollectionURL))
	s.mux.HandleFunc("POST /api/wsdl/import", s.originGuard(s.importWSDL))
	s.mux.HandleFunc("POST /api/odata/import", s.originGuard(s.importOData))
	s.mux.HandleFunc("POST /api/graphql/import", s.originGuard(s.importGraphQL))
	s.mux.HandleFunc("POST /api/openapi/import", s.originGuard(s.importOpenAPI))
	s.mux.HandleFunc("POST /api/grpc/import", s.originGuard(s.importGRPC))
	s.mux.HandleFunc("GET /api/collections/{id}/export/postman", s.originGuard(s.exportCollectionPostman))
	s.mux.HandleFunc("GET /api/collections/{id}", s.originGuard(s.getCollection))
	s.mux.HandleFunc("PUT /api/collections/{id}", s.originGuard(s.saveCollection))
	s.mux.HandleFunc("DELETE /api/collections/{id}", s.originGuard(s.deleteCollection))

	s.mux.HandleFunc("GET /api/environments", s.originGuard(s.listEnvironments))
	s.mux.HandleFunc("POST /api/environments/import", s.originGuard(s.importEnvironment))
	s.mux.HandleFunc("POST /api/environments/import-url", s.originGuard(s.importEnvironmentURL))
	s.mux.HandleFunc("GET /api/environments/{id}", s.originGuard(s.getEnvironment))
	s.mux.HandleFunc("PUT /api/environments/{id}", s.originGuard(s.saveEnvironment))
	s.mux.HandleFunc("DELETE /api/environments/{id}", s.originGuard(s.deleteEnvironment))

	s.mux.HandleFunc("POST /api/send", s.originGuard(s.send))
	s.mux.HandleFunc("POST /api/curl/import", s.originGuard(s.curlImport))
	s.mux.HandleFunc("POST /api/curl/export", s.originGuard(s.curlExport))
	s.mux.HandleFunc("GET /api/history", s.originGuard(s.listHistory))

	s.mux.HandleFunc("GET /api/settings", s.originGuard(s.getSettings))
	s.mux.HandleFunc("PUT /api/settings", s.originGuard(s.saveSettings))

	s.mux.HandleFunc("GET /api/cookies", s.originGuard(s.listCookies))
	s.mux.HandleFunc("PUT /api/cookies", s.originGuard(s.setCookie))
	s.mux.HandleFunc("DELETE /api/cookies", s.originGuard(s.clearCookies))
	s.mux.HandleFunc("DELETE /api/cookies/{domain}/{name}", s.originGuard(s.deleteCookie))

	s.mux.HandleFunc("POST /api/run", s.originGuard(s.runCollection))
	s.mux.HandleFunc("POST /api/odata/batch", s.originGuard(s.odataBatch))

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
	col, err := importer.ImportCollection(data, store.NewID)
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

// importCollectionURL fetches a hapidays or Postman collection file from a
// public URL (e.g. a GitHub raw link to a shared example) and imports it
// exactly like a local file drop would — same reasoning as importWSDL's
// url mode: a browser can't cross-origin-fetch most hosts, so this has to
// happen server-side.
func (s *Server) importCollectionURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	data, err := fetchRawFile(r.Context(), req.URL)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	col, err := importer.ImportCollection(data, store.NewID)
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

// fetchRawFile GETs an arbitrary file over http(s) — used by the "import
// from URL" flows (a collection or environment shared as a raw file, e.g.
// on GitHub). Same redirect/scheme restrictions as fetchWSDL.
func fetchRawFile(ctx context.Context, rawURL string) ([]byte, error) {
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-http(s) scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("only http/https URLs are supported")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching %s: server returned HTTP %d", rawURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// importWSDL accepts either { "raw": "<wsdl xml>" } (uploaded file content,
// read client-side and posted as text) or { "url": "https://..." } (fetched
// here — a browser can't cross-origin-fetch a ?WSDL URL, so this has to be
// server-side, same exposure class as /api/send which already lets a user
// point this tool at any URL by design).
func (s *Server) importWSDL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Raw string `json:"raw"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}

	data := []byte(req.Raw)
	if req.URL != "" {
		fetched, err := fetchWSDL(r.Context(), req.URL)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		data = fetched
	}
	if len(data) == 0 {
		writeErr(w, 400, fmt.Errorf("no WSDL content provided (neither raw text nor a fetchable url)"))
		return
	}

	col, err := wsdl.Import(data, req.URL, store.NewID)
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

func fetchWSDL(ctx context.Context, rawURL string) ([]byte, error) {
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-http(s) scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("only http/https URLs are supported")
	}
	// Some WSDL hosts (IIS/legacy ASP.NET services, and the WAFs in front
	// of them) reset the connection on Go's default "Go-http-client/1.1"
	// User-Agent — hit this exact wall testing against dneonline's own
	// calculator WSDL. A browser-shaped UA avoids it and is otherwise
	// harmless.
	req.Header.Set("User-Agent", "Mozilla/5.0 hapidays-wsdl-import")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch WSDL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching WSDL: server returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// importOData accepts a $metadata URL plus an (optional) auth spec and
// returns a generated collection. Unlike importWSDL's fetchWSDL (a bare,
// unauthenticated GET), this goes through client.Execute — SAP Gateway/CPI-
// style services, the primary real-world target here, commonly gate
// $metadata behind the same auth as the data endpoints, so a bare fetch
// would just 401. Going through Execute also means mTLS, the configured
// proxy, and an insecure-TLS override all apply exactly as they would to
// any other request from this app.
func (s *Server) importOData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string     `json:"url"`
		Auth model.Auth `json:"auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.URL == "" {
		writeErr(w, 400, fmt.Errorf("a $metadata URL is required"))
		return
	}

	settings, err := s.store.LoadSettings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	metaSpec := model.RequestSpec{
		Method:  "GET",
		URLRaw:  req.URL,
		Auth:    req.Auth,
		Body:    model.Body{Mode: model.BodyNone},
		Headers: []model.KV{{Key: "Accept", Value: "application/xml"}},
	}
	result, err := client.Execute(ctx, metaSpec, map[string]string{}, client.Options{Settings: settings})
	if err != nil {
		writeErr(w, 500, fmt.Errorf("fetch $metadata: %w", err))
		return
	}
	if result.Error != "" {
		writeErr(w, 400, fmt.Errorf("fetch $metadata: %s", result.Error))
		return
	}
	if result.Status != 200 {
		writeErr(w, 400, fmt.Errorf("fetching $metadata: server returned HTTP %d — check the URL and auth", result.Status))
		return
	}

	serviceRoot := strings.TrimSuffix(req.URL, "$metadata")
	col, err := odata.Import([]byte(result.Body), serviceRoot, req.Auth, store.NewID)
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

// importOpenAPI accepts either { "raw": "<spec JSON/YAML>" } (an uploaded
// file, read client-side and posted as text) or { "url": "...", "auth":
// {...} } (fetched here via client.Execute — same reasoning as
// importOData: some gateways gate the spec document itself behind the same
// auth as the API it describes). Unlike every other importer, this can
// produce more than one Environment (one per server the spec declares)
// alongside the collection — see internal/openapi's doc comment for why
// credentials always land there, never in the collection itself.
func (s *Server) importOpenAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Raw  string     `json:"raw"`
		URL  string     `json:"url"`
		Auth model.Auth `json:"auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}

	data := []byte(req.Raw)
	if req.URL != "" {
		settings, err := s.store.LoadSettings()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		specSpec := model.RequestSpec{
			Method:  "GET",
			URLRaw:  req.URL,
			Auth:    req.Auth,
			Body:    model.Body{Mode: model.BodyNone},
			Headers: []model.KV{{Key: "Accept", Value: "application/json, application/yaml, text/yaml, */*"}},
		}
		result, err := client.Execute(ctx, specSpec, map[string]string{}, client.Options{Settings: settings})
		if err != nil {
			writeErr(w, 500, fmt.Errorf("fetch OpenAPI document: %w", err))
			return
		}
		if result.Error != "" {
			writeErr(w, 400, fmt.Errorf("fetch OpenAPI document: %s", result.Error))
			return
		}
		if result.Status != 200 {
			writeErr(w, 400, fmt.Errorf("fetching OpenAPI document: server returned HTTP %d — check the URL and auth", result.Status))
			return
		}
		data = []byte(result.Body)
	}
	if len(data) == 0 {
		writeErr(w, 400, fmt.Errorf("no OpenAPI/Swagger document provided (neither raw text nor a fetchable url)"))
		return
	}

	col, envs, err := openapi.Import(data, req.URL, store.NewID)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	col.UpdatedAt = time.Now()
	if err := s.store.SaveCollection(col); err != nil {
		writeErr(w, 500, err)
		return
	}
	for _, env := range envs {
		env.UpdatedAt = time.Now()
		if err := s.store.SaveEnvironment(env); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"collection": col, "environments": envs})
}

// importGraphQL POSTs the standard GraphQL introspection query to the
// given endpoint (through client.Execute, same reasoning as importOData —
// enterprise GraphQL endpoints commonly require the same auth as any other
// query) and generates a collection from the schema it returns.
func (s *Server) importGraphQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string     `json:"url"`
		Auth model.Auth `json:"auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.URL == "" {
		writeErr(w, 400, fmt.Errorf("a GraphQL endpoint URL is required"))
		return
	}

	settings, err := s.store.LoadSettings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	payload, _ := json.Marshal(map[string]string{"query": graphqlintro.IntrospectionQuery})
	introspectSpec := model.RequestSpec{
		Method: "POST",
		URLRaw: req.URL,
		Auth:   req.Auth,
		Body:   model.Body{Mode: model.BodyRaw, Raw: string(payload), RawLanguage: "json"},
	}
	result, err := client.Execute(ctx, introspectSpec, map[string]string{}, client.Options{Settings: settings})
	if err != nil {
		writeErr(w, 500, fmt.Errorf("introspection query failed: %w", err))
		return
	}
	if result.Error != "" {
		writeErr(w, 400, fmt.Errorf("introspection query failed: %s", result.Error))
		return
	}
	if result.Status != 200 {
		writeErr(w, 400, fmt.Errorf("introspection query: server returned HTTP %d — check the URL and auth", result.Status))
		return
	}

	col, err := graphqlintro.Import([]byte(result.Body), req.URL, req.Auth, store.NewID)
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

// importGRPC connects to a gRPC server's reflection service and builds a
// collection with one request per unary method (streaming methods are
// listed as skipped rather than silently turned into a broken request).
func (s *Server) importGRPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target    string `json:"target"`
		Plaintext bool   `json:"plaintext"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Target == "" {
		writeErr(w, 400, fmt.Errorf("a gRPC target (host:port) is required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	res, err := grpcintro.Import(ctx, req.Target, req.Plaintext, store.NewID)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	res.Collection.UpdatedAt = time.Now()
	if err := s.store.SaveCollection(res.Collection); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"collection": res.Collection,
		"skipped":    res.Skipped,
	})
}

func (s *Server) getCollection(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.LoadCollection(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, col)
}

// exportCollectionPostman is the one-way counterpart to importCollection's
// Postman-recognizing path — renders col in Postman Collection Format v2.1
// (see internal/importer/export.go) instead of hapidays's own native
// export shape, so it can be opened directly in Postman.
func (s *Server) exportCollectionPostman(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.LoadCollection(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	data, err := importer.ExportCollection(col)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(data)
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
	env, err := importer.ImportEnvironment(data, store.NewID)
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

// importEnvironmentURL is importEnvironment's URL-fetch counterpart — see
// importCollectionURL.
func (s *Server) importEnvironmentURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	data, err := fetchRawFile(r.Context(), req.URL)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	env, err := importer.ImportEnvironment(data, store.NewID)
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

// settingsForEnvironment overrides the global mTLS client cert with the
// environment's own, when it has one — dev/test/prod commonly need
// different client identities. An environment with no cert configured
// leaves settings untouched (falls back to the global one), not "no cert".
func (s *Server) settingsForEnvironment(settings store.Settings, environmentID string) store.Settings {
	if environmentID == "" {
		return settings
	}
	env, err := s.store.LoadEnvironment(environmentID)
	if err != nil || env.ClientCertFile == "" || env.ClientKeyFile == "" {
		return settings
	}
	settings.ClientCertFile = env.ClientCertFile
	settings.ClientKeyFile = env.ClientKeyFile
	return settings
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

// collectionHeaders loads a collection's default headers, mirroring
// collectionAuth. Returns nil if collectionID is empty or the collection
// can't be loaded.
func (s *Server) collectionHeaders(collectionID string) []model.KV {
	if collectionID == "" {
		return nil
	}
	col, err := s.store.LoadCollection(collectionID)
	if err != nil {
		return nil
	}
	return col.Headers
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
	settings = s.settingsForEnvironment(settings, req.EnvironmentID)

	vars := s.resolveVars(req.CollectionID, req.EnvironmentID)
	for k, v := range req.ExtraVars {
		vars[k] = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 65*time.Second)
	defer cancel()

	sendOpts := client.Options{
		Settings:           settings,
		InsecureSkipVerify: req.InsecureSkipVerify,
		Cookies:            s.store,
		CollectionAuth:     s.collectionAuth(req.CollectionID),
		CollectionHeaders:  s.collectionHeaders(req.CollectionID),
	}
	var result *client.Result
	if req.Request.Body.Mode == model.BodyGRPC {
		result, err = client.ExecuteGRPC(ctx, req.Request, vars, sendOpts)
	} else {
		result, err = client.Execute(ctx, req.Request, vars, sendOpts)
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	s.saveCaptured(req.EnvironmentID, result.Captured)

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

func (s *Server) curlImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Curl string `json:"curl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	spec, err := curlconv.Parse(req.Curl)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, spec)
}

func (s *Server) curlExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Request       model.RequestSpec `json:"request"`
		CollectionID  string            `json:"collectionId"`
		EnvironmentID string            `json:"environmentId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	vars := s.resolveVars(req.CollectionID, req.EnvironmentID)
	curl := curlconv.Export(req.Request, vars, s.collectionAuth(req.CollectionID), s.collectionHeaders(req.CollectionID))
	writeJSON(w, 200, map[string]string{"curl": curl})
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
	settings = s.settingsForEnvironment(settings, req.EnvironmentID)
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
		OnCapture: func(captured map[string]string) { s.saveCaptured(req.EnvironmentID, captured) },
		Client: client.Options{
			Settings:           settings,
			InsecureSkipVerify: req.InsecureSkipVerify,
			Cookies:            s.store,
			CollectionAuth:     col.Auth,
			CollectionHeaders:  col.Headers,
		},
	})
	writeJSON(w, 200, results)
}

// saveCaptured writes captured values into the environment (updating
// existing keys, appending new ones). No environment selected = nowhere to
// save, so it's a no-op.
func (s *Server) saveCaptured(environmentID string, captured map[string]string) {
	if len(captured) == 0 || environmentID == "" {
		return
	}
	env, err := s.store.LoadEnvironment(environmentID)
	if err != nil {
		return
	}
	for k, v := range captured {
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

// ---- OData $batch ----

type batchRequest struct {
	CollectionID  string `json:"collectionId"`
	FolderID      string `json:"folderId,omitempty"` // empty = every request in the collection
	EnvironmentID string `json:"environmentId,omitempty"`
	BatchURL      string `json:"batchUrl"` // e.g. {{baseUrl}}/$batch, resolved before use
}

type batchStepResult struct {
	NodeID  string              `json:"nodeId"`
	Name    string              `json:"name"`
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body"`
	Error   string              `json:"error,omitempty"`
}

// odataBatch bundles every request under a folder (or the whole
// collection) into one OData $batch call and returns the individual
// sub-responses. Building the sub-requests goes through client.PrepareRequest
// — the same resolution (vars, auth, body) a normal /api/send uses — so a
// batched request behaves exactly like it would sent on its own, just
// bundled onto the wire as one HTTP call.
func (s *Server) odataBatch(w http.ResponseWriter, r *http.Request) {
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.BatchURL == "" {
		writeErr(w, 400, fmt.Errorf("a $batch URL is required"))
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
	leaves := runner.Flatten(nodes)
	if len(leaves) == 0 {
		writeErr(w, 400, fmt.Errorf("no requests found to batch"))
		return
	}

	settings, err := s.store.LoadSettings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	settings = s.settingsForEnvironment(settings, req.EnvironmentID)
	vars := s.resolveVars(req.CollectionID, req.EnvironmentID)

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	clientOpts := client.Options{Settings: settings, Cookies: s.store, CollectionAuth: col.Auth, CollectionHeaders: col.Headers}
	httpReqs := make([]*http.Request, 0, len(leaves))
	for _, leaf := range leaves {
		hr, err := client.PrepareRequest(ctx, *leaf.Request, vars, clientOpts)
		if err != nil {
			writeErr(w, 400, fmt.Errorf("preparing %q: %w", leaf.Name, err))
			return
		}
		httpReqs = append(httpReqs, hr)
	}

	batchURL := client.Resolve(req.BatchURL, vars)
	parsedBatchURL, err := url.Parse(batchURL)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("invalid $batch URL: %w", err))
		return
	}
	servicePath := strings.TrimSuffix(parsedBatchURL.Path, "/$batch")

	body, contentType, err := odatabatch.Build(httpReqs, servicePath)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	httpClient, err := client.NewHTTPClient(clientOpts)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	outerReq, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL, bytes.NewReader(body))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	outerReq.Header.Set("Content-Type", contentType)
	applyAuthForBatch(outerReq, col.Auth, vars)

	resp, err := httpClient.Do(outerReq)
	if err != nil {
		writeErr(w, 502, fmt.Errorf("$batch request failed: %w", err))
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if resp.StatusCode >= 300 {
		writeErr(w, 502, fmt.Errorf("$batch endpoint returned HTTP %d: %s", resp.StatusCode, string(respBody)))
		return
	}

	results, err := odatabatch.Parse(resp.Header.Get("Content-Type"), respBody)
	if err != nil {
		writeErr(w, 502, fmt.Errorf("parsing $batch response: %w", err))
		return
	}

	out := make([]batchStepResult, len(leaves))
	for i, leaf := range leaves {
		out[i] = batchStepResult{NodeID: leaf.ID, Name: leaf.Name, Method: leaf.Request.Method, URL: client.Resolve(leaf.Request.URLRaw, vars)}
		if i < len(results) {
			out[i].Status = results[i].Status
			out[i].Headers = results[i].Headers
			out[i].Body = results[i].Body
		} else {
			out[i].Error = "no matching sub-response returned by the server"
		}
	}
	writeJSON(w, 200, out)
}

// applyAuthForBatch puts the collection's own auth on the outer $batch
// HTTP call — the $batch endpoint itself commonly requires the same auth
// as the data endpoints, same reasoning as importOData fetching $metadata
// through client.Execute rather than a bare fetch. Only the common,
// directly-representable-as-a-header cases are handled (Basic/Bearer/API
// key); anything else (digest, AWS SigV4, OAuth2) would need a full
// request/response round trip of its own to apply correctly, which a
// single outer POST doesn't provide room for.
func applyAuthForBatch(req *http.Request, auth model.Auth, vars map[string]string) {
	switch auth.Type {
	case model.AuthBasic:
		req.SetBasicAuth(client.Resolve(auth.Params["username"], vars), client.Resolve(auth.Params["password"], vars))
	case model.AuthBearer:
		req.Header.Set("Authorization", "Bearer "+client.Resolve(auth.Params["token"], vars))
	case model.AuthAPIKey:
		if auth.Params["in"] == "query" {
			q := req.URL.Query()
			q.Set(client.Resolve(auth.Params["key"], vars), client.Resolve(auth.Params["value"], vars))
			req.URL.RawQuery = q.Encode()
		} else {
			req.Header.Set(client.Resolve(auth.Params["key"], vars), client.Resolve(auth.Params["value"], vars))
		}
	}
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
	RefreshToken   string `json:"refreshToken,omitempty"`
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
		RefreshToken:   req.RefreshToken,
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
