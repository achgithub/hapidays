package evidence

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"

	"hapidays/internal/model"
)

// Two renderings of a pack, both meant to be read and copied from:
//
//   - RenderText: plain text. Pastes straight into an email, ticket or doc.
//   - RenderHTML: the same content with an index and links to each exchange,
//     plain <pre> blocks and no scripts, styles beyond the bare minimum, or
//     external resources — quick to open, easy to select and copy from.
//
// The pack's raw JSON is offered separately for anything that wants the data.

type headerRow struct{ Name, Value string }

type itemView struct {
	Index       int
	Anchor      string
	Name        string
	Passed      bool
	Verdict     string
	Started     string
	Method, URL string
	MessageID   string
	StatusLine  string
	DurationMS  int64
	Error       string
	ReqHeaders  []headerRow
	ReqBody     string
	ReqNote     string
	RespHeaders []headerRow
	RespBody    string
	RespNote    string
	Assertions  []assertionRow
}

type assertionRow struct{ Verdict, Text string }

type packView struct {
	ID          string
	Who         string
	Saved       string
	Collection  string
	Environment string
	Credentials string
	Notes       string
	Total       int
	Passed      int
	Failed      int
	Items       []itemView
}

func viewOf(p *model.EvidencePack) packView {
	v := packView{
		ID: p.ID, Who: p.Who, Notes: strings.TrimSpace(p.Notes),
		Saved:       p.SavedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
		Collection:  p.CollectionName,
		Environment: p.EnvironmentName,
		Total:       len(p.Items),
		Credentials: "redacted (each replaced by a short fingerprint; equal values give equal fingerprints)",
	}
	if !p.CredentialsRedacted {
		v.Credentials = "INCLUDED - this pack contains real credentials, do not share it"
	}
	for i, it := range p.Items {
		iv := itemView{
			Index: i + 1, Anchor: "item-" + strconv.Itoa(i+1),
			Name: it.Name, Passed: it.Passed, Verdict: verdict(it.Passed),
			MessageID: it.MessageID, DurationMS: it.Response.DurationMS, Error: it.Response.Error,
		}
		if it.Passed {
			v.Passed++
		} else {
			v.Failed++
		}
		if !it.StartedAt.IsZero() {
			iv.Started = it.StartedAt.UTC().Format(time.RFC3339)
		}
		switch {
		case it.Response.StatusText != "":
			iv.StatusLine = it.Response.StatusText
		case it.Response.Status != 0:
			iv.StatusLine = strconv.Itoa(it.Response.Status)
		}
		if it.Request != nil {
			iv.Method, iv.URL = it.Request.Method, it.Request.URL
			iv.ReqHeaders = headerRows(it.Request.Headers)
			iv.ReqBody, iv.ReqNote = bodyView(it.Request.Body, it.Request.BodyIsBase64, it.Request.BodyTruncated)
		}
		iv.RespHeaders = headerRows(it.Response.Headers)
		iv.RespBody, iv.RespNote = bodyView(it.Response.Body, it.Response.BodyIsBase64, it.Response.BodyTruncated)
		for _, a := range it.Assertions {
			iv.Assertions = append(iv.Assertions, assertionRow{Verdict: verdict(a.Passed), Text: a.Message})
		}
		v.Items = append(v.Items, iv)
	}
	return v
}

func verdict(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func headerRows(h map[string][]string) []headerRow {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	var rows []headerRow
	for _, k := range names {
		for _, val := range h[k] {
			rows = append(rows, headerRow{Name: k, Value: val})
		}
	}
	return rows
}

// bodyView prepares a body for display and returns a note to show with it.
// JSON is re-indented with json.Indent, which keeps key order as it was;
// binary bodies are labelled rather than dumped as if they were text.
func bodyView(body string, isBase64, truncated bool) (string, string) {
	note := ""
	if truncated {
		note = "body truncated"
	}
	if isBase64 {
		label := "binary body shown as base64"
		if dec, err := base64.StdEncoding.DecodeString(body); err == nil {
			label = "binary body, " + strconv.Itoa(len(dec)) + " bytes, shown as base64"
		}
		if note != "" {
			label += "; " + note
		}
		return body, label
	}
	if t := strings.TrimSpace(body); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var out bytes.Buffer
		if json.Indent(&out, []byte(t), "", "  ") == nil {
			return out.String(), note
		}
	}
	return body, note
}

// RenderText renders a pack as plain text.
func RenderText(p *model.EvidencePack) []byte {
	v := viewOf(p)
	var b strings.Builder
	line := strings.Repeat("=", 72)
	sub := strings.Repeat("-", 72)

	b.WriteString("TEST EVIDENCE\n" + line + "\n")
	b.WriteString("Saved by:    " + v.Who + "\n")
	b.WriteString("Saved:       " + v.Saved + "\n")
	if v.Environment != "" {
		b.WriteString("Environment: " + v.Environment + "\n")
	}
	if v.Collection != "" {
		b.WriteString("Collection:  " + v.Collection + "\n")
	}
	b.WriteString("Credentials: " + v.Credentials + "\n")
	b.WriteString("Result:      " + strconv.Itoa(v.Total) + " exchange(s): " + strconv.Itoa(v.Passed) + " passed, " + strconv.Itoa(v.Failed) + " failed\n")
	if v.Notes != "" {
		b.WriteString("\nNotes\n" + sub + "\n" + v.Notes + "\n")
	}
	b.WriteString("\nINDEX\n" + sub + "\n")
	for _, it := range v.Items {
		b.WriteString(strconv.Itoa(it.Index) + ". [" + it.Verdict + "] " + it.Name)
		if it.Method != "" {
			b.WriteString("  (" + it.Method + " -> " + it.StatusLine + ")")
		}
		b.WriteString("\n")
	}
	for _, it := range v.Items {
		b.WriteString("\n" + line + "\n" + strconv.Itoa(it.Index) + ". " + it.Name + "  [" + it.Verdict + "]\n" + line + "\n")
		if it.Started != "" {
			b.WriteString("Started:    " + it.Started + "\n")
		}
		b.WriteString("Duration:   " + strconv.FormatInt(it.DurationMS, 10) + " ms\n")
		if it.MessageID != "" {
			b.WriteString("Message id: " + it.MessageID + "   (CPI monitor: message processing log id)\n")
		}
		b.WriteString("\n--- REQUEST\n")
		if it.Method != "" || it.URL != "" {
			b.WriteString(it.Method + " " + it.URL + "\n")
		}
		writeHeaders(&b, it.ReqHeaders)
		writeBody(&b, it.ReqBody, it.ReqNote)
		b.WriteString("\n--- RESPONSE\n")
		if it.Error != "" {
			b.WriteString("ERROR: " + it.Error + "\n")
		} else {
			b.WriteString(it.StatusLine + "\n")
		}
		writeHeaders(&b, it.RespHeaders)
		writeBody(&b, it.RespBody, it.RespNote)
		if len(it.Assertions) > 0 {
			b.WriteString("\n--- ASSERTIONS\n")
			for _, a := range it.Assertions {
				b.WriteString("[" + a.Verdict + "] " + a.Text + "\n")
			}
		}
	}
	return []byte(b.String())
}

func writeHeaders(b *strings.Builder, rows []headerRow) {
	for _, r := range rows {
		b.WriteString(r.Name + ": " + r.Value + "\n")
	}
}

func writeBody(b *strings.Builder, body, note string) {
	if note != "" {
		b.WriteString("[" + note + "]\n")
	}
	if body != "" {
		b.WriteString("\n" + body + "\n")
	}
}

// RenderHTML renders a pack as a light HTML page: an index at the top linking
// to each exchange, and plain <pre> blocks. html/template escapes every
// value, which matters here — a response body can itself be HTML (a proxy's
// error page) and must not be able to inject into the report.
func RenderHTML(p *model.EvidencePack) ([]byte, error) {
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, viewOf(p)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var reportTmpl = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Test evidence</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
body{font:14px/1.45 system-ui,sans-serif;max-width:1000px;margin:1.5em auto;padding:0 1em;color:#111}
pre{background:#f5f5f5;padding:.6em;overflow:auto;white-space:pre-wrap;word-break:break-word;font:12px/1.4 ui-monospace,Menlo,Consolas,monospace}
h1,h2,h3{margin:.8em 0 .3em} table td{padding:0 1em 0 0;vertical-align:top}
.PASS{color:#0a6b2a;font-weight:600}.FAIL{color:#b3261e;font-weight:600}.warn{color:#b3261e;font-weight:600}
.note{color:#555;font-style:italic} a{color:#0b57d0} hr{margin:2em 0}
@media (prefers-color-scheme:dark){body{background:#161616;color:#eee}pre{background:#222}a{color:#8ab4f8}.PASS{color:#6dd58c}.FAIL,.warn{color:#f2b8b5}.note{color:#aaa}}
</style></head><body>
<h1 id="top">Test evidence</h1>
<table>
<tr><td>Saved by</td><td>{{.Who}}</td></tr>
<tr><td>Saved</td><td>{{.Saved}}</td></tr>
{{if .Environment}}<tr><td>Environment</td><td>{{.Environment}}</td></tr>{{end}}
{{if .Collection}}<tr><td>Collection</td><td>{{.Collection}}</td></tr>{{end}}
<tr><td>Credentials</td><td>{{.Credentials}}</td></tr>
<tr><td>Result</td><td>{{.Total}} exchange(s): {{.Passed}} passed, {{.Failed}} failed</td></tr>
</table>
{{if .Notes}}<h2>Notes</h2><pre>{{.Notes}}</pre>{{end}}
<h2>Index</h2>
<ol>{{range .Items}}<li><a href="#{{.Anchor}}">{{.Name}}</a> <span class="{{.Verdict}}">{{.Verdict}}</span>{{if .Method}} {{.Method}} &rarr; {{.StatusLine}}{{end}}</li>{{end}}</ol>
{{range .Items}}<hr>
<h2 id="{{.Anchor}}">{{.Index}}. {{.Name}} <span class="{{.Verdict}}">{{.Verdict}}</span></h2>
<p><a href="#top">&uarr; index</a></p>
<table>
{{if .Started}}<tr><td>Started</td><td>{{.Started}}</td></tr>{{end}}
<tr><td>Duration</td><td>{{.DurationMS}} ms</td></tr>
{{if .MessageID}}<tr><td>Message id</td><td>{{.MessageID}} <span class="note">(CPI monitor: message processing log id)</span></td></tr>{{end}}
</table>
<h3>Request</h3>
<pre>{{if or .Method .URL}}{{.Method}} {{.URL}}
{{end}}{{range .ReqHeaders}}{{.Name}}: {{.Value}}
{{end}}{{if .ReqNote}}[{{.ReqNote}}]
{{end}}{{if .ReqBody}}
{{.ReqBody}}{{end}}</pre>
<h3>Response</h3>
<pre>{{if .Error}}ERROR: {{.Error}}{{else}}{{.StatusLine}}{{end}}
{{range .RespHeaders}}{{.Name}}: {{.Value}}
{{end}}{{if .RespNote}}[{{.RespNote}}]
{{end}}{{if .RespBody}}
{{.RespBody}}{{end}}</pre>
{{if .Assertions}}<h3>Assertions</h3>
<pre>{{range .Assertions}}[{{.Verdict}}] {{.Text}}
{{end}}</pre>{{end}}
{{end}}
</body></html>
`))
