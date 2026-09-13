// Package client resolves variables into a request and executes it over
// plain HTTP or TLS, honoring corporate proxies, extra trust roots, and
// mutual TLS client certs — the things enterprise networks actually need.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pmclone/internal/model"
	"pmclone/internal/store"
)

type Result struct {
	Status       int                 `json:"status"`
	StatusText   string              `json:"statusText"`
	Headers      map[string][]string `json:"headers"`
	Body         string              `json:"body"` // best-effort UTF-8; binary bodies are base64 (see BodyIsBase64)
	BodyIsBase64 bool                `json:"bodyIsBase64"`
	DurationMS   int64               `json:"durationMs"`
	SizeBytes    int64               `json:"sizeBytes"`
	Captured     map[string]string   `json:"captured,omitempty"`
	Error        string              `json:"error,omitempty"`
	// ResolvedURL is URLRaw with {{vars}} (and $guid/$timestamp/etc.)
	// substituted — the only faithful way to show "what actually got sent"
	// for callers (e.g. the step-through runner) that want to display it
	// without re-implementing Resolve's dynamic-variable handling.
	ResolvedURL string `json:"resolvedUrl,omitempty"`
}

var varPattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// Resolve substitutes {{var}} tokens using vars, falling back to a small
// set of Postman-compatible dynamic variables ($guid, $timestamp,
// $isoTimestamp, $randomInt). Unresolved tokens are left as-is so the user
// can see what's missing instead of silently sending "{{}}".
func Resolve(s string, vars map[string]string) string {
	return varPattern.ReplaceAllStringFunc(s, func(tok string) string {
		name := strings.TrimSpace(tok[2 : len(tok)-2])
		if v, ok := vars[name]; ok {
			return v
		}
		if dv, ok := dynamicVar(name); ok {
			return dv
		}
		return tok
	})
}

func dynamicVar(name string) (string, bool) {
	switch name {
	case "$guid":
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", rand.Uint32(), rand.Uint32()&0xffff, rand.Uint32()&0xffff, rand.Uint32()&0xffff, rand.Uint64()&0xffffffffffff), true
	case "$timestamp":
		return strconv.FormatInt(time.Now().Unix(), 10), true
	case "$isoTimestamp":
		return time.Now().UTC().Format(time.RFC3339), true
	case "$randomInt":
		return strconv.Itoa(rand.Intn(1000)), true
	}
	return "", false
}

// CookieJar is the persistence hook Execute uses to attach stored cookies
// to outgoing requests and save ones a response sets. Backed by
// internal/store in production; kept as an interface so client stays
// independent of store's file-locking details.
type CookieJar interface {
	CookiesForHost(host string) []*http.Cookie
	StoreCookies(host string, cookies []*http.Cookie)
}

type Options struct {
	Settings           store.Settings
	InsecureSkipVerify *bool // per-request override of Settings.InsecureSkipVerify
	Timeout            time.Duration
	Cookies            CookieJar // nil disables cookie persistence
	// CollectionAuth is substituted in when a request's own Auth.Type is
	// AuthInherit — the collection (there's no per-folder auth in this
	// model) is the only thing a request can inherit from.
	CollectionAuth model.Auth
}

func buildHTTPClient(opts Options) (*http.Client, error) {
	tlsCfg := &tls.Config{}

	skipVerify := opts.Settings.InsecureSkipVerify
	if opts.InsecureSkipVerify != nil {
		skipVerify = *opts.InsecureSkipVerify
	}
	tlsCfg.InsecureSkipVerify = skipVerify

	if opts.Settings.ExtraCAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(opts.Settings.ExtraCAFile)
		if err != nil {
			return nil, fmt.Errorf("read extra CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("extra CA file contains no usable certificates")
		}
		tlsCfg.RootCAs = pool
	}

	if opts.Settings.ClientCertFile != "" && opts.Settings.ClientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.Settings.ClientCertFile, opts.Settings.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	transport := &http.Transport{
		TLSClientConfig: tlsCfg,
		Proxy:           http.ProxyFromEnvironment,
	}
	if opts.Settings.ProxyURL != "" {
		proxyURL, err := url.Parse(opts.Settings.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}}, nil
}

// Execute resolves variables into spec, sends the request, and applies any
// Capture rules against the response. It never runs Postman pre-
// request/test scripts (spec.PreRequestScript / TestScript) — those are
// display-only; see internal/postman for why.
//
// Digest auth (RFC 7616) needs two round trips — an initial request to
// receive the WWW-Authenticate challenge, then the real, signed one — so
// it's handled here rather than in a single applyAuth call.
func Execute(ctx context.Context, spec model.RequestSpec, vars map[string]string, opts Options) (*Result, error) {
	if spec.Auth.Type == model.AuthInherit {
		spec.Auth = opts.CollectionAuth
	}

	rawURL := Resolve(spec.URLRaw, vars)
	rawURL = applyQueryParams(rawURL, spec.Query, vars)

	bodyBytes, contentType, err := buildBody(spec.Body, vars)
	if err != nil {
		return nil, err
	}

	req, err := buildRequest(ctx, spec, rawURL, vars, bodyBytes, contentType)
	if err != nil {
		return nil, err
	}

	httpClient, err := buildHTTPClient(opts)
	if err != nil {
		return nil, err
	}

	var jar *recordingJar
	if opts.Cookies != nil {
		jar = newRecordingJar()
		if stored := opts.Cookies.CookiesForHost(req.URL.Hostname()); len(stored) > 0 {
			jar.preload(req.URL, stored)
		}
		httpClient.Jar = jar
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return &Result{Error: err.Error(), DurationMS: time.Since(start).Milliseconds(), ResolvedURL: rawURL}, nil
	}

	if spec.Auth.Type == model.AuthDigest && resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		digestReq, derr := buildRequest(ctx, spec, rawURL, vars, bodyBytes, contentType)
		if derr == nil && applyDigestAuth(digestReq, challenge, spec.Auth.Params, vars) {
			resp, err = httpClient.Do(digestReq)
			if err != nil {
				return &Result{Error: err.Error(), DurationMS: time.Since(start).Milliseconds(), ResolvedURL: rawURL}, nil
			}
		}
	}
	defer resp.Body.Close()

	if jar != nil {
		for host, cookies := range jar.sets {
			opts.Cookies.StoreCookies(host, cookies)
		}
	}

	respBody, readErr := io.ReadAll(resp.Body)
	duration := time.Since(start).Milliseconds()
	if readErr != nil {
		return &Result{Error: readErr.Error(), Status: resp.StatusCode, DurationMS: duration, ResolvedURL: rawURL}, nil
	}

	result := &Result{
		Status:      resp.StatusCode,
		StatusText:  resp.Status,
		Headers:     resp.Header,
		DurationMS:  duration,
		SizeBytes:   int64(len(respBody)),
		ResolvedURL: rawURL,
	}
	if isPrintable(respBody) {
		result.Body = string(respBody)
	} else {
		result.Body = base64.StdEncoding.EncodeToString(respBody)
		result.BodyIsBase64 = true
	}

	result.Captured = applyCaptures(spec.Captures, resp.Header, respBody)
	return result, nil
}

// buildBody resolves variables into the request body and returns the raw
// bytes plus the Content-Type it implies. Returning bytes (not a reader)
// lets Execute reuse the same body across the digest-auth retry and lets
// AWS SigV4 hash the payload without consuming a stream.
// applyQueryParams merges spec.Query into rawURL's query string. Uses
// url.Values.Set (last-write-wins per key), not Add, deliberately: Postman
// exports duplicate query params both inline in url.raw AND in a separate
// url.query array (the importer used to just keep both, harmlessly, back
// when this array was never actually applied to the outgoing request) —
// Set collapses that duplication down to one value per key instead of
// sending every param twice for anything imported before this existed.
func applyQueryParams(rawURL string, query []model.KV, vars map[string]string) string {
	var enabled []model.KV
	for _, kv := range query {
		if !kv.Disabled {
			enabled = append(enabled, kv)
		}
	}
	if len(enabled) == 0 {
		return rawURL
	}
	base, existingQuery, _ := strings.Cut(rawURL, "?")
	q, _ := url.ParseQuery(existingQuery) // malformed existing query just starts empty, not fatal
	for _, kv := range enabled {
		q.Set(Resolve(kv.Key, vars), Resolve(kv.Value, vars))
	}
	return base + "?" + q.Encode()
}

func buildBody(body model.Body, vars map[string]string) ([]byte, string, error) {
	switch body.Mode {
	case model.BodyRaw, model.BodyGraphQL:
		return []byte(Resolve(body.Raw, vars)), rawLanguageToContentType(body.RawLanguage), nil
	case model.BodyURLEncoded:
		form := url.Values{}
		for _, kv := range body.URLEncoded {
			if kv.Disabled {
				continue
			}
			form.Set(Resolve(kv.Key, vars), Resolve(kv.Value, vars))
		}
		return []byte(form.Encode()), "application/x-www-form-urlencoded", nil
	case model.BodyFormData:
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		for _, f := range body.FormData {
			if f.Disabled {
				continue
			}
			if f.Type == "file" {
				// File contents aren't stored in the collection; the field
				// is written as empty. The UI attaches real file bytes via
				// a separate upload path in a later version.
				part, _ := mw.CreateFormFile(Resolve(f.Key, vars), "")
				_ = part
				continue
			}
			_ = mw.WriteField(Resolve(f.Key, vars), Resolve(f.Value, vars))
		}
		_ = mw.Close()
		return buf.Bytes(), mw.FormDataContentType(), nil
	}
	return nil, "", nil
}

func buildRequest(ctx context.Context, spec model.RequestSpec, rawURL string, vars map[string]string, bodyBytes []byte, contentType string) (*http.Request, error) {
	var bodyReader io.Reader
	if len(bodyBytes) > 0 {
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	for _, h := range spec.Headers {
		if h.Disabled {
			continue
		}
		req.Header.Set(Resolve(h.Key, vars), Resolve(h.Value, vars))
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}

	applyAuth(req, spec.Auth, vars, bodyBytes)
	return req, nil
}

func rawLanguageToContentType(lang string) string {
	switch lang {
	case "json":
		return "application/json"
	case "xml":
		return "application/xml"
	case "html":
		return "text/html"
	default:
		return "text/plain"
	}
}

// applyAuth handles every auth type that can be fully resolved in one pass
// (i.e. everything except Digest, which needs the server's challenge first
// and is handled in Execute).
func applyAuth(req *http.Request, auth model.Auth, vars map[string]string, bodyBytes []byte) {
	switch auth.Type {
	case model.AuthBasic:
		req.SetBasicAuth(Resolve(auth.Params["username"], vars), Resolve(auth.Params["password"], vars))
	case model.AuthBearer, model.AuthOAuth2:
		token := Resolve(auth.Params["token"], vars)
		if auth.Type == model.AuthOAuth2 {
			token = Resolve(auth.Params["accessToken"], vars)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	case model.AuthAPIKey:
		key := Resolve(auth.Params["key"], vars)
		value := Resolve(auth.Params["value"], vars)
		if strings.EqualFold(auth.Params["in"], "query") {
			q := req.URL.Query()
			q.Set(key, value)
			req.URL.RawQuery = q.Encode()
		} else {
			req.Header.Set(key, value)
		}
	case model.AuthAWSSigV4:
		applyAWSSigV4(req, auth.Params, vars, bodyBytes)
	}
}

func applyCaptures(captures []model.Capture, headers http.Header, body []byte) map[string]string {
	if len(captures) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, c := range captures {
		switch c.Source {
		case "header":
			if v := headers.Get(c.From); v != "" {
				out[c.IntoVar] = v
			}
		case "body_json":
			var parsed any
			if json.Unmarshal(body, &parsed) == nil {
				if v, ok := jsonPathLookup(parsed, c.From); ok {
					out[c.IntoVar] = fmt.Sprintf("%v", v)
				}
			}
		}
	}
	return out
}

// jsonPathLookup resolves a dotted path like "data.token" against decoded JSON.
func jsonPathLookup(v any, path string) (any, bool) {
	cur := v
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func isPrintable(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	sample := b
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	for _, r := range string(sample) {
		if r == '�' {
			return false
		}
	}
	return true
}
