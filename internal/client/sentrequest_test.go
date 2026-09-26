package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hapidays/internal/model"
)

type fakeJar struct{ cookies []*http.Cookie }

func (j fakeJar) CookiesForHost(host, path string) []*http.Cookie { return j.cookies }
func (j fakeJar) StoreCookies(host string, c []*http.Cookie)      {}

// The evidence for a request has to show what really went out, including the
// session Cookie the jar adds just before sending — on CPI that cookie is what
// the CSRF token is tied to, so a reviewer needs to see it was sent.
func TestExecuteRecordsRequestAsSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	spec := model.RequestSpec{
		Method:  "POST",
		URLRaw:  srv.URL + "/x",
		Headers: []model.KV{{Key: "X-CSRF-Token", Value: "tok-{{n}}"}},
		Auth:    model.Auth{Type: model.AuthBasic, Params: map[string]string{"username": "u", "password": "p"}},
		Body:    model.Body{Mode: model.BodyRaw, Raw: `{"a":"{{n}}"}`},
	}
	res, err := Execute(context.Background(), spec, map[string]string{"n": "42"}, Options{
		Cookies: fakeJar{cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "sess-1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Request == nil {
		t.Fatal("Result.Request is nil")
	}
	h := res.Request.Headers
	if got := h["Cookie"]; len(got) != 1 || !strings.Contains(got[0], "JSESSIONID=sess-1") {
		t.Errorf("Cookie header = %v, want the jar's session cookie", got)
	}
	if got := h["X-Csrf-Token"]; len(got) != 1 || got[0] != "tok-42" {
		t.Errorf("X-CSRF-Token = %v, want the resolved value tok-42", got)
	}
	if got := h["Authorization"]; len(got) != 1 || !strings.HasPrefix(got[0], "Basic ") {
		t.Errorf("Authorization = %v, want a Basic header", got)
	}
	if got := h["Content-Length"]; len(got) != 1 || got[0] != "10" {
		t.Errorf("Content-Length = %v, want 10 (the length of the body that was sent)", got)
	}
	if res.Request.Body != `{"a":"42"}` || res.Request.Method != "POST" || !strings.HasSuffix(res.Request.URL, "/x") {
		t.Errorf("request = %+v", res.Request)
	}
	if res.StartedAt.IsZero() {
		t.Error("StartedAt not set")
	}
}

// After a redirect the request worth showing is the last one — the one that
// got the answer.
func TestExecuteRecordsLastRequestAfterRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	res, err := Execute(context.Background(), model.RequestSpec{Method: "GET", URLRaw: srv.URL + "/start"}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Request == nil || !strings.HasSuffix(res.Request.URL, "/final") {
		t.Errorf("recorded %+v, want the /final request", res.Request)
	}
}
