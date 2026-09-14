package client

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

// recordingJar wraps a real net/http/cookiejar.Jar so Go's transport gets
// correct cookie behavior across redirects (which a hand-rolled "read
// resp.Cookies() at the end" approach gets wrong: a Set-Cookie on an
// intermediate redirect hop, like httpbin's /cookies/set endpoint, never
// appears on the final response). It also records every SetCookies call
// so Execute can persist what changed via the CookieJar interface.
type recordingJar struct {
	inner *cookiejar.Jar
	sets  map[string][]*http.Cookie // host -> cookies set during this jar's lifetime
}

func newRecordingJar() *recordingJar {
	inner, _ := cookiejar.New(nil)
	return &recordingJar{inner: inner, sets: map[string][]*http.Cookie{}}
}

// preload seeds the jar from persisted storage without recording it back
// out again (it hasn't changed).
func (j *recordingJar) preload(u *url.URL, cookies []*http.Cookie) {
	j.inner.SetCookies(u, cookies)
}

func (j *recordingJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.inner.SetCookies(u, cookies)
	if len(cookies) > 0 {
		for _, c := range cookies {
			// Go's http.Cookie.Path is exactly what the Set-Cookie header
			// specified — empty if the server omitted a Path attribute. j.inner
			// (net/http/cookiejar) resolves that internally for its own
			// in-session matching, but what gets recorded here is what
			// store.StoreCookies persists, so it needs the same RFC 6265
			// §5.1.4 default-path resolved explicitly before it's saved.
			if c.Path == "" {
				c.Path = defaultCookiePath(u.Path)
			}
		}
		j.sets[u.Hostname()] = append(j.sets[u.Hostname()], cookies...)
	}
}

// defaultCookiePath implements RFC 6265 §5.1.4: the directory containing
// the request path, or "/" if that request path is "/", empty, or has no
// further '/' after the first.
func defaultCookiePath(requestPath string) string {
	if !strings.HasPrefix(requestPath, "/") || requestPath == "/" {
		return "/"
	}
	if i := strings.LastIndex(requestPath, "/"); i > 0 {
		return requestPath[:i]
	}
	return "/"
}

func (j *recordingJar) Cookies(u *url.URL) []*http.Cookie {
	return j.inner.Cookies(u)
}
