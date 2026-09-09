package client

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// applyDigestAuth parses a WWW-Authenticate: Digest challenge and sets the
// Authorization header for a retry, per RFC 7616 (qop=auth only — the
// common case; auth-int, which digests the body too, isn't implemented).
// Returns false if the challenge isn't a Digest one it can handle, in
// which case Execute just returns the original 401 response.
func applyDigestAuth(req *http.Request, challenge string, params map[string]string, vars map[string]string) bool {
	if !strings.HasPrefix(strings.TrimSpace(challenge), "Digest ") {
		return false
	}
	directives := parseDigestChallenge(challenge)
	realm := directives["realm"]
	nonce := directives["nonce"]
	if nonce == "" {
		return false
	}
	qop := directives["qop"]
	opaque := directives["opaque"]

	username := Resolve(params["username"], vars)
	password := Resolve(params["password"], vars)
	cnonce := randomHex(8)
	nc := "00000001"

	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(req.Method + ":" + req.URL.RequestURI())

	var response string
	if qop != "" {
		response = md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}

	parts := []string{
		`username="` + username + `"`,
		`realm="` + realm + `"`,
		`nonce="` + nonce + `"`,
		`uri="` + req.URL.RequestURI() + `"`,
		`response="` + response + `"`,
	}
	if qop != "" {
		parts = append(parts, `qop=`+qop, `nc=`+nc, `cnonce="`+cnonce+`"`)
	}
	if opaque != "" {
		parts = append(parts, `opaque="`+opaque+`"`)
	}
	req.Header.Set("Authorization", "Digest "+strings.Join(parts, ", "))
	return true
}

func parseDigestChallenge(challenge string) map[string]string {
	directives := map[string]string{}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(challenge), "Digest "))
	for _, pair := range splitDirectives(rest) {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		directives[key] = value
	}
	return directives
}

// splitDirectives splits a comma-separated directive list while ignoring
// commas inside quoted values (e.g. a domain list in the "domain" directive).
func splitDirectives(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case r == ',' && !inQuotes:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
