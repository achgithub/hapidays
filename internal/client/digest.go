package client

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"strings"
)

// digestHash returns the hash.Hash for a challenge's algorithm directive,
// per RFC 7616 §6.1 — SHA-256 is the mandatory-to-implement algorithm,
// SHA-512-256 a backup, MD5 kept only for servers that never upgraded
// (and the assumed default when the directive is absent, per the RFC).
// The "-sess" variants (session-keyed HA1) aren't handled — ok is false
// for those, same as for an algorithm this doesn't recognize at all.
func digestHash(algorithm string) (h func() hash.Hash, ok bool) {
	switch strings.ToUpper(algorithm) {
	case "", "MD5":
		return md5.New, true
	case "SHA-256":
		return sha256.New, true
	case "SHA-512-256":
		return sha512.New512_256, true
	default:
		return nil, false
	}
}

// applyDigestAuth parses a WWW-Authenticate: Digest challenge and sets the
// Authorization header for a retry, per RFC 7616. Supports both qop=auth
// and qop=auth-int (which folds a hash of the request body into HA2), and
// negotiates the challenge's algorithm directive (MD5, SHA-256, or
// SHA-512-256 — not the "-sess" variants). Returns false if the challenge
// isn't a Digest one it can handle, in which case Execute just returns the
// original 401 response.
func applyDigestAuth(req *http.Request, challenge string, params map[string]string, vars map[string]string, bodyBytes []byte) bool {
	if !strings.HasPrefix(strings.TrimSpace(challenge), "Digest ") {
		return false
	}
	directives := parseDigestChallenge(challenge)
	realm := directives["realm"]
	nonce := directives["nonce"]
	if nonce == "" {
		return false
	}
	newHash, ok := digestHash(directives["algorithm"])
	if !ok {
		return false
	}
	qop := pickQop(directives["qop"])
	opaque := directives["opaque"]

	username := Resolve(params["username"], vars)
	password := Resolve(params["password"], vars)
	cnonce := randomHex(8)
	nc := "00000001"

	ha1 := hashHex(newHash, username+":"+realm+":"+password)
	var ha2 string
	if qop == "auth-int" {
		ha2 = hashHex(newHash, req.Method+":"+req.URL.RequestURI()+":"+hashHex(newHash, string(bodyBytes)))
	} else {
		ha2 = hashHex(newHash, req.Method+":"+req.URL.RequestURI())
	}

	var response string
	if qop != "" {
		response = hashHex(newHash, ha1+":"+nonce+":"+nc+":"+cnonce+":"+qop+":"+ha2)
	} else {
		response = hashHex(newHash, ha1+":"+nonce+":"+ha2)
	}

	parts := []string{
		`username="` + username + `"`,
		`realm="` + realm + `"`,
		`nonce="` + nonce + `"`,
		`uri="` + req.URL.RequestURI() + `"`,
		`response="` + response + `"`,
	}
	if directives["algorithm"] != "" {
		parts = append(parts, `algorithm=`+directives["algorithm"])
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

// pickQop chooses auth-int when the server offers it (a strictly stronger
// guarantee — it also authenticates the body), otherwise falls back to
// auth, otherwise (a server with no qop at all) the pre-RFC-2617 form.
// challengeQop may be a comma-separated list (e.g. "auth,auth-int").
func pickQop(challengeQop string) string {
	options := strings.Split(challengeQop, ",")
	for _, o := range options {
		if strings.TrimSpace(o) == "auth-int" {
			return "auth-int"
		}
	}
	for _, o := range options {
		if strings.TrimSpace(o) == "auth" {
			return "auth"
		}
	}
	return ""
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

func hashHex(newHash func() hash.Hash, s string) string {
	h := newHash()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
