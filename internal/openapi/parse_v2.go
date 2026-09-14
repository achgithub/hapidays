package openapi

import (
	"strings"
)

// parseV2 normalizes a Swagger 2.0 document into a normalizedDoc — same
// output shape as parseV3, different input shape: no servers[] (a single
// scheme+host+basePath instead), $ref points at #/definitions/... instead
// of #/components/schemas/..., and a request body arrives as an in:"body"
// or in:"formData" parameter rather than its own requestBody object.
func parseV2(doc map[string]any) (*normalizedDoc, error) {
	nd := &normalizedDoc{root: doc, Schemes: map[string]securityScheme{}}

	if info, ok := doc["info"].(map[string]any); ok {
		if t, ok := info["title"].(string); ok {
			nd.Title = t
		}
	}

	nd.Servers = []server{{Name: "Server", URL: v2BaseURL(doc)}}

	if defsRaw, ok := doc["securityDefinitions"].(map[string]any); ok {
		for _, name := range sortedKeys(defsRaw) {
			sm, ok := defsRaw[name].(map[string]any)
			if !ok {
				continue
			}
			nd.Schemes[name] = parseV2SecurityScheme(sm)
		}
	}

	nd.Security = securityNames(doc["security"])

	consumes, _ := doc["consumes"].([]any)
	defaultConsumes := firstString(consumes)

	pathsRaw, _ := doc["paths"].(map[string]any)
	for _, path := range sortedKeys(pathsRaw) {
		item, ok := pathsRaw[path].(map[string]any)
		if !ok {
			continue
		}
		var shared []any
		if pRaw, ok := item["parameters"].([]any); ok {
			shared = pRaw
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head", "options"} {
			opRaw, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			op := operation{Method: strings.ToUpper(method), Path: path}
			if id, ok := opRaw["operationId"].(string); ok {
				op.OperationID = id
			}
			if tags, ok := opRaw["tags"].([]any); ok && len(tags) > 0 {
				if t, ok := tags[0].(string); ok {
					op.Tag = t
				}
			}
			opConsumes := defaultConsumes
			if c, ok := opRaw["consumes"].([]any); ok {
				opConsumes = firstString(c)
			}
			var own []any
			if pRaw, ok := opRaw["parameters"].([]any); ok {
				own = pRaw
			}
			op.Params, op.RequestBody = parseV2Params(append(append([]any{}, shared...), own...), opConsumes)
			if secRaw, present := opRaw["security"]; present {
				op.Security = securityNames(secRaw)
			}
			nd.Operations = append(nd.Operations, op)
		}
	}
	return nd, nil
}

func v2BaseURL(doc map[string]any) string {
	host, _ := doc["host"].(string)
	if host == "" {
		return ""
	}
	basePath, _ := doc["basePath"].(string)
	scheme := "https"
	if schemesRaw, ok := doc["schemes"].([]any); ok {
		found := false
		for _, s := range schemesRaw {
			if s == "https" {
				found = true
			}
		}
		if !found {
			if s := firstString(schemesRaw); s != "" {
				scheme = s
			}
		}
	}
	return scheme + "://" + host + basePath
}

func firstString(list []any) string {
	for _, v := range list {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// parseV2Params splits Swagger 2.0's flat parameter list into
// path/query/header params plus (at most one) request body — v2 has no
// separate requestBody object; a body arrives as a single in:"body"
// parameter (schema is the body schema directly) or one-or-more
// in:"formData" parameters (each its own field, assembled into one object
// schema) depending on the operation's consumes type.
func parseV2Params(raw []any, consumes string) ([]opParam, *requestBody) {
	var params []opParam
	var rb *requestBody
	formProps := map[string]any{}
	var formRequired []any
	for _, pRaw := range raw {
		pm, ok := pRaw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := pm["name"].(string)
		in, _ := pm["in"].(string)
		required, _ := pm["required"].(bool)
		switch in {
		case "path", "query", "header":
			// v2 params carry their type/format/enum directly (no nested
			// "schema"), unlike v3 — build a schema object schemaPlaceholder
			// already knows how to read out of the same fields.
			schema := map[string]any{}
			for _, k := range []string{"type", "format", "enum", "default", "items"} {
				if v, ok := pm[k]; ok {
					schema[k] = v
				}
			}
			params = append(params, opParam{Name: name, In: in, Required: required, Schema: schema})
		case "body":
			schema, _ := pm["schema"].(map[string]any)
			ct := consumes
			if ct == "" {
				ct = "application/json"
			}
			rb = &requestBody{ContentType: ct, Schema: schema}
		case "formData":
			schema := map[string]any{}
			for _, k := range []string{"type", "format", "enum", "default"} {
				if v, ok := pm[k]; ok {
					schema[k] = v
				}
			}
			formProps[name] = schema
			if required {
				formRequired = append(formRequired, name)
			}
		}
	}
	if len(formProps) > 0 {
		ct := consumes
		if ct == "" {
			ct = "application/x-www-form-urlencoded"
		}
		rb = &requestBody{ContentType: ct, Schema: map[string]any{
			"type": "object", "properties": formProps, "required": formRequired,
		}}
	}
	return params, rb
}

// parseV2SecurityScheme maps Swagger 2.0's securityDefinitions shape
// (type: basic|apiKey|oauth2, oauth2's flow given directly rather than
// nested under a flows map, and its own flow-name vocabulary) onto the
// same securityScheme shape parseV3SecurityScheme produces.
func parseV2SecurityScheme(sm map[string]any) securityScheme {
	scheme := securityScheme{}
	typ, _ := sm["type"].(string)
	switch typ {
	case "basic":
		scheme.Type = "http"
		scheme.Scheme = "basic"
	case "apiKey":
		scheme.Type = "apiKey"
		scheme.In, _ = sm["in"].(string)
		scheme.ParamName, _ = sm["name"].(string)
	case "oauth2":
		scheme.Type = "oauth2"
		flowName, _ := sm["flow"].(string)
		grantType := map[string]string{
			"application": "client_credentials",
			"accessCode":  "authorization_code",
			"password":    "password",
			"implicit":    "implicit",
		}[flowName]
		if grantType != "" {
			flow := &oauthFlow{GrantType: grantType}
			flow.AuthURL, _ = sm["authorizationUrl"].(string)
			flow.TokenURL, _ = sm["tokenUrl"].(string)
			if scopes, ok := sm["scopes"].(map[string]any); ok {
				flow.Scopes = sortedKeys(scopes)
			}
			scheme.Flow = flow
		}
	}
	return scheme
}
