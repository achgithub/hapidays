package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"hapidays/internal/client"
	"hapidays/internal/model"
)

// A value captured by one request must be visible to later requests in the
// same run, the way it is when stepping through by hand with Send.
func TestRunCapturesFlowToLaterRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("X-CSRF-Token", "tok-123")
		case "/write":
			if r.Header.Get("X-CSRF-Token") != "tok-123" {
				w.WriteHeader(http.StatusForbidden)
			}
		}
	}))
	defer srv.Close()

	nodes := []*model.Node{
		{ID: "1", Name: "fetch", Request: &model.RequestSpec{
			Method:   "GET",
			URLRaw:   srv.URL + "/token",
			Auth:     model.Auth{Type: model.AuthNone},
			Captures: []model.Capture{{Source: "header", From: "X-CSRF-Token", IntoVar: "csrf"}},
		}},
		{ID: "2", Name: "write", Request: &model.RequestSpec{
			Method:  "POST",
			URLRaw:  srv.URL + "/write",
			Auth:    model.Auth{Type: model.AuthNone},
			Headers: []model.KV{{Key: "X-CSRF-Token", Value: "{{csrf}}"}},
		}},
	}

	var saved map[string]string
	results := Run(context.Background(), nodes, Options{
		Vars:      map[string]string{"csrf": "placeholder"},
		Client:    client.Options{},
		OnCapture: func(c map[string]string) { saved = c },
	})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[1].Status != 200 {
		t.Errorf("write status = %d (err %q), want 200: captured token was not passed on", results[1].Status, results[1].Error)
	}
	if saved["csrf"] != "tok-123" {
		t.Errorf("OnCapture got %v, want csrf=tok-123", saved)
	}
}

// Saving a run as evidence needs each step's full exchange, which the runner
// only keeps when asked (it makes results as big as every response body).
func TestRunKeepsExchangesOnlyWhenAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reply", "yes")
		_, _ = w.Write([]byte("hello " + r.Header.Get("X-Sent")))
	}))
	defer srv.Close()

	nodes := []*model.Node{{ID: "1", Name: "a", Request: &model.RequestSpec{
		Method:  "GET",
		URLRaw:  srv.URL + "/",
		Headers: []model.KV{{Key: "X-Sent", Value: "world"}},
		Auth:    model.Auth{Type: model.AuthNone},
	}}}

	without := Run(context.Background(), nodes, Options{})
	if without[0].Exchange != nil {
		t.Error("exchange present although not asked for")
	}

	with := Run(context.Background(), nodes, Options{KeepExchanges: true})
	res, ok := with[0].Exchange.(*client.Result)
	if !ok || res == nil {
		t.Fatalf("exchange = %#v, want *client.Result", with[0].Exchange)
	}
	if res.Body != "hello world" || res.Headers["X-Reply"][0] != "yes" {
		t.Errorf("response not kept: body %q headers %v", res.Body, res.Headers)
	}
	if res.Request == nil || res.Request.Headers["X-Sent"][0] != "world" {
		t.Errorf("request as sent not kept: %+v", res.Request)
	}
}
