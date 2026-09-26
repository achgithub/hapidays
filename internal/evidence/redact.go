// Package evidence turns executed requests into a saved, shareable test
// evidence pack: builds it from client results, redacts credentials, and
// renders it as a self-contained HTML report.
package evidence

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Redaction works in three layers, because a credential reaches an evidence
// file by more than one route: by the header it travels in (Authorization,
// Cookie), by the field it is stored under in a body or query string
// (password, access_token), and by simply being the resolved value of a
// secret variable that got substituted into a URL or body. Any one layer
// alone leaves gaps — e.g. the client secret in a credentials POST is caught
// by field name, but the same secret pasted into a free-text field is only
// caught by value.
//
// Each secret is replaced by a short fingerprint, not blanked, so a reviewer
// can still see that the CSRF token fetched in one step is the very one sent
// in the next, without seeing it.

var (
	// A secret-looking name. Deliberately not just "key"/"auth", which would
	// swallow ordinary fields (a partner key, "SAPAuthenticatedUserName").
	secretNameWords = []string{"secret", "password", "passwd", "pwd", "token", "apikey", "api_key", "api-key", "credential", "privatekey", "private_key", "authorization"}
	// Names that contain a secret word but describe something else.
	notSecretWords = []string{"type", "expires", "url", "uri", "host", "endpoint", "count", "length"}

	jsonPairRe   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"(\s*:\s*)"((?:[^"\\]|\\.)*)"`)
	formPairRe   = regexp.MustCompile(`(^|&)([^=&\s]+)=([^&]*)`)
	xmlElementRe = regexp.MustCompile(`(<(?:[\w.-]+:)?([\w.-]+)(?:\s[^>]*)?>)([^<]+)(</)`)
	queryPairRe  = regexp.MustCompile(`([?&])([^=&#]+)=([^&#]*)`)
	userinfoRe   = regexp.MustCompile(`(://[^/@:\s]+:)([^@/\s]+)(@)`)
)

func isSecretName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range notSecretWords {
		if strings.Contains(n, w) {
			return false
		}
	}
	for _, w := range secretNameWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Headers redacted by name whatever their value looks like.
var secretHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true,
	"cookie": true, "set-cookie": true,
	"x-csrf-token": true, "x-xsrf-token": true,
	"x-api-key": true, "api-key": true, "apikey": true,
}

func isSecretHeader(name string) bool {
	n := strings.ToLower(name)
	return secretHeaders[n] || isSecretName(n)
}

// Marker stands in for a redacted value. The same value always gives the
// same marker, so equal secrets can still be recognised as equal.
func Marker(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "[REDACTED:" + hex.EncodeToString(sum[:])[:6] + "]"
}

// minSecretLen keeps short, common values ("true", "1", an empty CSRF token)
// out of the by-value replacement: replacing them would corrupt unrelated
// text, and an empty string would match everywhere.
const minSecretLen = 6

// Redactor replaces credentials in the strings it is given.
type Redactor struct {
	values []string // longest first, so a secret containing another is replaced whole
}

// NewRedactor builds a Redactor from the variables in scope (their values
// are secrets when their names say so) plus any extra known secret values.
func NewRedactor(vars map[string]string, extra ...string) *Redactor {
	set := map[string]bool{}
	add := func(v string) {
		if len(v) < minSecretLen {
			return
		}
		set[v] = true
		if q := url.QueryEscape(v); q != v {
			set[q] = true
		}
		if q := url.PathEscape(v); q != v {
			set[q] = true
		}
	}
	for name, v := range vars {
		if isSecretName(name) {
			add(v)
		}
	}
	for _, v := range extra {
		add(v)
	}
	r := &Redactor{}
	for v := range set {
		r.values = append(r.values, v)
	}
	sort.Slice(r.values, func(i, j int) bool {
		if len(r.values[i]) != len(r.values[j]) {
			return len(r.values[i]) > len(r.values[j])
		}
		return r.values[i] < r.values[j]
	})
	return r
}

// Text replaces every known secret value in s.
func (r *Redactor) Text(s string) string {
	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Marker(v))
		}
	}
	return s
}

// SecretsInAuthHeader pulls the secret parts out of an Authorization header
// value, so they can also be found elsewhere (e.g. the same password inside
// a request body) even when the request used a literal credential rather
// than a variable.
func SecretsInAuthHeader(value string) []string {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok {
		return nil
	}
	rest = strings.TrimSpace(rest)
	out := []string{rest}
	if strings.EqualFold(scheme, "basic") {
		if dec, err := base64.StdEncoding.DecodeString(rest); err == nil {
			out = append(out, string(dec))
			if _, pass, ok := strings.Cut(string(dec), ":"); ok {
				out = append(out, pass)
			}
		}
	}
	return out
}

// Header redacts one header's values.
func (r *Redactor) Header(name string, values []string) []string {
	out := make([]string, len(values))
	lname := strings.ToLower(name)
	for i, v := range values {
		switch {
		case lname == "authorization" || lname == "proxy-authorization":
			if scheme, rest, ok := strings.Cut(strings.TrimSpace(v), " "); ok {
				out[i] = scheme + " " + Marker(strings.TrimSpace(rest))
			} else {
				out[i] = Marker(v)
			}
		case lname == "cookie":
			out[i] = redactCookieHeader(v)
		case lname == "set-cookie":
			out[i] = redactSetCookie(v)
		case isSecretHeader(name):
			out[i] = Marker(v)
		default:
			out[i] = r.Text(v)
		}
	}
	return out
}

// Headers redacts a whole header map.
func (r *Redactor) Headers(h map[string][]string) map[string][]string {
	if h == nil {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = r.Header(k, v)
	}
	return out
}

// Keep the cookie names (a reviewer wants to see a session cookie was sent)
// but not their values.
func redactCookieHeader(v string) string {
	parts := strings.Split(v, ";")
	for i, p := range parts {
		name, val, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok {
			parts[i] = name + "=" + Marker(val)
		}
	}
	return strings.Join(parts, "; ")
}

func redactSetCookie(v string) string {
	first, rest, hasRest := strings.Cut(v, ";")
	name, val, ok := strings.Cut(strings.TrimSpace(first), "=")
	if !ok {
		return v
	}
	out := name + "=" + Marker(val)
	if hasRest {
		out += ";" + rest
	}
	return out
}

// URL redacts credentials embedded in a URL: user:password@, secret-named
// query parameters, and any known secret value.
func (r *Redactor) URL(u string) string {
	u = userinfoRe.ReplaceAllStringFunc(u, func(m string) string {
		g := userinfoRe.FindStringSubmatch(m)
		return g[1] + Marker(g[2]) + g[3]
	})
	u = queryPairRe.ReplaceAllStringFunc(u, func(m string) string {
		g := queryPairRe.FindStringSubmatch(m)
		if isSecretName(g[2]) && g[3] != "" {
			return g[1] + g[2] + "=" + Marker(g[3])
		}
		return m
	})
	return r.Text(u)
}

// Body redacts a text body: JSON string fields, form fields and XML element
// text whose names look secret, then any known secret value anywhere.
func (r *Redactor) Body(body string) string {
	if body == "" {
		return body
	}
	body = jsonPairRe.ReplaceAllStringFunc(body, func(m string) string {
		g := jsonPairRe.FindStringSubmatch(m)
		if isSecretName(g[1]) && g[3] != "" {
			return `"` + g[1] + `"` + g[2] + `"` + Marker(g[3]) + `"`
		}
		return m
	})
	if !strings.ContainsAny(body, "<{[") { // looks like a form body, not markup or JSON
		body = formPairRe.ReplaceAllStringFunc(body, func(m string) string {
			g := formPairRe.FindStringSubmatch(m)
			if isSecretName(g[2]) && g[3] != "" {
				return g[1] + g[2] + "=" + Marker(g[3])
			}
			return m
		})
	}
	body = xmlElementRe.ReplaceAllStringFunc(body, func(m string) string {
		g := xmlElementRe.FindStringSubmatch(m)
		if isSecretName(g[2]) && strings.TrimSpace(g[3]) != "" {
			return g[1] + Marker(strings.TrimSpace(g[3])) + g[4]
		}
		return m
	})
	return r.Text(body)
}
