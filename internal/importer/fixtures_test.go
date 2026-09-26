package importer

import (
	"fmt"
	"os"
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
