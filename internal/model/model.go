// Package model holds the app's normalized data model. Postman's on-disk
// format is polymorphic (see internal/importer); everything downstream of the
// importer works against these plain, order-preserving types instead.
package model

import "time"

type KV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled,omitempty"`
}

type AuthType string

const (
	AuthNone    AuthType = "none"
	AuthBasic   AuthType = "basic"
	AuthBearer  AuthType = "bearer"
	AuthAPIKey  AuthType = "apikey"
	AuthInherit AuthType = "inherit"
	AuthDigest  AuthType = "digest"
	AuthAWSSigV4 AuthType = "awsv4"
	// AuthOAuth2 applies a bearer token the same way AuthBearer does; the
	// token in Params["accessToken"] is fetched ahead of time via
	// POST /api/oauth2/token (client_credentials/password/authorization_code)
	// and cached on the request, not re-fetched on every send.
	AuthOAuth2 AuthType = "oauth2"
)

type Auth struct {
	Type AuthType `json:"type"`
	// Params holds type-specific fields, e.g. basic: username/password,
	// bearer: token, apikey: key/value/addTo ("header"|"query").
	Params map[string]string `json:"params,omitempty"`
}

type BodyMode string

const (
	BodyNone       BodyMode = "none"
	BodyRaw        BodyMode = "raw"
	BodyURLEncoded BodyMode = "urlencoded"
	BodyFormData   BodyMode = "formdata"
	BodyGraphQL    BodyMode = "graphql"
	BodySoap       BodyMode = "soap"
)

type FormField struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"` // "text" | "file"
	Disabled bool   `json:"disabled,omitempty"`
}

type Body struct {
	Mode        BodyMode    `json:"mode"`
	Raw         string      `json:"raw,omitempty"`
	RawLanguage string      `json:"rawLanguage,omitempty"` // json|xml|text|html
	URLEncoded  []KV        `json:"urlEncoded,omitempty"`
	FormData    []FormField `json:"formData,omitempty"`
	SoapVersion string      `json:"soapVersion,omitempty"` // "1.1" | "1.2" — only used when Mode == soap
	SoapAction  string      `json:"soapAction,omitempty"`  // only used when Mode == soap
	// WS-Security UsernameToken (only used when Mode == soap). Mode is
	// "" (off) | "passwordText" | "passwordDigest". The password is stored
	// the same way a Basic-auth password already is — plaintext in this
	// collection's JSON — never send anything more sensitive (a private
	// key) through these fields.
	WsSecurityMode     string `json:"wsSecurityMode,omitempty"`
	WsSecurityUsername string `json:"wsSecurityUsername,omitempty"`
	WsSecurityPassword string `json:"wsSecurityPassword,omitempty"`
	// SignBody, when true, XML-signs the SOAP Body (WS-Security X.509
	// message signing) using the client certificate configured in
	// Settings — the same one used for mutual TLS. Only the on/off flag
	// lives here; the cert/key paths stay in Settings so a private key
	// path never ends up in collection JSON.
	SignBody bool `json:"signBody,omitempty"`
}

// Capture is our own (non-Postman) feature: after a response comes back,
// pull a value out of it into an environment variable. Covers the common
// "GET X-CSRF-Token: Fetch, then reuse it" pattern without needing a JS
// engine to run Postman's pm.environment.set() scripts.
type Capture struct {
	Source string `json:"source"` // "header" | "body_json"
	From   string `json:"from"`   // header name, or JSON path like "data.token"
	IntoVar string `json:"intoVar"`
}

type RequestSpec struct {
	Method  string    `json:"method"`
	URLRaw  string    `json:"urlRaw"` // may contain {{vars}}; kept verbatim, not re-encoded
	Query   []KV      `json:"query,omitempty"`
	Headers []KV      `json:"headers,omitempty"`
	Auth    Auth      `json:"auth"`
	Body    Body      `json:"body"`
	Captures []Capture `json:"captures,omitempty"`

	// Raw scripts imported from Postman, kept for visibility but not
	// executed (see internal/importer doc comment for why).
	PreRequestScript string `json:"preRequestScript,omitempty"`
	TestScript       string `json:"testScript,omitempty"`
	HasScript        bool   `json:"hasScript,omitempty"`
}

// Node is either a folder (Children non-nil, Request nil) or a request leaf.
//
// Children deliberately has no `omitempty`: the frontend tells folder from
// request purely by `if (node.children)`, and Go's omitempty drops a slice
// whenever its length is 0 — nil (a request leaf) and a real, currently-
// empty folder ([]*Node{}) would both vanish from the JSON and become
// indistinguishable (and misrender as requests) on the next load. Without
// omitempty, nil still encodes as `null` (falsy) while an empty folder
// encodes as `[]` (truthy), preserving the distinction.
type Node struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Children []*Node      `json:"children"`
	Request  *RequestSpec `json:"request,omitempty"`
}

type Collection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Variables []KV      `json:"variables,omitempty"`
	Auth      Auth      `json:"auth"` // collection-level default auth
	Root      []*Node   `json:"root"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Environment struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Values []KV   `json:"values"`
	// ClientCertFile/ClientKeyFile, when set, override the global mTLS
	// cert configured in Settings for requests sent under this
	// environment — different environments (dev/test/prod) commonly need
	// different client identities. Empty means "use the global Settings
	// cert", not "use no cert".
	ClientCertFile string    `json:"clientCertFile,omitempty"`
	ClientKeyFile  string    `json:"clientKeyFile,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type HistoryEntry struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	URL        string    `json:"url"` // resolved (vars substituted), for display only
	Status     int       `json:"status"`
	DurationMS int64     `json:"durationMs"`
	SizeBytes  int64     `json:"sizeBytes"`

	// Request is the original, unresolved spec (as composed, {{vars}}
	// intact) — kept so a history entry can be reloaded into the editor
	// and re-sent, not just looked at. CollectionID/EnvironmentID (best
	// effort: the collection/environment may since have been deleted) let
	// the reload restore the same var/inherit-auth context it ran under.
	Request       RequestSpec `json:"request"`
	CollectionID  string      `json:"collectionId,omitempty"`
	EnvironmentID string      `json:"environmentId,omitempty"`
}

// CookieRecord is one persisted cookie, matched against outgoing requests
// by exact-or-subdomain match on Domain. Path-scoping isn't modeled — v1
// sends a cookie to every path on a matching domain, which is broader than
// browsers but fine for the session-cookie-on-one-API-host case this
// exists for.
type CookieRecord struct {
	Domain   string    `json:"domain"`
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"httpOnly,omitempty"`
}

// RunStepResult is one executed request within a collection run.
type RunStepResult struct {
	Iteration  int    `json:"iteration"`
	NodeID     string  `json:"nodeId"`
	Name       string  `json:"name"`
	Method     string  `json:"method"`
	URL        string  `json:"url"`
	Status     int     `json:"status"`
	DurationMS int64   `json:"durationMs"`
	SizeBytes  int64   `json:"sizeBytes"`
	Error      string  `json:"error,omitempty"`
}
