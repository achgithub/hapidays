// Package postman imports Postman Collection v2.0/v2.1 exports and
// Postman environment exports into pmclone's normalized internal/model
// types.
//
// Postman's JSON is polymorphic in several places (see the raw* types
// below) so this file parses into loose intermediate structs first, then
// normalizes. Deliberately NOT handled: pm.* pre-request/test scripts are
// parsed and kept verbatim on the RequestSpec (HasScript=true) but never
// executed — there is no JS engine here. The one script pattern that
// matters in practice, SAP/OData's "X-CSRF-Token: Fetch" + capture, is
// instead covered by model.Capture, which the UI can set up per-request
// without running arbitrary JS.
package postman

import (
	"encoding/json"
	"fmt"
	"strings"

	"pmclone/internal/model"
)

// ---- raw (on-disk) shapes ----

type rawCollection struct {
	Info struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"info"`
	Item      []rawItem     `json:"item"`
	Variable  []rawKV       `json:"variable"`
	Auth      *rawAuth      `json:"auth"`
}

type rawItem struct {
	Name    string      `json:"name"`
	Item    []rawItem   `json:"item"` // present => folder
	Request *rawRequest `json:"request"`
	Event   []rawEvent  `json:"event"`
}

type rawRequest struct {
	Method string      `json:"method"`
	Header rawHeaders  `json:"header"`
	URL    rawURL      `json:"url"`
	Auth   *rawAuth    `json:"auth"`
	Body   *rawBody    `json:"body"`
}

// rawHeaders: array of {key,value,disabled} OR legacy newline-delimited string.
type rawHeaders []rawKV

func (h *rawHeaders) UnmarshalJSON(b []byte) error {
	var asArray []rawKV
	if err := json.Unmarshal(b, &asArray); err == nil {
		*h = asArray
		return nil
	}
	var asString string
	if err := json.Unmarshal(b, &asString); err == nil {
		for _, line := range strings.Split(asString, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			kv := rawKV{Key: strings.TrimSpace(parts[0])}
			if len(parts) == 2 {
				kv.Value = strings.TrimSpace(parts[1])
			}
			*h = append(*h, kv)
		}
		return nil
	}
	return fmt.Errorf("header: unsupported shape %s", string(b))
}

type rawKV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
	Type     string `json:"type"` // used inside auth param arrays
}

// rawURL: either a bare string or an object with host/path as arrays.
type rawURL struct {
	Raw   string
	Query []rawKV
}

func (u *rawURL) UnmarshalJSON(b []byte) error {
	var asString string
	if err := json.Unmarshal(b, &asString); err == nil {
		u.Raw = asString
		return nil
	}
	var obj struct {
		Raw   string  `json:"raw"`
		Host  any     `json:"host"`
		Path  any     `json:"path"`
		Query []rawKV `json:"query"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("url: unsupported shape: %w", err)
	}
	u.Raw = obj.Raw
	u.Query = obj.Query
	if u.Raw == "" {
		// Reconstruct from host/path arrays (rare, but seen in older exports).
		u.Raw = joinURLParts(obj.Host) + "/" + joinURLParts(obj.Path)
	}
	return nil
}

func joinURLParts(v any) string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, p := range arr {
		if s, ok := p.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "/")
}

// rawAuth: type + a params array (v2.1) or object (older exports) per auth type.
type rawAuth struct {
	Type   string
	Params map[string]string
}

func (a *rawAuth) UnmarshalJSON(b []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if t, ok := probe["type"]; ok {
		_ = json.Unmarshal(t, &a.Type)
	}
	a.Params = map[string]string{}
	raw, ok := probe[a.Type]
	if !ok {
		return nil
	}
	var asArray []rawKV
	if err := json.Unmarshal(raw, &asArray); err == nil {
		for _, kv := range asArray {
			a.Params[kv.Key] = kv.Value
		}
		return nil
	}
	var asObject map[string]string
	if err := json.Unmarshal(raw, &asObject); err == nil {
		for k, v := range asObject {
			a.Params[k] = v
		}
	}
	return nil
}

type rawBody struct {
	Mode       string `json:"mode"`
	Raw        string `json:"raw"`
	URLEncoded []rawKV `json:"urlencoded"`
	FormData   []struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		Type     string `json:"type"`
		Disabled bool   `json:"disabled"`
	} `json:"formdata"`
	Options struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

type rawEvent struct {
	Listen string `json:"listen"` // "prerequest" | "test"
	Script struct {
		Exec rawExec `json:"exec"`
	} `json:"script"`
}

// rawExec: array of lines (normal) or a single string (rare).
type rawExec []string

func (e *rawExec) UnmarshalJSON(b []byte) error {
	var asArray []string
	if err := json.Unmarshal(b, &asArray); err == nil {
		*e = asArray
		return nil
	}
	var asString string
	if err := json.Unmarshal(b, &asString); err == nil {
		*e = []string{asString}
		return nil
	}
	return fmt.Errorf("script.exec: unsupported shape")
}

// ---- environment ----

type rawEnvironment struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Values []struct {
		Key     string `json:"key"`
		Value   string `json:"value"`
		Enabled bool   `json:"enabled"`
	} `json:"values"`
}

// ---- public API ----

func ImportCollection(data []byte, newID func() string) (*model.Collection, error) {
	var rc rawCollection
	if err := json.Unmarshal(data, &rc); err != nil {
		return nil, fmt.Errorf("parse collection: %w", err)
	}
	col := &model.Collection{
		ID:   newID(),
		Name: rc.Info.Name,
	}
	for _, v := range rc.Variable {
		col.Variables = append(col.Variables, model.KV{Key: v.Key, Value: v.Value, Disabled: v.Disabled})
	}
	if rc.Auth != nil {
		col.Auth = model.Auth{Type: model.AuthType(rc.Auth.Type), Params: rc.Auth.Params}
	}
	col.Root = convertItems(rc.Item, newID)
	return col, nil
}

func convertItems(items []rawItem, newID func() string) []*model.Node {
	nodes := make([]*model.Node, 0, len(items))
	for _, it := range items {
		node := &model.Node{ID: newID(), Name: it.Name}
		if it.Request != nil {
			node.Request = convertRequest(*it.Request, it.Event)
		} else {
			node.Children = convertItems(it.Item, newID)
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func convertRequest(r rawRequest, events []rawEvent) *model.RequestSpec {
	// Postman's own export duplicates query params: once inline in
	// url.raw, again in url.query. Keep only the latter — URLRaw here is
	// the base URL with no query string, matching how the app treats
	// Query as the sole source of query params (applyQueryParams merges
	// it in at send time).
	urlRaw, _, _ := strings.Cut(r.URL.Raw, "?")
	spec := &model.RequestSpec{
		Method: r.Method,
		URLRaw: urlRaw,
	}
	for _, q := range r.URL.Query {
		spec.Query = append(spec.Query, model.KV{Key: q.Key, Value: q.Value, Disabled: q.Disabled})
	}
	for _, h := range r.Header {
		spec.Headers = append(spec.Headers, model.KV{Key: h.Key, Value: h.Value, Disabled: h.Disabled})
	}
	if r.Auth != nil {
		spec.Auth = model.Auth{Type: model.AuthType(r.Auth.Type), Params: r.Auth.Params}
	} else {
		spec.Auth = model.Auth{Type: model.AuthInherit}
	}
	if r.Body != nil {
		spec.Body = convertBody(*r.Body)
	} else {
		spec.Body = model.Body{Mode: model.BodyNone}
	}
	for _, ev := range events {
		script := strings.Join(ev.Script.Exec, "\n")
		if script == "" {
			continue
		}
		spec.HasScript = true
		if ev.Listen == "prerequest" {
			spec.PreRequestScript = script
		} else if ev.Listen == "test" {
			spec.TestScript = script
		}
	}
	return spec
}

func convertBody(b rawBody) model.Body {
	body := model.Body{Mode: model.BodyMode(b.Mode)}
	if body.Mode == "" {
		body.Mode = model.BodyNone
	}
	switch body.Mode {
	case model.BodyRaw, model.BodyGraphQL:
		body.Raw = b.Raw
		body.RawLanguage = b.Options.Raw.Language
	case model.BodyURLEncoded:
		for _, kv := range b.URLEncoded {
			body.URLEncoded = append(body.URLEncoded, model.KV{Key: kv.Key, Value: kv.Value, Disabled: kv.Disabled})
		}
	case model.BodyFormData:
		for _, f := range b.FormData {
			body.FormData = append(body.FormData, model.FormField{Key: f.Key, Value: f.Value, Type: f.Type, Disabled: f.Disabled})
		}
	}
	return body
}

func ImportEnvironment(data []byte, newID func() string) (*model.Environment, error) {
	var re rawEnvironment
	if err := json.Unmarshal(data, &re); err != nil {
		return nil, fmt.Errorf("parse environment: %w", err)
	}
	env := &model.Environment{ID: newID(), Name: re.Name}
	for _, v := range re.Values {
		env.Values = append(env.Values, model.KV{Key: v.Key, Value: v.Value, Disabled: !v.Enabled})
	}
	return env, nil
}
