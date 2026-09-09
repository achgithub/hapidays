package client

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
		j.sets[u.Hostname()] = append(j.sets[u.Hostname()], cookies...)
	}
}

func (j *recordingJar) Cookies(u *url.URL) []*http.Cookie {
	return j.inner.Cookies(u)
}
