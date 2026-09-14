// Package curlconv converts between a curl command line and hapidays's own
// RequestSpec — "paste a curl command from a colleague/browser devtools"
// on import, "copy as curl to hand to a colleague/CI script" on export.
//
// Import handles the shape curl commands actually come in real life:
// browser "Copy as cURL" output (backslash-continued multi-line, single-
// quoted args), not just a single-line hand-typed command. It is not a
// full curl option parser — unrecognized flags are skipped rather than
// rejected, since a pasted command may carry flags (-s, -k, --compressed,
// -L) that don't map onto anything in RequestSpec and shouldn't block the
// import over it.
package curlconv

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"hapidays/internal/client"
	"hapidays/internal/model"
)

// Parse tokenizes and interprets a curl command line into a RequestSpec.
func Parse(cmd string) (*model.RequestSpec, error) {
	tokens, err := tokenize(cmd)
	if err != nil {
		return nil, err
	}
	if len(tokens) > 0 && tokens[0] == "curl" {
		tokens = tokens[1:]
	}

	spec := &model.RequestSpec{Auth: model.Auth{Type: model.AuthNone}, Body: model.Body{Mode: model.BodyNone}}
	var rawURL, dataBody, method, user string
	var headers []model.KV
	var bodySet bool

	// booleanFlags take no value and are simply skipped — they don't map
	// onto anything RequestSpec models (verbosity, redirects, TLS
	// verification is a session-level setting here, not per-request).
	booleanFlags := map[string]bool{
		"-s": true, "--silent": true, "-S": true, "--show-error": true,
		"-k": true, "--insecure": true, "-L": true, "--location": true,
		"-v": true, "--verbose": true, "--compressed": true, "-i": true,
		"--include": true, "-#": true, "--progress-bar": true, "-G": true,
		"--get": true, "-f": true, "--fail": true, "-4": true, "-6": true,
	}

	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		next := func() string {
			i++
			if i < len(tokens) {
				return tokens[i]
			}
			return ""
		}
		switch {
		case booleanFlags[t]:
			continue
		case t == "-X" || t == "--request":
			method = strings.ToUpper(next())
		case t == "-H" || t == "--header":
			h := next()
			if k, v, ok := strings.Cut(h, ":"); ok {
				headers = append(headers, model.KV{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
			}
		case t == "-d" || t == "--data" || t == "--data-raw" || t == "--data-binary" || t == "--data-ascii":
			dataBody += next()
			bodySet = true
		case t == "--data-urlencode":
			dataBody += next() // best-effort: not re-percent-encoded, just appended
			bodySet = true
		case t == "-u" || t == "--user":
			user = next()
		case t == "-b" || t == "--cookie":
			headers = append(headers, model.KV{Key: "Cookie", Value: next()})
		case t == "-A" || t == "--user-agent":
			headers = append(headers, model.KV{Key: "User-Agent", Value: next()})
		case t == "-e" || t == "--referer":
			headers = append(headers, model.KV{Key: "Referer", Value: next()})
		case t == "--url":
			rawURL = next()
		case t == "-F" || t == "--form":
			// Best-effort form-data support: key=value only (a @filename
			// value can't be represented since curl's import can't read
			// the filesystem it was copied from).
			f := next()
			if k, v, ok := strings.Cut(f, "="); ok {
				spec.Body.Mode = model.BodyFormData
				spec.Body.FormData = append(spec.Body.FormData, model.FormField{Key: k, Value: v, Type: "text"})
			}
		case strings.HasPrefix(t, "-") && len(t) > 0:
			// An unrecognized flag. If the next token looks like its value
			// (doesn't itself start with "-"), consume it too so it isn't
			// mistaken for the URL — most curl flags we don't model
			// (--connect-timeout, --max-time, --cacert, ...) take a value.
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") && rawURL == "" && !looksLikeURL(tokens[i+1]) {
				i++
			}
		default:
			if rawURL == "" {
				rawURL = t
			}
		}
	}

	if rawURL == "" {
		return nil, fmt.Errorf("no URL found in curl command")
	}
	spec.URLRaw = rawURL
	spec.Headers = headers

	if bodySet {
		spec.Body.Mode = model.BodyRaw
		spec.Body.Raw = dataBody
		spec.Body.RawLanguage = guessLanguage(dataBody, headers)
	}

	if method != "" {
		spec.Method = method
	} else if bodySet {
		spec.Method = "POST" // curl's own default when -d is present
	} else {
		spec.Method = "GET"
	}

	if user != "" {
		username, password, _ := strings.Cut(user, ":")
		spec.Auth = model.Auth{Type: model.AuthBasic, Params: map[string]string{"username": username, "password": password}}
	}

	return spec, nil
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func guessLanguage(body string, headers []model.KV) string {
	for _, h := range headers {
		if strings.EqualFold(h.Key, "content-type") {
			ct := strings.ToLower(h.Value)
			switch {
			case strings.Contains(ct, "json"):
				return "json"
			case strings.Contains(ct, "xml"):
				return "xml"
			}
		}
	}
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return "json"
	}
	if strings.HasPrefix(trimmed, "<") {
		return "xml"
	}
	return "text"
}

// tokenize splits a curl command line the way a shell would for the
// purposes curl import needs: single- and double-quoted arguments,
// backslash escapes, and backslash-newline line continuations (the exact
// shape of Chrome/Firefox devtools' "Copy as cURL", which is the single
// most common real-world source of a pasted curl command).
func tokenize(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inToken := false
	i := 0
	runes := []rune(s)
	for i < len(runes) {
		c := runes[i]
		switch {
		case c == '\\' && i+1 < len(runes) && runes[i+1] == '\n':
			i += 2 // line continuation: drop both characters, keep tokenizing
			continue
		case c == '\'':
			inToken = true
			i++
			for i < len(runes) && runes[i] != '\'' {
				cur.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated single quote")
			}
			i++ // closing quote
		case c == '"':
			inToken = true
			i++
			for i < len(runes) && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < len(runes) {
					i++
				}
				cur.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++ // closing quote
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inToken = false
			}
			i++
		case c == '\\' && i+1 < len(runes):
			inToken = true
			cur.WriteRune(runes[i+1])
			i += 2
		default:
			inToken = true
			cur.WriteRune(c)
			i++
		}
	}
	if inToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}

// Export renders spec as a curl command, resolving {{vars}} the same way
// a real send would. collectionAuth is substituted in for AuthInherit,
// mirroring Execute's own resolution — curl has no concept of "inherit
// from collection". collectionHeaders are merged in the same way Execute
// merges them (client.MergeHeaders), so the exported command matches what
// actually gets sent.
func Export(spec model.RequestSpec, vars map[string]string, collectionAuth model.Auth, collectionHeaders []model.KV) string {
	if spec.Body.Mode == model.BodyGRPC {
		method := ""
		if spec.Body.GRPC != nil {
			method = spec.Body.GRPC.FullMethod
		}
		return "# gRPC calls aren't HTTP requests and can't be reproduced as a curl command — use grpcurl instead, e.g.:\n" +
			"# grpcurl -plaintext <target> " + method
	}

	if spec.Auth.Type == model.AuthInherit {
		spec.Auth = collectionAuth
	}
	spec.Headers = client.MergeHeaders(collectionHeaders, spec.Headers)

	rawURL := client.Resolve(spec.URLRaw, vars)
	rawURL = appendQuery(rawURL, spec.Query, vars)

	var b strings.Builder
	b.WriteString("curl")
	if spec.Method != "" && spec.Method != "GET" {
		fmt.Fprintf(&b, " -X %s", spec.Method)
	}
	fmt.Fprintf(&b, " %s", shellQuote(rawURL))

	for _, h := range spec.Headers {
		if h.Disabled {
			continue
		}
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(client.Resolve(h.Key, vars)+": "+client.Resolve(h.Value, vars)))
	}

	switch spec.Auth.Type {
	case model.AuthBasic:
		fmt.Fprintf(&b, " \\\n  -u %s", shellQuote(spec.Auth.Params["username"]+":"+spec.Auth.Params["password"]))
	case model.AuthDigest:
		fmt.Fprintf(&b, " \\\n  --digest -u %s", shellQuote(spec.Auth.Params["username"]+":"+spec.Auth.Params["password"]))
	case model.AuthBearer:
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote("Authorization: Bearer "+client.Resolve(spec.Auth.Params["token"], vars)))
	case model.AuthOAuth2:
		if tok := spec.Auth.Params["accessToken"]; tok != "" {
			fmt.Fprintf(&b, " \\\n  -H %s", shellQuote("Authorization: Bearer "+tok))
		} else {
			b.WriteString(" \\\n  # OAuth2 token not yet fetched in this session — send once in hapidays first, or set it manually above")
		}
	case model.AuthAPIKey:
		if spec.Auth.Params["in"] == "query" {
			sep := "?"
			if strings.Contains(rawURL, "?") {
				sep = "&"
			}
			// Rewritten in-place on the last line isn't practical here, so
			// just note it as an appended header-shaped comment instead —
			// the common case (header) is handled correctly below.
			_ = sep
			fmt.Fprintf(&b, " \\\n  # API key sent as query param %s=%s — add it to the URL above", spec.Auth.Params["key"], spec.Auth.Params["value"])
		} else {
			fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(spec.Auth.Params["key"]+": "+spec.Auth.Params["value"]))
		}
	case model.AuthAWSSigV4:
		b.WriteString(" \\\n  # AWS SigV4 auth can't be reproduced as a static curl command (it signs per-request with a timestamp) — use the AWS CLI's `--sign-request` or a signing proxy instead")
	}

	if ct := bodyContentType(spec.Body); ct != "" {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote("Content-Type: "+ct))
	}
	switch spec.Body.Mode {
	case model.BodyRaw, model.BodyGraphQL:
		fmt.Fprintf(&b, " \\\n  --data-raw %s", shellQuote(client.Resolve(spec.Body.Raw, vars)))
	case model.BodySoap:
		fmt.Fprintf(&b, " \\\n  --data-raw %s", shellQuote(client.Resolve(spec.Body.Raw, vars)))
		if spec.Body.SoapVersion != "1.2" {
			fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(`SOAPAction: "`+client.Resolve(spec.Body.SoapAction, vars)+`"`))
		}
		if spec.Body.WsSecurityMode != "" || spec.Body.SignBody {
			b.WriteString(" \\\n  # WS-Security UsernameToken/signing is applied by hapidays at send time and isn't reproduced in this curl command")
		}
	case model.BodyURLEncoded:
		form := url.Values{}
		for _, kv := range spec.Body.URLEncoded {
			if !kv.Disabled {
				form.Set(client.Resolve(kv.Key, vars), client.Resolve(kv.Value, vars))
			}
		}
		fmt.Fprintf(&b, " \\\n  --data %s", shellQuote(form.Encode()))
	case model.BodyFormData:
		for _, f := range spec.Body.FormData {
			if f.Disabled {
				continue
			}
			if f.Type == "file" {
				fmt.Fprintf(&b, " \\\n  -F %s", shellQuote(client.Resolve(f.Key, vars)+"=@/path/to/file"))
			} else {
				fmt.Fprintf(&b, " \\\n  -F %s", shellQuote(client.Resolve(f.Key, vars)+"="+client.Resolve(f.Value, vars)))
			}
		}
	}

	return b.String()
}

func appendQuery(rawURL string, query []model.KV, vars map[string]string) string {
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
	q, _ := url.ParseQuery(existingQuery)
	for _, kv := range enabled {
		q.Set(client.Resolve(kv.Key, vars), client.Resolve(kv.Value, vars))
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return base + "?" + q.Encode()
}

func bodyContentType(body model.Body) string {
	switch body.Mode {
	case model.BodyRaw, model.BodyGraphQL:
		switch body.RawLanguage {
		case "json":
			return "application/json"
		case "xml":
			return "application/xml"
		}
	case model.BodyURLEncoded:
		return "application/x-www-form-urlencoded"
	case model.BodySoap:
		if body.SoapVersion == "1.2" {
			ct := "application/soap+xml; charset=utf-8"
			if body.SoapAction != "" {
				ct += `; action="` + body.SoapAction + `"`
			}
			return ct
		}
		return "text/xml; charset=utf-8"
	}
	return ""
}

// shellQuote wraps s in single quotes, escaping any embedded single quote
// with the standard '\” trick — the only character that needs escaping
// inside single-quoted shell text.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
