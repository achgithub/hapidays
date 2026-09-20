// Postman-format export: the reverse of ImportCollection/ImportEnvironment
// in import.go. hapidays's own export (used for hapidays<->hapidays
// sharing) is just the native model.Collection/model.Environment JSON —
// this is specifically for producing a file Postman itself can open.
//
// Best-effort, not bit-for-bit what Postman's own export would produce:
//   - OAuth2 params are passed through under hapidays's own key names,
//     which mostly (but not entirely) line up with Postman's; Postman's
//     own grantType vocabulary ("authorization_code_with_pkce",
//     "password_credentials", ...) differs from ours in a few spots — see
//     oauth2GrantType.
//   - SOAP and gRPC have no Postman equivalent, so both export as a raw
//     body (XML for SOAP, the protojson request for gRPC) — how anyone
//     sharing a SOAP call via Postman already does it by hand, just
//     automated. WS-Security config and the gRPC call's target/method
//     metadata don't survive the round trip; only the body text does.
package importer

import (
	"encoding/json"

	"hapidays/internal/model"
)

const postmanSchemaV21 = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

type pmCollection struct {
	Info     pmInfo   `json:"info"`
	Item     []pmItem `json:"item"`
	Variable []pmKV   `json:"variable,omitempty"`
	Auth     *pmAuth  `json:"auth,omitempty"`
}

type pmInfo struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
}

type pmKV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type pmItem struct {
	Name    string     `json:"name"`
	Item    []pmItem   `json:"item,omitempty"`
	Request *pmRequest `json:"request,omitempty"`
}

type pmRequest struct {
	Method string  `json:"method,omitempty"`
	Header []pmKV  `json:"header,omitempty"`
	URL    string  `json:"url,omitempty"`
	Auth   *pmAuth `json:"auth,omitempty"`
	Body   *pmBody `json:"body,omitempty"`
}

type pmAuth struct {
	Type   string `json:"type"`
	Basic  []pmKV `json:"basic,omitempty"`
	Bearer []pmKV `json:"bearer,omitempty"`
	Digest []pmKV `json:"digest,omitempty"`
	Apikey []pmKV `json:"apikey,omitempty"`
	Awsv4  []pmKV `json:"awsv4,omitempty"`
	OAuth2 []pmKV `json:"oauth2,omitempty"`
}

type pmBody struct {
	Mode       string        `json:"mode"`
	Raw        string        `json:"raw,omitempty"`
	URLEncoded []pmKV        `json:"urlencoded,omitempty"`
	FormData   []pmFormField `json:"formdata,omitempty"`
	GraphQL    *pmGraphQL    `json:"graphql,omitempty"`
	Options    *pmRawOptions `json:"options,omitempty"`
}

type pmFormField struct {
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	Type     string `json:"type,omitempty"`
	Src      string `json:"src,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type pmGraphQL struct {
	Query     string `json:"query"`
	Variables string `json:"variables,omitempty"`
}

type pmRawOptions struct {
	Raw pmRawLanguage `json:"raw"`
}

type pmRawLanguage struct {
	Language string `json:"language,omitempty"`
}

// ExportCollection renders col in Postman Collection Format v2.1 — the
// shape learning.postman.com/collection-format documents and Postman's own
// importer reads.
func ExportCollection(col *model.Collection) ([]byte, error) {
	pc := pmCollection{
		Info:     pmInfo{Name: col.Name, Schema: postmanSchemaV21},
		Item:     exportNodes(col.Root),
		Variable: exportKVs(col.Variables),
		Auth:     exportAuth(col.Auth),
	}
	// Postman has no separate collection-level "headers sent by default"
	// concept the way hapidays does — fold them into Variables' sibling,
	// collection Variable list, isn't right either (they're headers, not
	// vars). There's genuinely no lossless home for them in this schema;
	// they're dropped, same as WS-Security config and gRPC metadata.
	return json.MarshalIndent(pc, "", "  ")
}

func exportNodes(nodes []*model.Node) []pmItem {
	items := make([]pmItem, 0, len(nodes))
	for _, n := range nodes {
		if n.Children != nil {
			items = append(items, pmItem{Name: n.Name, Item: exportNodes(n.Children)})
			continue
		}
		items = append(items, pmItem{Name: n.Name, Request: exportRequest(n.Request)})
	}
	return items
}

func exportRequest(r *model.RequestSpec) *pmRequest {
	if r == nil {
		return &pmRequest{}
	}
	req := &pmRequest{
		Method: r.Method,
		Header: exportKVs(r.Headers),
		URL:    r.URLRaw,
		Body:   exportBody(r.Body),
	}
	// AuthInherit has no Postman equivalent to set — Postman inherits
	// automatically from the parent whenever an item's own "auth" key is
	// simply absent, which is exactly the behavior we want, so leave
	// req.Auth nil rather than encode a type Postman doesn't know.
	if r.Auth.Type != model.AuthInherit {
		req.Auth = exportAuth(r.Auth)
	}
	return req
}

func exportAuth(auth model.Auth) *pmAuth {
	switch auth.Type {
	case model.AuthBasic:
		return &pmAuth{Type: "basic", Basic: paramKVs(auth.Params, "username", "password")}
	case model.AuthBearer:
		return &pmAuth{Type: "bearer", Bearer: paramKVs(auth.Params, "token")}
	case model.AuthDigest:
		return &pmAuth{Type: "digest", Digest: paramKVs(auth.Params, "username", "password")}
	case model.AuthAPIKey:
		return &pmAuth{Type: "apikey", Apikey: paramKVs(auth.Params, "key", "value", "in")}
	case model.AuthAWSSigV4:
		return &pmAuth{Type: "awsv4", Awsv4: paramKVs(auth.Params, "accessKey", "secretKey", "region", "service", "sessionToken")}
	case model.AuthOAuth2:
		return &pmAuth{Type: "oauth2", OAuth2: oauth2Params(auth.Params)}
	default:
		return &pmAuth{Type: "noauth"}
	}
}

// paramKVs pulls the given keys out of params (skipping ones that are
// empty/absent) into Postman's "array of {key,value,type:string}" auth
// param encoding, in a fixed order — Postman doesn't care about order, but
// a stable one makes exported files diff cleanly.
func paramKVs(params map[string]string, keys ...string) []pmKV {
	var out []pmKV
	for _, k := range keys {
		if v, ok := params[k]; ok && v != "" {
			out = append(out, pmKV{Key: k, Value: v, Type: "string"})
		}
	}
	return out
}

// oauth2GrantType maps hapidays's grant-type strings to Postman's own
// vocabulary where the two differ; passed through unchanged otherwise.
func oauth2GrantType(g string) string {
	if g == "password" {
		return "password_credentials" // Postman's name for RFC 6749 §4.3
	}
	return g
}

func oauth2Params(params map[string]string) []pmKV {
	keys := []string{"grantType", "accessTokenUrl", "authUrl", "clientId", "clientSecret", "username", "password", "scope", "accessToken"}
	var out []pmKV
	for _, k := range keys {
		v := params[k]
		if v == "" {
			continue
		}
		outKey := k
		if k == "grantType" {
			v = oauth2GrantType(v)
			outKey = "grant_type"
		}
		out = append(out, pmKV{Key: outKey, Value: v, Type: "string"})
	}
	return out
}

func exportKVs(kvs []model.KV) []pmKV {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]pmKV, len(kvs))
	for i, kv := range kvs {
		out[i] = pmKV{Key: kv.Key, Value: kv.Value, Disabled: kv.Disabled}
	}
	return out
}

func exportBody(b model.Body) *pmBody {
	switch b.Mode {
	case model.BodyNone:
		return nil
	case model.BodyURLEncoded:
		return &pmBody{Mode: "urlencoded", URLEncoded: exportKVs(b.URLEncoded)}
	case model.BodyFormData:
		fields := make([]pmFormField, len(b.FormData))
		for i, f := range b.FormData {
			fields[i] = pmFormField{Key: f.Key, Value: f.Value, Type: f.Type, Src: f.Src, Disabled: f.Disabled}
		}
		return &pmBody{Mode: "formdata", FormData: fields}
	case model.BodyGraphQL:
		if b.GraphQLQuery != "" {
			return &pmBody{Mode: "graphql", GraphQL: &pmGraphQL{Query: b.GraphQLQuery, Variables: b.GraphQLVariables}}
		}
		// Pre-split collections stored the already-assembled
		// {"query":...,"variables":...} JSON POST payload directly in Raw
		// (see buildBody's back-compat path) — split it back out so it
		// still lands in Postman's native graphql mode instead of raw JSON.
		var parsed struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables,omitempty"`
		}
		if json.Unmarshal([]byte(b.Raw), &parsed) == nil && parsed.Query != "" {
			gql := &pmGraphQL{Query: parsed.Query}
			if len(parsed.Variables) > 0 {
				gql.Variables = string(parsed.Variables)
			}
			return &pmBody{Mode: "graphql", GraphQL: gql}
		}
		return &pmBody{Mode: "raw", Raw: b.Raw, Options: &pmRawOptions{Raw: pmRawLanguage{Language: "json"}}}
	case model.BodySoap:
		return &pmBody{Mode: "raw", Raw: b.Raw, Options: &pmRawOptions{Raw: pmRawLanguage{Language: "xml"}}}
	case model.BodyGRPC:
		raw := ""
		if b.GRPC != nil {
			raw = b.GRPC.RequestJSON
		}
		return &pmBody{Mode: "raw", Raw: raw, Options: &pmRawOptions{Raw: pmRawLanguage{Language: "json"}}}
	default: // raw
		lang := b.RawLanguage
		if lang == "" {
			lang = "text"
		}
		return &pmBody{Mode: "raw", Raw: b.Raw, Options: &pmRawOptions{Raw: pmRawLanguage{Language: lang}}}
	}
}
