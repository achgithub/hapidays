package openapi

import (
	"fmt"
	"strings"
)

// parseV3 normalizes an OpenAPI 3.0/3.1 document (already decoded into
// map[string]any) into a normalizedDoc.
func parseV3(doc map[string]any) (*normalizedDoc, error) {
	nd := &normalizedDoc{root: doc, Schemes: map[string]securityScheme{}}

	if info, ok := doc["info"].(map[string]any); ok {
		if t, ok := info["title"].(string); ok {
			nd.Title = t
		}
	}

	if serversRaw, ok := doc["servers"].([]any); ok {
		for i, s := range serversRaw {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			url, _ := sm["url"].(string)
			name := fmt.Sprintf("Server %d", i+1)
			if desc, ok := sm["description"].(string); ok && desc != "" {
				name = desc
			}
			nd.Servers = append(nd.Servers, server{Name: name, URL: substituteServerVars(url, sm)})
		}
	}
	if len(nd.Servers) == 0 {
		nd.Servers = []server{{Name: "Server"}}
	}

	if comps, ok := doc["components"].(map[string]any); ok {
		if schemesRaw, ok := comps["securitySchemes"].(map[string]any); ok {
			for _, name := range sortedKeys(schemesRaw) {
				sm, ok := schemesRaw[name].(map[string]any)
				if !ok {
					continue
				}
				nd.Schemes[name] = parseV3SecurityScheme(sm)
			}
		}
	}

	nd.Security = securityNames(doc["security"])

	pathsRaw, _ := doc["paths"].(map[string]any)
	for _, path := range sortedKeys(pathsRaw) {
		item, ok := pathsRaw[path].(map[string]any)
		if !ok {
			continue
		}
		var shared []opParam
		if pRaw, ok := item["parameters"].([]any); ok {
			shared = parseV3Params(pRaw)
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
			var own []opParam
			if pRaw, ok := opRaw["parameters"].([]any); ok {
				own = parseV3Params(pRaw)
			}
			op.Params = append(append([]opParam{}, shared...), own...)
			if rb, ok := opRaw["requestBody"].(map[string]any); ok {
				op.RequestBody = parseV3RequestBody(rb)
			}
			if secRaw, present := opRaw["security"]; present {
				op.Security = securityNames(secRaw)
			}
			nd.Operations = append(nd.Operations, op)
		}
	}
	return nd, nil
}

// substituteServerVars replaces {varName} placeholders in a server URL
// with that variable's declared default — the only sensible value to pick
// without asking the user, same reasoning as every other placeholder this
// importer generates.
func substituteServerVars(url string, serverObj map[string]any) string {
	vars, ok := serverObj["variables"].(map[string]any)
	if !ok {
		return url
	}
	for name, vRaw := range vars {
		vm, ok := vRaw.(map[string]any)
		if !ok {
			continue
		}
		def, _ := vm["default"].(string)
		url = strings.ReplaceAll(url, "{"+name+"}", def)
	}
	return url
}

func parseV3Params(raw any) []opParam {
	list, _ := raw.([]any)
	var out []opParam
	for _, pRaw := range list {
		pm, ok := pRaw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := pm["name"].(string)
		in, _ := pm["in"].(string)
		if in != "path" && in != "query" && in != "header" {
			continue // "cookie" params aren't modeled anywhere in RequestSpec — dropped, not guessed at
		}
		required, _ := pm["required"].(bool)
		schema, _ := pm["schema"].(map[string]any)
		out = append(out, opParam{Name: name, In: in, Required: required, Schema: schema})
	}
	return out
}

func parseV3RequestBody(rb map[string]any) *requestBody {
	content, ok := rb["content"].(map[string]any)
	if !ok || len(content) == 0 {
		return nil
	}
	ct := pickContentType(content)
	entry, _ := content[ct].(map[string]any)
	schema, _ := entry["schema"].(map[string]any)
	return &requestBody{ContentType: ct, Schema: schema}
}

// pickContentType prefers JSON, then form encodings, then whatever's first
// alphabetically — a generated example body is far more useful in the
// common JSON case, and falling back to a deterministic pick beats a
// random one for anything else.
func pickContentType(content map[string]any) string {
	for _, preferred := range []string{"application/json", "application/x-www-form-urlencoded", "multipart/form-data"} {
		if _, ok := content[preferred]; ok {
			return preferred
		}
	}
	keys := sortedKeys(content)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func parseV3SecurityScheme(sm map[string]any) securityScheme {
	scheme := securityScheme{}
	scheme.Type, _ = sm["type"].(string)
	scheme.Scheme, _ = sm["scheme"].(string)
	scheme.In, _ = sm["in"].(string)
	scheme.ParamName, _ = sm["name"].(string)
	scheme.OpenIDConnectURL, _ = sm["openIdConnectUrl"].(string)

	flows, ok := sm["flows"].(map[string]any)
	if !ok {
		return scheme
	}
	// Prefer clientCredentials/authorizationCode over the others when a
	// scheme (unusually) offers more than one — hapidays's OAuth2 tab can
	// only represent one flow at a time, and those two are the ones an API
	// client actually drives end-to-end itself (implicit and password are
	// both discouraged/deprecated in current OAuth guidance).
	for _, flowName := range []string{"clientCredentials", "authorizationCode", "password", "implicit"} {
		fRaw, ok := flows[flowName].(map[string]any)
		if !ok {
			continue
		}
		grantType := map[string]string{
			"clientCredentials": "client_credentials",
			"authorizationCode": "authorization_code",
			"password":          "password",
			"implicit":          "implicit", // hapidays has no implicit-grant UI; surfaced anyway so it's not silently dropped
		}[flowName]
		flow := &oauthFlow{GrantType: grantType}
		flow.AuthURL, _ = fRaw["authorizationUrl"].(string)
		flow.TokenURL, _ = fRaw["tokenUrl"].(string)
		if scopes, ok := fRaw["scopes"].(map[string]any); ok {
			flow.Scopes = sortedKeys(scopes)
		}
		scheme.Flow = flow
		break
	}
	return scheme
}
