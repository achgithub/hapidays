// Package openapi imports an OpenAPI 3.0/3.1 or Swagger 2.0 document (JSON
// or YAML) into a hapidays collection — the same "point at a schema, get a
// runnable collection" pattern internal/wsdl, internal/odata, and
// internal/graphqlintro already implement for their own protocols, but for
// the far more common REST-API artifact none of those cover.
//
// Both spec versions parse into the same normalizedDoc (see parseV3/parseV2)
// so collection generation, JSON Schema example generation, and auth
// mapping are all written once; only the two parse functions differ,
// reflecting the documents' actual shape differences (servers[] vs
// host+basePath+schemes[], components vs definitions, a requestBody object
// vs an in:body/in:formData parameter).
package openapi

import "sort"

// normalizedDoc is the version-agnostic shape both parsers produce.
type normalizedDoc struct {
	Title      string
	Servers    []server
	Operations []operation
	// Security is the document-wide default security requirement — the
	// names of schemes in Schemes, ANDed together (OpenAPI's outer list is
	// OR of alternatives, inner list is AND; hapidays's Auth model can only
	// express one scheme at a time, so — same reasoning as everywhere else
	// in this codebase that trims a spec down to what's actually useful —
	// only the first alternative is used).
	Security []string
	Schemes  map[string]securityScheme
	root     map[string]any // the raw parsed document, kept for $ref resolution
}

type server struct {
	Name string // description, or "Server N" — becomes a generated Environment's name
	URL  string // server variables already substituted with their declared default
}

type operation struct {
	Method      string // "GET", "POST", ...
	Path        string // "/pets/{petId}"
	OperationID string
	Tag         string // first tag, or "" for untagged
	Params      []opParam
	RequestBody *requestBody // nil if this operation has no body
	// Security is nil when the operation just uses the document default,
	// or a (possibly empty, meaning explicitly no auth) list when the
	// operation overrides it — the same nil-vs-empty distinction
	// model.AuthInherit vs model.AuthNone needs downstream.
	Security []string
}

type opParam struct {
	Name     string
	In       string // "path" | "query" | "header"
	Required bool
	Schema   map[string]any
}

type requestBody struct {
	ContentType string // "application/json" | "application/x-www-form-urlencoded" | "multipart/form-data"
	Schema      map[string]any
}

type securityScheme struct {
	Type             string // "apiKey" | "http" | "oauth2" | "openIdConnect"
	Scheme           string // http: "basic" | "bearer"
	In               string // apiKey: "header" | "query"
	ParamName        string // apiKey: the header/query param name
	OpenIDConnectURL string
	Flow             *oauthFlow
}

type oauthFlow struct {
	GrantType string // "client_credentials" | "authorization_code" | "password"
	AuthURL   string
	TokenURL  string
	Scopes    []string
}

// securityNames flattens a spec's `security` value — a list of
// {schemeName: [scopes]} objects, OR'd together — down to the scheme names
// in the first alternative, per normalizedDoc.Security's doc comment.
// Returns nil for an absent/malformed value, and a non-nil empty slice for
// an explicit `security: []` (no auth), which callers need to tell apart.
func securityNames(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	if len(list) == 0 {
		return []string{}
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		return []string{}
	}
	names := make([]string, 0, len(first))
	for name := range first {
		names = append(names, name)
	}
	sort.Strings(names) // map iteration order isn't stable; pick deterministically
	return names
}
