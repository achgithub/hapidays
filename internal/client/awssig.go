package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// applyAWSSigV4 signs req per AWS Signature Version 4
// (https://docs.aws.amazon.com/general/latest/gr/sigv4-signing.html).
// Params: accessKey, secretKey, sessionToken (optional), region, service.
func applyAWSSigV4(req *http.Request, params map[string]string, vars map[string]string, bodyBytes []byte) {
	accessKey := Resolve(params["accessKey"], vars)
	secretKey := Resolve(params["secretKey"], vars)
	sessionToken := Resolve(params["sessionToken"], vars)
	region := Resolve(params["region"], vars)
	service := Resolve(params["service"], vars)
	if accessKey == "" || secretKey == "" || region == "" || service == "" {
		return
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}
	payloadHash := sha256Hex(bodyBytes)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if req.Host == "" {
		req.Host = req.URL.Host
	}

	canonicalHeaders, signedHeaders := canonicalizeHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	signingKey = hmacSHA256(signingKey, region)
	signingKey = hmacSHA256(signingKey, service)
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	authHeader := "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	req.Header.Set("Authorization", authHeader)
}

func canonicalURI(u *url.URL) string {
	path := u.Path
	if path == "" {
		return "/"
	}
	return awsURIEncode(path, false)
}

func canonicalQuery(u *url.URL) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		values := q[k]
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, awsURIEncode(k, true)+"="+awsURIEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// awsURIEncode percent-encodes s per AWS's UriEncode() rules (Create a
// canonical request, in the AWS SigV4 docs): every byte except
// A-Za-z0-9-._~ is percent-encoded, uppercase hex, space as %20 — not the
// "+" that url.QueryEscape/EscapedPath would produce, which is the classic
// wrong-implementation gotcha and would break the signature for any query
// value containing a space. encodeSlash controls whether '/' itself gets
// encoded: false for a path (where '/' is a segment separator to keep),
// true for a query key/value (where AWS wants it encoded like anything
// else).
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalizeHeaders(req *http.Request) (canonical, signed string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headerMap := map[string]string{"host": host}
	for k, v := range req.Header {
		headerMap[strings.ToLower(k)] = strings.Join(v, ",")
	}
	names := make([]string, 0, len(headerMap))
	for k := range headerMap {
		names = append(names, k)
	}
	sort.Strings(names)

	var canonicalLines []string
	for _, name := range names {
		canonicalLines = append(canonicalLines, name+":"+strings.TrimSpace(headerMap[name]))
	}
	return strings.Join(canonicalLines, "\n") + "\n", strings.Join(names, ";")
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
