package model

import "time"

// SentRequest is a request as it actually went out on the wire — the
// resolved URL and the headers after auth, collection headers and the cookie
// jar have all been applied — as opposed to RequestSpec, which is the
// editable definition with {{vars}} still in it.
type SentRequest struct {
	Method        string              `json:"method"`
	URL           string              `json:"url"`
	Headers       map[string][]string `json:"headers"`
	Body          string              `json:"body,omitempty"`
	BodyIsBase64  bool                `json:"bodyIsBase64,omitempty"`
	BodyTruncated bool                `json:"bodyTruncated,omitempty"`
}

// EvidenceResponse is the response half of a saved exchange.
type EvidenceResponse struct {
	Status        int                 `json:"status"`
	StatusText    string              `json:"statusText,omitempty"`
	Headers       map[string][]string `json:"headers,omitempty"`
	Body          string              `json:"body,omitempty"`
	BodyIsBase64  bool                `json:"bodyIsBase64,omitempty"`
	BodyTruncated bool                `json:"bodyTruncated,omitempty"`
	DurationMS    int64               `json:"durationMs"`
	SizeBytes     int64               `json:"sizeBytes"`
	Error         string              `json:"error,omitempty"`
}

// EvidenceItem is one request/response exchange.
type EvidenceItem struct {
	Name       string            `json:"name"`
	StartedAt  time.Time         `json:"startedAt"`
	Request    *SentRequest      `json:"request,omitempty"`
	Response   EvidenceResponse  `json:"response"`
	Assertions []AssertionResult `json:"assertions,omitempty"`
	Passed     bool              `json:"passed"`
	// MessageID is the CPI message-processing-log id when the response
	// carries one (SAP_MessageProcessingLogID), so a reviewer can look the
	// call up in the tenant's monitor.
	MessageID string `json:"messageId,omitempty"`
}

// EvidencePack is what gets saved and handed to a reviewer: one or more
// exchanges plus who saved it, when, and the tester's notes.
type EvidencePack struct {
	ID              string    `json:"id"`
	SavedAt         time.Time `json:"savedAt"`
	Who             string    `json:"who"`
	Notes           string    `json:"notes,omitempty"`
	CollectionName  string    `json:"collectionName,omitempty"`
	EnvironmentName string    `json:"environmentName,omitempty"`
	// CredentialsRedacted is false only when the person saving explicitly
	// asked for credentials to be included; the report then says so loudly.
	CredentialsRedacted bool           `json:"credentialsRedacted"`
	Items               []EvidenceItem `json:"items"`
}
