package importer

import (
	"reflect"
	"testing"

	"hapidays/internal/model"
)

func TestCapturesFromScript(t *testing.T) {
	cases := []struct {
		name, script string
		want         []model.Capture
	}{
		{"csrf via variable (SAP stock script)",
			"\nlet xsrfValue = pm.response.headers.get('X-CSRF-Token')\npm.environment.set(\"cpi_x_csrf_token\", xsrfValue);\n",
			[]model.Capture{{Source: "header", From: "X-CSRF-Token", IntoVar: "cpi_x_csrf_token"}}},
		{"csrf inline",
			`pm.environment.set("t", pm.response.headers.get("x-csrf-token"))`,
			[]model.Capture{{Source: "header", From: "x-csrf-token", IntoVar: "t"}}},
		{"token via JSON.parse and legacy setter",
			`var jsonData = JSON.parse(responseBody); postman.setEnvironmentVariable("cpi_api_token", jsonData.access_token);`,
			[]model.Capture{{Source: "body_json", From: "access_token", IntoVar: "cpi_api_token"}}},
		{"token via pm.response.json()",
			`let jsonData = pm.response.json(); pm.environment.set("cpi_api_token", jsonData.access_token);`,
			[]model.Capture{{Source: "body_json", From: "access_token", IntoVar: "cpi_api_token"}}},
		{"json inline nested path",
			`pm.collectionVariables.set("id", pm.response.json().data.id)`,
			[]model.Capture{{Source: "body_json", From: "data.id", IntoVar: "id"}}},
		{"unrecognised script yields nothing",
			`if (pm.response.code === 200) { pm.environment.set("x", "literal") }`, nil},
	}
	for _, c := range cases {
		if got := capturesFromScript(c.script); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAssertionsFromScript(t *testing.T) {
	cases := []struct {
		name, script string
		want         []model.Assertion
	}{
		{"status via pm.test", `pm.test("Status code is 201", function () { pm.response.to.have.status(201); });`,
			[]model.Assertion{{Type: model.AssertStatusEquals, Expected: "201"}}},
		{"status via expect code", `pm.expect(pm.response.code).to.eql(204)`,
			[]model.Assertion{{Type: model.AssertStatusEquals, Expected: "204"}}},
		{"ok and success", `pm.response.to.be.ok; pm.response.to.be.success;`,
			[]model.Assertion{{Type: model.AssertStatusEquals, Expected: "200"}, {Type: model.AssertStatusRange, Expected: "2xx"}}},
		{"header exists and equals", `pm.response.to.have.header("X-CSRF-Token"); pm.response.to.have.header('Content-Type', 'application/json');`,
			[]model.Assertion{{Type: model.AssertHeaderExists, Target: "X-CSRF-Token"}, {Type: model.AssertHeaderEquals, Target: "Content-Type", Expected: "application/json"}}},
		{"duration and body", `pm.expect(pm.response.responseTime).to.be.below(500); pm.expect(pm.response.text()).to.include("ok");`,
			[]model.Assertion{{Type: model.AssertMaxDurationMS, Expected: "500"}, {Type: model.AssertBodyContains, Expected: "ok"}}},
		{"duplicates collapse", `pm.response.to.have.status(200); pm.response.to.have.status(200);`,
			[]model.Assertion{{Type: model.AssertStatusEquals, Expected: "200"}}},
	}
	for _, c := range cases {
		if got := assertionsFromScript(c.script); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
