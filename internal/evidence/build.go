package evidence

import (
	"strings"
	"time"

	"hapidays/internal/client"
	"hapidays/internal/model"
)

// maxBody caps a saved response body; a pack is meant to be read and emailed.
const maxBody = 1 << 20

// Input is one executed request to be included in a pack.
type Input struct {
	Name   string
	Result client.Result
}

// BuildParams describes the pack to build.
type BuildParams struct {
	ID                 string
	Who, Notes         string
	CollectionName     string
	EnvironmentName    string
	IncludeCredentials bool
	// Vars are the variables that were in scope when the requests ran; the
	// values of secret-named ones are redacted wherever they appear.
	Vars  map[string]string
	Items []Input
	Now   time.Time
}

// Build assembles a pack, redacting credentials unless asked not to.
func Build(p BuildParams) *model.EvidencePack {
	pack := &model.EvidencePack{
		ID:                  p.ID,
		SavedAt:             p.Now,
		Who:                 strings.TrimSpace(p.Who),
		Notes:               p.Notes,
		CollectionName:      p.CollectionName,
		EnvironmentName:     p.EnvironmentName,
		CredentialsRedacted: !p.IncludeCredentials,
	}

	// Credentials typed straight into a request's auth (not via a variable)
	// are only visible in the Authorization header that went out; learn them
	// from there so they are also caught when they appear elsewhere.
	var extra []string
	for _, in := range p.Items {
		if in.Result.Request == nil {
			continue
		}
		for k, vs := range in.Result.Request.Headers {
			if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "proxy-authorization") {
				for _, v := range vs {
					extra = append(extra, SecretsInAuthHeader(v)...)
				}
			}
		}
	}
	red := NewRedactor(p.Vars, extra...)

	for _, in := range p.Items {
		pack.Items = append(pack.Items, buildItem(in, red, p.IncludeCredentials))
	}
	return pack
}

func buildItem(in Input, red *Redactor, includeCredentials bool) model.EvidenceItem {
	res := in.Result
	item := model.EvidenceItem{
		Name:       in.Name,
		StartedAt:  res.StartedAt,
		Assertions: res.Assertions,
		Response: model.EvidenceResponse{
			Status:       res.Status,
			StatusText:   res.StatusText,
			Headers:      res.Headers,
			Body:         res.Body,
			BodyIsBase64: res.BodyIsBase64,
			DurationMS:   res.DurationMS,
			SizeBytes:    res.SizeBytes,
			Error:        res.Error,
		},
		Passed: Passed(res),
	}
	item.MessageID = headerValue(res.Headers, "SAP_MessageProcessingLogID")

	if res.Request != nil {
		req := *res.Request
		item.Request = &req
	} else if res.ResolvedURL != "" {
		item.Request = &model.SentRequest{URL: res.ResolvedURL}
	}

	// Truncate before redacting so a secret split by the cut can't survive
	// half-redacted, then redact.
	if len(item.Response.Body) > maxBody {
		item.Response.Body = item.Response.Body[:maxBody]
		item.Response.BodyTruncated = true
	}
	if includeCredentials {
		return item
	}

	if item.Request != nil {
		item.Request.URL = red.URL(item.Request.URL)
		item.Request.Headers = red.Headers(item.Request.Headers)
		if !item.Request.BodyIsBase64 {
			item.Request.Body = red.Body(item.Request.Body)
		}
	}
	item.Response.Headers = red.Headers(item.Response.Headers)
	if !item.Response.BodyIsBase64 {
		item.Response.Body = red.Body(item.Response.Body)
	}
	item.Response.Error = red.Text(item.Response.Error)
	for i := range item.Assertions {
		item.Assertions[i].Message = red.Text(item.Assertions[i].Message)
		item.Assertions[i].Expected = red.Text(item.Assertions[i].Expected)
		item.Assertions[i].Target = red.Text(item.Assertions[i].Target)
	}
	return item
}

// Passed applies the same rule as the runner: assertions, when there are
// any, decide; otherwise a 2xx/3xx with no transport error.
func Passed(res client.Result) bool {
	if res.Error != "" {
		return false
	}
	if len(res.Assertions) > 0 {
		for _, a := range res.Assertions {
			if !a.Passed {
				return false
			}
		}
		return true
	}
	return res.Status >= 200 && res.Status < 400
}

func headerValue(h map[string][]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}
