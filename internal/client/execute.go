// Package client resolves variables into a request and executes it over
// plain HTTP or TLS, honoring corporate proxies, extra trust roots, and
// mutual TLS client certs — the things enterprise networks actually need.
package client

import (
	"bytes"
	"context"
	"crypto"
	crand "crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"hapidays/internal/model"
	"hapidays/internal/store"
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
	ResolvedURL string                  `json:"resolvedUrl,omitempty"`
	Assertions  []model.AssertionResult `json:"assertions,omitempty"`
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
	CookiesForHost(host, path string) []*http.Cookie
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
	// CollectionHeaders are merged under the request's own headers by
	// MergeHeaders before every send — see Collection.Headers.
	CollectionHeaders []model.KV
}

// MergeHeaders combines a collection's default headers with a request's own,
// a request header overriding the collection header of the same key
// (case-insensitive, matching HTTP semantics) rather than sending both.
// Collection headers keep their relative order; a request header with a new
// key is appended after them, in its own order.
func MergeHeaders(collectionHeaders, requestHeaders []model.KV) []model.KV {
	merged := make([]model.KV, 0, len(collectionHeaders)+len(requestHeaders))
	index := map[string]int{}
	for _, h := range collectionHeaders {
		index[strings.ToLower(h.Key)] = len(merged)
		merged = append(merged, h)
	}
	for _, h := range requestHeaders {
		key := strings.ToLower(h.Key)
		if i, ok := index[key]; ok {
			merged[i] = h
			continue
		}
		index[key] = len(merged)
		merged = append(merged, h)
	}
	return merged
}

// buildTLSConfig builds the TLS config shared by the HTTP client and (for
// gRPC, which has no http.Transport) credentials.NewTLS — mTLS cert, extra
// CA, and skip-verify all apply the same way regardless of transport.
func buildTLSConfig(opts Options) (*tls.Config, error) {
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
	return tlsCfg, nil
}

func buildHTTPClient(opts Options) (*http.Client, error) {
	tlsCfg, err := buildTLSConfig(opts)
	if err != nil {
		return nil, err
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

// NewHTTPClient exposes buildHTTPClient for callers that need the same
// TLS/proxy/mTLS-aware client this package uses internally but want to
// send something other than a single Execute-shaped request — e.g. an
// OData $batch request, which is one HTTP call carrying several bundled
// sub-requests in its body.
func NewHTTPClient(opts Options) (*http.Client, error) {
	return buildHTTPClient(opts)
}

// PrepareRequest resolves spec into a ready-to-send, fully-built
// *http.Request — variables substituted, query params merged, body built
// (including SOAP/WS-Security), auth applied — without sending it. Used by
// OData $batch to bundle several already-resolved requests into one HTTP
// call; Execute itself doesn't use this since it also needs the digest-auth
// retry path, which re-resolves the body for the second request.
func PrepareRequest(ctx context.Context, spec model.RequestSpec, vars map[string]string, opts Options) (*http.Request, error) {
	if spec.Auth.Type == model.AuthInherit {
		spec.Auth = opts.CollectionAuth
	}
	spec.Headers = MergeHeaders(opts.CollectionHeaders, spec.Headers)
	rawURL := Resolve(spec.URLRaw, vars)
	rawURL = applyQueryParams(rawURL, spec.Query, vars)
	bodyBytes, contentType, err := buildBody(spec.Body, vars, opts.Settings)
	if err != nil {
		return nil, err
	}
	return buildRequest(ctx, spec, rawURL, vars, bodyBytes, contentType)
}

// Execute resolves variables into spec, sends the request, and applies any
// Capture rules against the response. It never runs Postman pre-
// request/test scripts (spec.PreRequestScript / TestScript) — those are
// display-only; see internal/importer for why.
//
// Digest auth (RFC 7616) needs two round trips — an initial request to
// receive the WWW-Authenticate challenge, then the real, signed one — so
// it's handled here rather than in a single applyAuth call.
func Execute(ctx context.Context, spec model.RequestSpec, vars map[string]string, opts Options) (*Result, error) {
	if spec.Auth.Type == model.AuthInherit {
		spec.Auth = opts.CollectionAuth
	}
	spec.Headers = MergeHeaders(opts.CollectionHeaders, spec.Headers)

	rawURL := Resolve(spec.URLRaw, vars)
	rawURL = applyQueryParams(rawURL, spec.Query, vars)

	bodyBytes, contentType, err := buildBody(spec.Body, vars, opts.Settings)
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
		if stored := opts.Cookies.CookiesForHost(req.URL.Hostname(), req.URL.Path); len(stored) > 0 {
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
		if derr == nil && applyDigestAuth(digestReq, challenge, spec.Auth.Params, vars, bodyBytes) {
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
	result.Assertions = applyAssertions(spec.Assertions, result, resp.Header, respBody)
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

func buildBody(body model.Body, vars map[string]string, settings store.Settings) ([]byte, string, error) {
	switch body.Mode {
	case model.BodyRaw:
		return []byte(Resolve(body.Raw, vars)), rawLanguageToContentType(body.RawLanguage), nil
	case model.BodyGraphQL:
		if body.GraphQLQuery == "" && body.Raw != "" {
			// Collections saved before the split query/variables editor
			// stored the already-assembled {"query","variables"} JSON
			// directly in Raw — keep sending that until it's resaved
			// through the new fields.
			return []byte(Resolve(body.Raw, vars)), rawLanguageToContentType(body.RawLanguage), nil
		}
		variables := json.RawMessage("null")
		if v := strings.TrimSpace(Resolve(body.GraphQLVariables, vars)); v != "" {
			if !json.Valid([]byte(v)) {
				return nil, "", fmt.Errorf("GraphQL variables must be valid JSON: %s", v)
			}
			variables = json.RawMessage(v)
		}
		payload, err := json.Marshal(struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
		}{Query: Resolve(body.GraphQLQuery, vars), Variables: variables})
		if err != nil {
			return nil, "", fmt.Errorf("build GraphQL payload: %w", err)
		}
		return payload, "application/json", nil
	case model.BodySoap:
		envelope := Resolve(body.Raw, vars)
		if body.WsSecurityMode != "" {
			envelope = insertWsSecurityHeader(envelope, body.WsSecurityMode, Resolve(body.WsSecurityUsername, vars), Resolve(body.WsSecurityPassword, vars))
		}
		if body.SignBody {
			if settings.ClientCertFile == "" || settings.ClientKeyFile == "" {
				return nil, "", fmt.Errorf("this request has \"Sign body (X.509)\" enabled but no client certificate is configured — set one in Settings (the same cert used for mutual TLS)")
			}
			signed, err := signSoapBody(envelope, settings.ClientCertFile, settings.ClientKeyFile)
			if err != nil {
				return nil, "", fmt.Errorf("sign SOAP body: %w", err)
			}
			envelope = signed
		}
		return []byte(envelope), soapContentType(body, vars), nil
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
				src := Resolve(f.Src, vars)
				if src == "" {
					continue
				}
				file, err := os.Open(src)
				if err != nil {
					return nil, "", fmt.Errorf("form field %q: open %s: %w", f.Key, src, err)
				}
				part, err := mw.CreateFormFile(Resolve(f.Key, vars), filepath.Base(src))
				if err != nil {
					file.Close()
					return nil, "", fmt.Errorf("form field %q: %w", f.Key, err)
				}
				_, err = io.Copy(part, file)
				file.Close()
				if err != nil {
					return nil, "", fmt.Errorf("form field %q: read %s: %w", f.Key, src, err)
				}
				continue
			}
			_ = mw.WriteField(Resolve(f.Key, vars), Resolve(f.Value, vars))
		}
		_ = mw.Close()
		return buf.Bytes(), mw.FormDataContentType(), nil
	}
	return nil, "", nil
}

const (
	wsseNS            = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	wsuNS             = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"
	passwordTextURI   = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText"
	passwordDigestURI = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"
	base64BinaryURI   = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary"
)

var (
	headerOpenCloseRe = regexp.MustCompile(`(?is)(<[\w.-]*:?Header[^>]*>)(.*?)(</[\w.-]*:?Header>)`)
	headerSelfCloseRe = regexp.MustCompile(`(?is)<[\w.-]*:?Header\s*/>`)
	envelopeOpenRe    = regexp.MustCompile(`(?is)(<[\w.-]*:?Envelope\b[^>]*>)`)
)

// buildUsernameTokenXML implements the OASIS WS-Security UsernameToken
// Profile 1.0: PasswordText sends the password as-is (relies on transport
// security); PasswordDigest sends
// Base64(SHA1(decode_base64(Nonce) ++ utf8(Created) ++ utf8(Password))) so
// the plaintext password never crosses the wire, at the cost of Created
// having to be within the server's clock-skew tolerance (commonly ~5 min) —
// which is why this runs at send time in Go, never precomputed client-side.
func buildUsernameTokenXML(mode, username, password string) string {
	if mode == "passwordDigest" {
		nonce := make([]byte, 16)
		_, _ = crand.Read(nonce)
		nonceB64 := base64.StdEncoding.EncodeToString(nonce)
		created := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(created))
		h.Write([]byte(password))
		digest := base64.StdEncoding.EncodeToString(h.Sum(nil))
		return fmt.Sprintf(`<wsse:Security xmlns:wsse="%s" soap:mustUnderstand="1">
      <wsse:UsernameToken xmlns:wsu="%s">
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="%s">%s</wsse:Password>
        <wsse:Nonce EncodingType="%s">%s</wsse:Nonce>
        <wsu:Created>%s</wsu:Created>
      </wsse:UsernameToken>
    </wsse:Security>`, wsseNS, wsuNS, xmlEscape(username), passwordDigestURI, digest, base64BinaryURI, nonceB64, created)
	}
	return fmt.Sprintf(`<wsse:Security xmlns:wsse="%s" soap:mustUnderstand="1">
      <wsse:UsernameToken xmlns:wsu="%s">
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="%s">%s</wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>`, wsseNS, wsuNS, xmlEscape(username), passwordTextURI, xmlEscape(password))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// insertWsSecurityHeader splices a wsse:Security block into the envelope's
// soap:Header. Never re-serializes the envelope with encoding/xml — that
// mangles namespace prefixes on round-trip — so this is string surgery,
// same approach and same prefix-agnostic matching as detectSoapFault in
// app.js. Three cases: an existing open/close Header (insert before the
// close), a self-closing <soap:Header/> (expand it), or no Header element
// at all (insert one right after the Envelope's opening tag).
func insertWsSecurityHeader(envelope, mode, username, password string) string {
	token := buildUsernameTokenXML(mode, username, password)
	if m := headerOpenCloseRe.FindStringSubmatchIndex(envelope); m != nil {
		insertAt := m[5] // start of the closing tag capture group
		return envelope[:insertAt] + "  " + token + "\n    " + envelope[insertAt:]
	}
	if loc := headerSelfCloseRe.FindStringIndex(envelope); loc != nil {
		return envelope[:loc[0]] + "<soap:Header>\n    " + token + "\n  </soap:Header>" + envelope[loc[1]:]
	}
	if m := envelopeOpenRe.FindStringSubmatchIndex(envelope); m != nil {
		insertAt := m[1] // end of the opening Envelope tag
		return envelope[:insertAt] + "\n  <soap:Header>\n    " + token + "\n  </soap:Header>" + envelope[insertAt:]
	}
	// No recognizable Envelope at all — leave the body untouched rather
	// than guessing; the user will see the request fail auth server-side,
	// which is a clearer signal than a header silently going nowhere.
	return envelope
}

// signSoapBody implements WS-Security X.509 message signing: it XML-signs
// the soap:Body (RSA-SHA256 over the exclusive-C14N-canonicalized element)
// and inserts the resulting <ds:Signature> into wsse:Security in the
// header, referencing the Body by a wsu:Id.
//
// Unlike insertWsSecurityHeader (string splicing, used for UsernameToken),
// this parses the envelope with etree rather than treating it as text.
// That's not a style choice — canonicalization operates on the parsed,
// namespace-resolved element tree, so the bytes that get digested and the
// bytes that get sent must come from the same parse. Splicing a signature
// into text after the fact would sign one representation and send another.
func signSoapBody(envelope, certFile, keyFile string) (string, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return "", fmt.Errorf("load signing cert/key: %w", err)
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return "", fmt.Errorf("configured signing key does not support XML signing")
	}

	doc := etree.NewDocument()
	if err := doc.ReadFromString(envelope); err != nil {
		return "", fmt.Errorf("parse envelope for signing: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return "", fmt.Errorf("empty envelope")
	}

	body := findChildByLocal(root, "Body")
	if body == nil {
		return "", fmt.Errorf("no soap:Body element found to sign")
	}
	if body.SelectAttrValue("Id", "") == "" {
		body.CreateAttr("xmlns:wsu", wsuNS)
		body.CreateAttr("wsu:Id", "Body-1")
	}
	// goxmldsig's exclusive-C14N pass canonicalizes starting from a fresh,
	// empty namespace context — it doesn't walk up to ancestors the way
	// NSBuildParentContext (used elsewhere in ConstructSignature) does. The
	// envelope's own soap: prefix is declared once, on the root Envelope,
	// so Body's tag (soap:Body) would otherwise reference an "undeclared"
	// prefix from the canonicalizer's point of view. Inlining the
	// declaration directly onto Body — a redundant but harmless
	// declaration in plain XML terms — makes it resolvable without one.
	if envNS := root.SelectAttrValue("xmlns:"+root.Space, ""); envNS != "" {
		if body.SelectAttrValue("xmlns:"+root.Space, "") == "" {
			body.CreateAttr("xmlns:"+root.Space, envNS)
		}
	}

	ctx, err := dsig.NewSigningContext(signer, [][]byte{cert.Certificate[0]})
	if err != nil {
		return "", fmt.Errorf("create signing context: %w", err)
	}
	ctx.IdAttribute = "Id" // matches wsu:Id — etree matches attr keys namespace-agnostically when the lookup key has no prefix
	ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")

	// enveloped=false: the signature is going into the header, not inside
	// the Body it signs, so the enveloped-signature transform (which
	// exists to let a verifier strip a Signature that's a *descendant* of
	// the signed element before canonicalizing) doesn't apply here.
	sigEl, err := ctx.ConstructSignature(body, false)
	if err != nil {
		return "", fmt.Errorf("construct XML signature: %w", err)
	}

	security := findOrCreateSecurityHeader(root)
	security.AddChild(sigEl)

	out, err := doc.WriteToString()
	if err != nil {
		return "", fmt.Errorf("serialize signed envelope: %w", err)
	}
	return out, nil
}

func findChildByLocal(parent *etree.Element, local string) *etree.Element {
	for _, c := range parent.ChildElements() {
		if c.Tag == local {
			return c
		}
	}
	return nil
}

// findOrCreateSecurityHeader mirrors insertWsSecurityHeader's three cases
// (existing Header, none at all) but operates on the parsed tree — used
// when signing runs without a prior UsernameToken having already spliced a
// Header/Security in as text.
func findOrCreateSecurityHeader(envelope *etree.Element) *etree.Element {
	header := findChildByLocal(envelope, "Header")
	if header == nil {
		header = etree.NewElement(envelope.Space + ":Header")
		envelope.InsertChildAt(0, header) // Header must precede Body
	}
	security := findChildByLocal(header, "Security")
	if security == nil {
		security = header.CreateElement("wsse:Security")
		security.CreateAttr("xmlns:wsse", wsseNS)
		security.CreateAttr(envelope.Space+":mustUnderstand", "1")
	}
	return security
}

// soapContentType computes the Content-Type per the SOAP 1.1/1.2 standards.
// SOAP 1.1 carries the action in a separate SOAPAction header (set in
// buildRequest); SOAP 1.2 embeds it as an `action` parameter on the
// Content-Type itself and has no SOAPAction header at all.
func soapContentType(body model.Body, vars map[string]string) string {
	if body.SoapVersion == "1.2" {
		ct := "application/soap+xml; charset=utf-8"
		if action := Resolve(body.SoapAction, vars); action != "" {
			ct += `; action="` + action + `"`
		}
		return ct
	}
	return "text/xml; charset=utf-8"
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
	// SOAP 1.1 requires SOAPAction as its own header (quoted, and still
	// present-but-empty when there's no action — RFC-shaped like `""`, never
	// omitted). SOAP 1.2 has no such header; the action lives in
	// Content-Type's `action` param instead (see soapContentType).
	if spec.Body.Mode == model.BodySoap && spec.Body.SoapVersion != "1.2" && req.Header.Get("SOAPAction") == "" {
		req.Header.Set("SOAPAction", `"`+Resolve(spec.Body.SoapAction, vars)+`"`)
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

// applyAssertions evaluates each Assertion against the response and returns
// one AssertionResult per enabled assertion, in order. Never returns an
// error — an assertion that can't be evaluated (e.g. json_path_equals
// against a non-JSON body) fails with a message explaining why, the same
// as any other unmet assertion, rather than aborting the whole response.
func applyAssertions(assertions []model.Assertion, result *Result, headers http.Header, body []byte) []model.AssertionResult {
	if len(assertions) == 0 {
		return nil
	}
	var jsonBody any
	jsonErr := json.Unmarshal(body, &jsonBody)

	out := make([]model.AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		if a.Disabled {
			continue
		}
		r := model.AssertionResult{Assertion: a}
		switch a.Type {
		case model.AssertStatusEquals:
			want := a.Expected
			got := strconv.Itoa(result.Status)
			r.Passed = got == want
			r.Message = fmt.Sprintf("status %s (expected %s)", got, want)
		case model.AssertStatusRange:
			r.Passed = statusInRange(result.Status, a.Expected)
			r.Message = fmt.Sprintf("status %d (expected %s)", result.Status, a.Expected)
		case model.AssertHeaderExists:
			_, ok := headers[textproto.CanonicalMIMEHeaderKey(a.Target)]
			r.Passed = ok
			r.Message = fmt.Sprintf("header %q present: %v", a.Target, ok)
		case model.AssertHeaderEquals:
			got := headers.Get(a.Target)
			r.Passed = got == a.Expected
			r.Message = fmt.Sprintf("header %q = %q (expected %q)", a.Target, got, a.Expected)
		case model.AssertBodyContains:
			r.Passed = strings.Contains(string(body), a.Expected)
			r.Message = fmt.Sprintf("body contains %q: %v", a.Expected, r.Passed)
		case model.AssertJSONPathExists:
			if jsonErr != nil {
				r.Message = "response body is not valid JSON: " + jsonErr.Error()
				break
			}
			_, ok := jsonPathLookup(jsonBody, a.Target)
			r.Passed = ok
			r.Message = fmt.Sprintf("json path %q present: %v", a.Target, ok)
		case model.AssertJSONPathEquals:
			if jsonErr != nil {
				r.Message = "response body is not valid JSON: " + jsonErr.Error()
				break
			}
			v, ok := jsonPathLookup(jsonBody, a.Target)
			got := ""
			if ok {
				got = fmt.Sprintf("%v", v)
			}
			r.Passed = ok && got == a.Expected
			r.Message = fmt.Sprintf("json path %q = %q (expected %q)", a.Target, got, a.Expected)
		case model.AssertMaxDurationMS:
			maxMS, err := strconv.ParseInt(a.Expected, 10, 64)
			if err != nil {
				r.Message = "invalid max_duration_ms expected value: " + a.Expected
				break
			}
			r.Passed = result.DurationMS <= maxMS
			r.Message = fmt.Sprintf("took %dms (max %dms)", result.DurationMS, maxMS)
		default:
			r.Message = "unknown assertion type: " + a.Type
		}
		out = append(out, r)
	}
	return out
}

func statusInRange(status int, rangeSpec string) bool {
	if len(rangeSpec) != 3 || rangeSpec[1] != 'x' || rangeSpec[2] != 'x' {
		return false
	}
	return status/100 == int(rangeSpec[0]-'0')
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
