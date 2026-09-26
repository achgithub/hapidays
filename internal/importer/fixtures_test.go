package importer

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"hapidays/internal/model"
)

// SAP's "Partner Directory Example Flows" Postman collection is a real-world
// import: two environments (Neo and CF) of basic-auth and bearer requests,
// with SAP's stock CSRF-fetch and OAuth-token test scripts. It pins down that
// those scripts come through as captures and its status checks as assertions.
func TestImportPartnerDirectoryExampleFlows(t *testing.T) {
	data, err := os.ReadFile("../../testdata/postman/Partner Directory Example Flows.postman_collection.json")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	col, err := ImportCollection(data, func() string { n++; return fmt.Sprint(n) })
	if err != nil {
		t.Fatal(err)
	}

	var requests, withAssertions, withCaptures int
	byName := map[string]*model.RequestSpec{}
	var walk func(nodes []*model.Node)
	walk = func(nodes []*model.Node) {
		for _, node := range nodes {
			if r := node.Request; r != nil {
				requests++
				if len(r.Assertions) > 0 {
					withAssertions++
				}
				if len(r.Captures) > 0 {
					withCaptures++
				}
				byName[node.Name] = r
			}
			walk(node.Children)
		}
	}
	walk(col.Root)

	if requests != 78 || withAssertions != 32 || withCaptures != 12 {
		t.Errorf("requests/withAssertions/withCaptures = %d/%d/%d, want 78/32/12", requests, withAssertions, withCaptures)
	}

	token := byName["PD Access Token"]
	if token == nil || len(token.Captures) != 1 || token.Captures[0] != (model.Capture{Source: "body_json", From: "access_token", IntoVar: "cpi_api_token"}) {
		t.Errorf("PD Access Token captures = %+v, want access_token -> cpi_api_token", token)
	}
	csrf := byName["XSLTFlow_GetXsrfToken"]
	if csrf == nil || len(csrf.Captures) != 1 || csrf.Captures[0] != (model.Capture{Source: "header", From: "x-csrf-token", IntoVar: "cpi_x_csrf_token"}) {
		t.Errorf("XSLTFlow_GetXsrfToken captures = %+v, want x-csrf-token -> cpi_x_csrf_token", csrf)
	}
	post := byName["PD String ReceiverUrl"]
	if post == nil || len(post.Assertions) != 1 || post.Assertions[0] != (model.Assertion{Type: model.AssertStatusEquals, Expected: "201"}) {
		t.Errorf("PD String ReceiverUrl assertions = %+v, want status equals 201", post)
	}
}

// SAP's "Handle Errors" collection: collection-level basic auth, CSRF fetches
// that store into globals (pm.globals.set) or the environment, and status
// checks for both success (200) and deliberate failure (500) cases.
func TestImportHandleErrors(t *testing.T) {
	data, err := os.ReadFile("../../testdata/postman/HandleErrors.postman_collection.json")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	col, err := ImportCollection(data, func() string { n++; return fmt.Sprint(n) })
	if err != nil {
		t.Fatal(err)
	}
	if col.Auth.Type != model.AuthBasic {
		t.Errorf("collection auth = %q, want basic", col.Auth.Type)
	}

	byName := map[string][]*model.RequestSpec{} // names repeat ("New Request"), so keep every match
	var walk func(nodes []*model.Node)
	walk = func(nodes []*model.Node) {
		for _, node := range nodes {
			if node.Request != nil {
				byName[node.Name] = append(byName[node.Name], node.Request)
			}
			walk(node.Children)
		}
	}
	walk(col.Root)

	wantStatus := func(name, code string) {
		t.Helper()
		for _, r := range byName[name] {
			if len(r.Assertions) != 1 || r.Assertions[0] != (model.Assertion{Type: model.AssertStatusEquals, Expected: code}) {
				t.Errorf("%s assertions = %+v, want status equals %s", name, r.Assertions, code)
			}
		}
		if len(byName[name]) == 0 {
			t.Errorf("request %q not found", name)
		}
	}
	wantStatus("SplitterWithStopOnException_withError", "500")    // a failure case: expects 500
	wantStatus("SplitterWithStopOnException_withoutError", "200") // and its success twin
	wantStatus("error on Failure, wrong identifier", "500")
	wantStatus("HandleErrors_DependentFlows", "200")

	csrf := func(name, header string) {
		t.Helper()
		reqs := byName[name]
		if len(reqs) == 0 {
			t.Fatalf("request %q not found", name)
		}
		want := model.Capture{Source: "header", From: header, IntoVar: "XSRFToken"}
		for _, r := range reqs {
			if len(r.Captures) != 1 || r.Captures[0] != want {
				t.Errorf("%s captures = %+v, want %+v", name, r.Captures, want)
			}
		}
	}
	csrf("SplitterWithStopOnException_GetXsrfToken", "X-CSRF-Token") // via pm.globals.set

	// Several requests are all named "New Request": only the HEADs fetch the token.
	for _, r := range byName["New Request"] {
		if r.Method == "HEAD" && len(r.Captures) != 1 {
			t.Errorf("HEAD New Request should capture the CSRF token, got %+v", r.Captures)
		}
	}
}

// SAP's "Apply Highest Security Standards" collection: XML payloads (a DOCTYPE
// with an external entity, near-identical integrity variants), a GET with a
// body, a SOAP call and a blank header name, alongside the usual CSRF fetches.
func TestImportApplyHighestSecurityStandards(t *testing.T) {
	data, err := os.ReadFile("../../testdata/postman/ApplyHighestSecurityStandards.postman_collection.json")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	col, err := ImportCollection(data, func() string { n++; return fmt.Sprint(n) })
	if err != nil {
		t.Fatal(err)
	}
	if col.Auth.Type != model.AuthBasic {
		t.Errorf("collection auth = %q, want basic", col.Auth.Type)
	}
	byName := map[string]*model.RequestSpec{}
	var walk func(nodes []*model.Node)
	walk = func(nodes []*model.Node) {
		for _, node := range nodes {
			if node.Request != nil {
				byName[node.Name] = node.Request
			}
			walk(node.Children)
		}
	}
	walk(col.Root)

	if r := byName["CSRFProtection"]; r == nil || r.Method != "GET" || r.Body.Raw != "<Test>Data</Test>" {
		t.Errorf("CSRFProtection should be a GET keeping its body, got %+v", r)
	}
	// Bodies must survive byte for byte, CRLFs included: the integrity variants
	// differ by nothing more than a comment or an attribute's position.
	if r := byName["DisableDTDs - Use External Entity"]; r == nil || !strings.Contains(r.Body.Raw, "<!ENTITY xxe SYSTEM") || !strings.Contains(r.Body.Raw, "\r\n") {
		t.Errorf("DTD body not preserved as written: %+v", r)
	}
	seen := map[string]bool{}
	for name, r := range byName {
		if strings.HasPrefix(name, "DataIntegrity - ") {
			seen[r.Body.Raw] = true
		}
	}
	if len(seen) != 5 {
		t.Errorf("DataIntegrity variants: %d distinct bodies, want 5", len(seen))
	}
	if r := byName["SplitterWithStopOnException_withError"]; r != nil {
		t.Errorf("unexpected request from another collection: %+v", r)
	}
	if r := byName["DisableDTDs_GetXsrfToken"]; r == nil || len(r.Captures) != 1 || r.Captures[0].IntoVar != "XSRFToken" {
		t.Errorf("DisableDTDs_GetXsrfToken should capture XSRFToken, got %+v", r)
	}
}
