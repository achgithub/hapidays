package importer

import (
	"regexp"
	"strings"

	"hapidays/internal/model"
)

// Best-effort translation of the Postman test-script idioms that have a
// direct hapidays equivalent, so an imported collection is useful straight
// away instead of every request needing its captures and checks re-entered
// by hand. Nothing here executes JavaScript: each idiom is recognised by
// pattern and turned into a model.Capture or model.Assertion. Anything with
// conditionals, loops or string manipulation is not recognised and stays
// only as the read-only script text on the RequestSpec.

// setter matches the ways a script stores a value in a variable scope.
const setter = `(?:pm\.(?:environment|collectionVariables|globals|variables)\.set|postman\.set(?:Environment|Global)Variable)`

var (
	// pm.environment.set("x", pm.response.headers.get("H"))
	headerDirectRe = regexp.MustCompile(setter + `\(\s*["']([^"']+)["']\s*,\s*pm\.response\.headers\.get\(\s*["']([^"']+)["']\s*\)\s*\)`)
	// pm.environment.set("x", pm.response.json().a.b)
	jsonDirectRe = regexp.MustCompile(setter + `\(\s*["']([^"']+)["']\s*,\s*pm\.response\.json\(\)\.([\w.]+)\s*\)`)
	// let v = pm.response.headers.get("H") | pm.response.json() | JSON.parse(responseBody)
	// — the stock SAP "X-CSRF-Token: Fetch" and OAuth-token scripts both do this,
	// then pass v (or v.field) to a setter on a later line.
	assignRe = regexp.MustCompile(`(?:var|let|const)\s+(\w+)\s*=\s*(?:pm\.response\.(headers\.get\(\s*["']([^"']+)["']\s*\)|json\(\))|JSON\.parse\(\s*responseBody\s*\))`)

	statusRe   = regexp.MustCompile(`pm\.response\.to\.have\.status\(\s*(\d{3})\s*\)`)
	codeRe     = regexp.MustCompile(`pm\.expect\(\s*pm\.response\.code\s*\)\.to\.(?:eql|equal|eq)\(\s*(\d{3})\s*\)`)
	okRe       = regexp.MustCompile(`pm\.response\.to\.be\.ok\b`)
	successRe  = regexp.MustCompile(`pm\.response\.to\.be\.success\b`)
	headerRe   = regexp.MustCompile(`pm\.response\.to\.have\.header\(\s*["']([^"']+)["']\s*(?:,\s*["']([^"']*)["']\s*)?\)`)
	durationRe = regexp.MustCompile(`pm\.expect\(\s*pm\.response\.responseTime\s*\)\.to\.be\.(?:below|lessThan)\(\s*(\d+)\s*\)`)
	bodyRe     = regexp.MustCompile(`pm\.expect\(\s*pm\.response\.text\(\)\s*\)\.to\.(?:include|contain)\(\s*["']([^"']+)["']\s*\)`)
)

// capturesFromScript returns the Captures equivalent to the response-to-
// variable assignments found in a test script.
func capturesFromScript(script string) []model.Capture {
	var out []model.Capture
	add := func(c model.Capture) {
		for _, e := range out {
			if e == c {
				return
			}
		}
		out = append(out, c)
	}
	for _, m := range headerDirectRe.FindAllStringSubmatch(script, -1) {
		add(model.Capture{Source: "header", From: m[2], IntoVar: m[1]})
	}
	for _, m := range jsonDirectRe.FindAllStringSubmatch(script, -1) {
		add(model.Capture{Source: "body_json", From: m[2], IntoVar: m[1]})
	}
	for _, am := range assignRe.FindAllStringSubmatch(script, -1) {
		v := regexp.QuoteMeta(am[1])
		if strings.HasPrefix(am[2], "headers") {
			use := regexp.MustCompile(setter + `\(\s*["']([^"']+)["']\s*,\s*` + v + `\s*\)`)
			for _, m := range use.FindAllStringSubmatch(script, -1) {
				add(model.Capture{Source: "header", From: am[3], IntoVar: m[1]})
			}
		} else {
			use := regexp.MustCompile(setter + `\(\s*["']([^"']+)["']\s*,\s*` + v + `\.([\w.]+)\s*\)`)
			for _, m := range use.FindAllStringSubmatch(script, -1) {
				add(model.Capture{Source: "body_json", From: m[2], IntoVar: m[1]})
			}
		}
	}
	return out
}

// assertionsFromScript returns the Assertions equivalent to the response
// checks found in a test script.
func assertionsFromScript(script string) []model.Assertion {
	var out []model.Assertion
	add := func(a model.Assertion) {
		for _, e := range out {
			if e == a {
				return
			}
		}
		out = append(out, a)
	}
	for _, m := range statusRe.FindAllStringSubmatch(script, -1) {
		add(model.Assertion{Type: model.AssertStatusEquals, Expected: m[1]})
	}
	for _, m := range codeRe.FindAllStringSubmatch(script, -1) {
		add(model.Assertion{Type: model.AssertStatusEquals, Expected: m[1]})
	}
	if okRe.MatchString(script) {
		add(model.Assertion{Type: model.AssertStatusEquals, Expected: "200"})
	}
	if successRe.MatchString(script) {
		add(model.Assertion{Type: model.AssertStatusRange, Expected: "2xx"})
	}
	for _, m := range headerRe.FindAllStringSubmatch(script, -1) {
		if m[2] != "" {
			add(model.Assertion{Type: model.AssertHeaderEquals, Target: m[1], Expected: m[2]})
		} else {
			add(model.Assertion{Type: model.AssertHeaderExists, Target: m[1]})
		}
	}
	for _, m := range durationRe.FindAllStringSubmatch(script, -1) {
		add(model.Assertion{Type: model.AssertMaxDurationMS, Expected: m[1]})
	}
	for _, m := range bodyRe.FindAllStringSubmatch(script, -1) {
		add(model.Assertion{Type: model.AssertBodyContains, Expected: m[1]})
	}
	return out
}

// SuggestFromScript exposes the recognisers to the UI's "Suggest from script"
// button, so there is a single implementation of the patterns rather than a
// second copy in the front end drifting out of sync with the importer.
func SuggestFromScript(script string) ([]model.Capture, []model.Assertion) {
	return capturesFromScript(script), assertionsFromScript(script)
}
