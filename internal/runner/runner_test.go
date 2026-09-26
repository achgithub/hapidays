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
