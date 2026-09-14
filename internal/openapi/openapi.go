package openapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"hapidays/internal/model"
	yaml "sigs.k8s.io/yaml"
)

// Import parses an OpenAPI 3.0/3.1 or Swagger 2.0 document (JSON or YAML —
// sigs.k8s.io/yaml accepts both through one call, since JSON is a YAML
// subset) and returns a runnable collection plus one Environment per
// server the spec declares. Credential placeholders (API keys, tokens,
// passwords) always land in those environments, never as literal values or
// collection variables — the same rule this app applies everywhere else:
// collections are the shareable/portable unit, environments are where a
// secret belongs, even a placeholder one.
//
// Unlike internal/odata.Import, this doesn't take an auth parameter for
// the generated collection's default — an OpenAPI/Swagger document
// describes its own security schemes, so there's a real default to derive
// (see planAuth) rather than needing to borrow whatever auth fetched the
// document itself.
//
// sourceURL is the URL the document was fetched from (empty for a locally
// uploaded file) — needed because a server URL is allowed to be relative
// (e.g. "/api/v3", the Swagger Petstore's own real spec does exactly this),
// meant to resolve against wherever the spec itself is served from, the
// same way a browser resolves a relative link.
func Import(data []byte, sourceURL string, newID func() string) (*model.Collection, []*model.Environment, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse OpenAPI/Swagger document: %w", err)
	}

	var nd *normalizedDoc
	var err error
	switch {
	case hasStringPrefix(raw["openapi"], "3."):
		nd, err = parseV3(raw)
	case raw["swagger"] == "2.0":
		nd, err = parseV2(raw)
	default:
		return nil, nil, fmt.Errorf(`not a recognizable OpenAPI 3.x or Swagger 2.0 document (no "openapi": "3.x" or "swagger": "2.0" field)`)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(nd.Operations) == 0 {
		return nil, nil, fmt.Errorf(`no operations found under this document's "paths"`)
	}
	for i := range nd.Servers {
		nd.Servers[i].URL = resolveServerURL(nd.Servers[i].URL, sourceURL)
	}

	plan := planAuth(nd)
	return buildCollection(nd, plan, newID), buildEnvironments(nd, plan, newID), nil
}

func hasStringPrefix(v any, prefix string) bool {
	s, ok := v.(string)
	return ok && strings.HasPrefix(s, prefix)
}

// resolveServerURL resolves a possibly-relative server URL against
// sourceURL, exactly like a browser resolves a relative link against the
// page it's on. A no-op when serverURL is already absolute, sourceURL is
// unknown (a locally uploaded file), or either fails to parse — in which
// case serverURL is returned as-is and ends up in the generated
// Environment verbatim, same as before this existed.
func resolveServerURL(serverURL, sourceURL string) string {
	if serverURL == "" || sourceURL == "" {
		return serverURL
	}
	base, err := url.Parse(sourceURL)
	if err != nil {
		return serverURL
	}
	ref, err := url.Parse(serverURL)
	if err != nil || ref.IsAbs() {
		return serverURL
	}
	return base.ResolveReference(ref).String()
}

func buildCollection(nd *normalizedDoc, plan authPlan, newID func() string) *model.Collection {
	name := nd.Title
	if name == "" {
		name = "OpenAPI Import"
	}

	col := &model.Collection{
		ID:        newID(),
		Name:      name,
		Auth:      plan.collectionAuth,
		Variables: append([]model.KV{}, plan.notes...),
	}

	// Group by tag, preserving nd.Operations' own deterministic order
	// (sorted path, fixed method order) within each tag.
	tagOrder := []string{}
	byTag := map[string][]operation{}
	for _, op := range nd.Operations {
		if _, seen := byTag[op.Tag]; !seen {
			tagOrder = append(tagOrder, op.Tag)
		}
		byTag[op.Tag] = append(byTag[op.Tag], op)
	}
	sort.Strings(tagOrder)

	for _, tag := range tagOrder {
		folderName := tag
		if folderName == "" {
			folderName = "Other"
		}
		folder := &model.Node{ID: newID(), Name: folderName}
		for _, op := range byTag[tag] {
			folder.Children = append(folder.Children, &model.Node{
				ID:      newID(),
				Name:    requestName(op),
				Request: buildRequest(op, nd, plan, col),
			})
		}
		col.Root = append(col.Root, folder)
	}
	return col
}

func requestName(op operation) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	return op.Method + " " + op.Path
}

func buildRequest(op operation, nd *normalizedDoc, plan authPlan, col *model.Collection) *model.RequestSpec {
	req := &model.RequestSpec{
		Method: op.Method,
		URLRaw: "{{baseUrl}}" + convertPathTemplate(op.Path),
		Auth:   resolveOpAuth(op, plan),
		Body:   model.Body{Mode: model.BodyNone},
	}

	for _, p := range op.Params {
		seedCollectionVar(col, p.Name, schemaPlaceholder(p.Schema, nd.root, map[string]bool{}))
		switch p.In {
		case "query":
			req.Query = append(req.Query, model.KV{Key: p.Name, Value: "{{" + p.Name + "}}", Disabled: !p.Required})
		case "header":
			req.Headers = append(req.Headers, model.KV{Key: p.Name, Value: "{{" + p.Name + "}}", Disabled: !p.Required})
			// "path" params need no extra field: convertPathTemplate already
			// folded {{name}} into req.URLRaw itself.
		}
	}

	if op.RequestBody != nil {
		req.Body = buildBody(op.RequestBody, nd)
	}
	return req
}

// convertPathTemplate swaps OpenAPI's single-brace path param syntax
// ({petId}) for hapidays's own {{petId}} — nothing else about the path
// changes.
func convertPathTemplate(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '{':
			b.WriteString("{{")
		case '}':
			b.WriteString("}}")
		default:
			b.WriteByte(path[i])
		}
	}
	return b.String()
}

func seedCollectionVar(col *model.Collection, key string, value any) {
	for _, v := range col.Variables {
		if v.Key == key {
			return // already seeded — same param name reused across operations (e.g. every /pet/{petId} op)
		}
	}
	col.Variables = append(col.Variables, model.KV{Key: key, Value: placeholderToString(value)})
}

func placeholderToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func buildBody(rb *requestBody, nd *normalizedDoc) model.Body {
	placeholder := schemaPlaceholder(rb.Schema, nd.root, map[string]bool{})
	switch rb.ContentType {
	case "application/x-www-form-urlencoded":
		obj, _ := placeholder.(map[string]any)
		var kvs []model.KV
		for _, k := range sortedKeys(obj) {
			kvs = append(kvs, model.KV{Key: k, Value: placeholderToString(obj[k])})
		}
		return model.Body{Mode: model.BodyURLEncoded, URLEncoded: kvs}
	case "multipart/form-data":
		obj, _ := placeholder.(map[string]any)
		var fields []model.FormField
		for _, k := range sortedKeys(obj) {
			fields = append(fields, model.FormField{Key: k, Value: placeholderToString(obj[k]), Type: "text"})
		}
		return model.Body{Mode: model.BodyFormData, FormData: fields}
	default: // JSON, or any other content type — best-effort as a raw JSON body
		raw, err := json.MarshalIndent(placeholder, "", "  ")
		if err != nil {
			raw = []byte("{}")
		}
		return model.Body{Mode: model.BodyRaw, Raw: string(raw), RawLanguage: "json"}
	}
}

// authPlan is computed once per document: the collection-level Auth shape
// (derived from the document's default security requirement), the Auth
// shape for every declared scheme (so an operation whose own `security`
// differs from the default still gets the right explicit block), and every
// {{var}} name a credential references — the list that becomes each
// generated Environment's seeded placeholder entries.
type authPlan struct {
	collectionAuth model.Auth
	schemeAuth     map[string]model.Auth
	varNames       []string
	// notes are non-secret collection variables surfaced for a security
	// scheme hapidays has no Auth type for (openIdConnect) — see Import's
	// doc comment on why this isn't silently dropped.
	notes []model.KV
}

func planAuth(nd *normalizedDoc) authPlan {
	plan := authPlan{schemeAuth: map[string]model.Auth{}}
	seen := map[string]bool{}
	addVar := func(name string) {
		if !seen[name] {
			seen[name] = true
			plan.varNames = append(plan.varNames, name)
		}
	}

	for _, name := range sortedKeys(nd.Schemes) {
		s := nd.Schemes[name]
		if s.Type == "openIdConnect" {
			plan.notes = append(plan.notes, model.KV{Key: "openIdConnectDiscoveryUrl", Value: s.OpenIDConnectURL})
			continue
		}
		auth, vars := authForScheme(name, s)
		plan.schemeAuth[name] = auth
		for _, v := range vars {
			addVar(v)
		}
	}

	if len(nd.Security) > 0 {
		if auth, ok := plan.schemeAuth[nd.Security[0]]; ok {
			plan.collectionAuth = auth
		}
	}
	return plan
}

func authForScheme(schemeName string, s securityScheme) (model.Auth, []string) {
	switch s.Type {
	case "apiKey":
		in := s.In
		if in == "" {
			in = "header"
		}
		key := s.ParamName
		if key == "" {
			key = schemeName
		}
		return model.Auth{Type: model.AuthAPIKey, Params: map[string]string{
			"in": in, "key": key, "value": "{{apiKey}}",
		}}, []string{"apiKey"}
	case "http":
		if s.Scheme == "bearer" {
			return model.Auth{Type: model.AuthBearer, Params: map[string]string{"token": "{{bearerToken}}"}}, []string{"bearerToken"}
		}
		// "basic" and any other unrecognized http scheme both map to Basic
		// — hapidays has a dedicated Digest type too, but nothing in the
		// OpenAPI security-scheme model distinguishes it from "basic"
		// cleanly enough to guess at automatically.
		return model.Auth{Type: model.AuthBasic, Params: map[string]string{
			"username": "{{username}}", "password": "{{password}}",
		}}, []string{"username", "password"}
	case "oauth2":
		if s.Flow == nil {
			return model.Auth{}, nil
		}
		params := map[string]string{
			"grantType": s.Flow.GrantType, "accessTokenUrl": s.Flow.TokenURL, "authUrl": s.Flow.AuthURL,
			"clientId": "{{oauthClientId}}", "clientSecret": "{{oauthClientSecret}}",
			"scope": strings.Join(s.Flow.Scopes, " "),
		}
		vars := []string{"oauthClientId", "oauthClientSecret"}
		if s.Flow.GrantType == "password" {
			params["username"] = "{{username}}"
			params["password"] = "{{password}}"
			vars = append(vars, "username", "password")
		}
		return model.Auth{Type: model.AuthOAuth2, Params: params}, vars
	default:
		return model.Auth{}, nil
	}
}

func resolveOpAuth(op operation, plan authPlan) model.Auth {
	if op.Security == nil {
		return model.Auth{Type: model.AuthInherit} // uses the document default, i.e. the collection's own Auth
	}
	if len(op.Security) == 0 {
		return model.Auth{Type: model.AuthNone} // explicit `security: []` override
	}
	if auth, ok := plan.schemeAuth[op.Security[0]]; ok {
		return auth
	}
	return model.Auth{Type: model.AuthNone}
}

func buildEnvironments(nd *normalizedDoc, plan authPlan, newID func() string) []*model.Environment {
	envs := make([]*model.Environment, 0, len(nd.Servers))
	for _, s := range nd.Servers {
		values := []model.KV{{Key: "baseUrl", Value: s.URL}}
		for _, v := range plan.varNames {
			values = append(values, model.KV{Key: v, Value: "<your " + humanize(v) + ">"})
		}
		envs = append(envs, &model.Environment{ID: newID(), Name: s.Name, Values: values})
	}
	return envs
}

// humanize turns a camelCase {{var}} name into a readable placeholder
// label — "bearerToken" -> "bearer token", "oauthClientId" -> "oauth
// client id".
func humanize(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}
