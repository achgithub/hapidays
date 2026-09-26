package evidence

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hapidays/internal/client"
	"hapidays/internal/model"
)

const (
	clientSecret = "s3cr3t-value-XYZ"
	apiToken     = "tok-abcdef-123456"
	literalPass  = "literalpass-987"
	sessionID    = "sess-999999"
	csrfToken    = "csrf-777777-abc"
	newToken     = "fresh-token-555555"
)

func sampleInput() (map[string]string, Input) {
	vars := map[string]string{
		"cpi_clientSecret": clientSecret,
		"cpi_api_token":    apiToken,
		"cpi_x_csrf_token": "", // empty until fetched: must not be treated as a secret to replace
		"AS2_flag_token":   "true",
		"cpi_token_url":    "https://tenant.example.com/oauth/token",
		"partner_id":       "Partner-2",
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("some-id:"+literalPass))
	res := client.Result{
		Status: 201, StatusText: "201 Created", DurationMS: 12,
		StartedAt: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC),
		Request: &model.SentRequest{
			Method: "POST",
			URL:    "https://tenant.example.com/api/v1/x?client_secret=" + clientSecret + "&ok=1",
			Headers: map[string][]string{
				"Authorization": {basic},
				"Cookie":        {"JSESSIONID=" + sessionID + "; other=zzzzzzzz"},
				"X-Csrf-Token":  {csrfToken},
				"Content-Type":  {"application/json"},
				"Host":          {"tenant.example.com"},
			},
			Body: `{"Pid":"Partner-2","Password":"` + clientSecret + `","note":"reused ` + apiToken + ` and ` + literalPass + `","flag":"true"}`,
		},
		Headers: map[string][]string{
			"Set-Cookie":                 {"JSESSIONID=" + sessionID + "; Path=/; HttpOnly"},
			"X-Csrf-Token":               {csrfToken},
			"Sap_messageprocessinglogid": {"AGq3abc"},
			"Content-Type":               {"application/json"},
		},
		Body: `{"access_token":"` + newToken + `","token_type":"bearer","echo":"` + apiToken + `"}`,
	}
	return vars, Input{Name: "create thing", Result: res}
}

// The check that matters: nothing secret survives into any rendering of a
// redacted pack — JSON, text or HTML — including the base64 of the Basic
// credentials.
func TestRedactedPackLeaksNothing(t *testing.T) {
	vars, in := sampleInput()
	pack := Build(BuildParams{ID: strings.Repeat("a", 32), Who: "tester", Vars: vars, Items: []Input{in}, Now: time.Now()})

	js, _ := json.Marshal(pack)
	html, err := RenderHTML(pack)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{"json": string(js), "text": string(RenderText(pack)), "html": string(html)}

	secrets := []string{clientSecret, apiToken, literalPass, sessionID, csrfToken, newToken,
		base64.StdEncoding.EncodeToString([]byte("some-id:" + literalPass))}
	for name, out := range outputs {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s output leaks %q", name, s)
			}
		}
	}
}

func TestRedactionKeepsWhatIsNotSecret(t *testing.T) {
	vars, in := sampleInput()
	pack := Build(BuildParams{ID: strings.Repeat("a", 32), Who: "t", Vars: vars, Items: []Input{in}, Now: time.Now()})
	text := string(RenderText(pack))
	// A Basic credential is redacted whole (user and password together).
	if !strings.Contains(text, "Authorization: Basic [REDACTED:") {
		t.Errorf("Basic Authorization should show only a fingerprint; text:\n%s", text)
	}
	for _, keep := range []string{
		"Partner-2", "tenant.example.com", `"flag": "true"`, // short value and a URL-named var are left alone
		"JSESSIONID=", "other=", // cookie names stay so a reviewer sees a session cookie was sent
		"token_type", "bearer", // "token_type" is not a secret
		"AGq3abc", // message id
	} {
		if !strings.Contains(text, keep) {
			t.Errorf("redaction removed %q; text:\n%s", keep, text)
		}
	}
}

// Equal secrets get equal fingerprints, so a reviewer can tell the token
// captured in one step is the one sent in the next without seeing it.
func TestFingerprintsAreConsistent(t *testing.T) {
	vars, in := sampleInput()
	pack := Build(BuildParams{ID: strings.Repeat("a", 32), Who: "t", Vars: vars, Items: []Input{in}, Now: time.Now()})
	it := pack.Items[0]
	sent := it.Request.Headers["X-Csrf-Token"][0]
	got := it.Response.Headers["X-Csrf-Token"][0]
	if sent != got || sent != Marker(csrfToken) {
		t.Errorf("csrf fingerprints differ: sent %q, received %q, want %q", sent, got, Marker(csrfToken))
	}
}

func TestIncludeCredentialsKeepsThem(t *testing.T) {
	vars, in := sampleInput()
	pack := Build(BuildParams{ID: strings.Repeat("a", 32), Who: "t", IncludeCredentials: true, Vars: vars, Items: []Input{in}, Now: time.Now()})
	if pack.CredentialsRedacted || !strings.Contains(string(RenderText(pack)), clientSecret) {
		t.Error("with credentials included, the secret should be present and the pack marked as unredacted")
	}
	if !strings.Contains(string(RenderText(pack)), "INCLUDED") {
		t.Error("an unredacted pack must say so")
	}
}

// A response body can itself be HTML (a proxy's error page) and must not be
// able to inject into the report.
func TestHTMLReportEscapesBodies(t *testing.T) {
	in := Input{Name: `<b>x</b>`, Result: client.Result{Status: 403, StatusText: "403 Forbidden", Body: `<script>alert(1)</script>`}}
	pack := Build(BuildParams{ID: strings.Repeat("a", 32), Who: "<i>me</i>", Items: []Input{in}, Now: time.Now()})
	out, err := RenderHTML(pack)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "<script>alert(1)") || strings.Contains(s, "<b>x</b>") || strings.Contains(s, "<i>me</i>") {
		t.Error("HTML report contains unescaped content")
	}
	if !strings.Contains(s, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("expected the escaped body in the report")
	}
	if !strings.Contains(s, `href="#item-1"`) || !strings.Contains(s, `id="item-1"`) {
		t.Error("expected an index linking to each exchange")
	}
}

func TestPassedFollowsAssertionsThenStatus(t *testing.T) {
	cases := []struct {
		name string
		res  client.Result
		want bool
	}{
		{"expected 403 with passing assertion", client.Result{Status: 403, Assertions: []model.AssertionResult{{Passed: true}}}, true},
		{"200 with failing assertion", client.Result{Status: 200, Assertions: []model.AssertionResult{{Passed: false}}}, false},
		{"500 no assertions", client.Result{Status: 500}, false},
		{"200 no assertions", client.Result{Status: 200}, true},
		{"transport error", client.Result{Error: "x", Assertions: []model.AssertionResult{{Passed: true}}}, false},
	}
	for _, c := range cases {
		if got := Passed(c.res); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestLargeBodyIsTruncated(t *testing.T) {
	in := Input{Name: "big", Result: client.Result{Status: 200, Body: strings.Repeat("a", maxBody+10)}}
	pack := Build(BuildParams{ID: strings.Repeat("a", 32), Who: "t", Items: []Input{in}, Now: time.Now()})
	r := pack.Items[0].Response
	if len(r.Body) != maxBody || !r.BodyTruncated {
		t.Errorf("body len %d truncated=%v", len(r.Body), r.BodyTruncated)
	}
}
