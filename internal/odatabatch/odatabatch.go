// Package odatabatch builds and parses OData $batch requests: several
// independent HTTP requests bundled into one multipart/mixed HTTP call, and
// the matching multipart/mixed response split back into individual
// per-request results.
//
// Covers the multipart/mixed batch format, which both OData v2 and v4
// support (v4.01 added a simpler JSON batch format as an alternative, but
// this app's primary real-world target — SAP Gateway/CPI, v2 — only
// supports multipart, so that's what's implemented). Per the OData
// spec, a batch part is either:
//   - a GET/HEAD request, sent directly in the outer multipart, or
//   - one or more state-changing requests grouped into a "changeset" — its
//     own nested multipart/mixed part — since only changesets get
//     transactional atomicity guarantees from the server.
//
// This implementation puts every non-GET request in its own single-item
// changeset rather than trying to infer which requests the caller wants
// grouped atomically. That's always spec-valid and avoids guessing at
// intent; a caller that specifically needs multiple writes to succeed or
// fail together isn't well served by an automatic grouping heuristic
// anyway.
package odatabatch

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// Result is one sub-response from a $batch call.
type Result struct {
	Status  int
	Headers map[string][]string
	Body    string
}

func newBoundary(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// Build renders requests as one OData $batch body. servicePath is the
// $batch endpoint's own path with "/$batch" removed (e.g.
// "/V2/Northwind/Northwind.svc" for a $batch URL of
// ".../Northwind.svc/$batch") — each request-line inside the batch must be
// relative to that service root, not the request's full absolute path;
// sending an absolute path there 404s (confirmed against the live public
// V2 Northwind $batch endpoint while building this). Each request's Body
// is read fully (and must therefore support re-reading if the caller
// intends to use it again afterward — callers here always construct a
// fresh *http.Request per batch, so that's not a concern).
func Build(requests []*http.Request, servicePath string) (body []byte, contentType string, err error) {
	batchBoundary := newBoundary("batch")
	var buf bytes.Buffer

	for _, req := range requests {
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			if err := writePart(&buf, batchBoundary, req, servicePath); err != nil {
				return nil, "", err
			}
			continue
		}
		// Non-GET: wrap in its own single-item changeset.
		changesetBoundary := newBoundary("changeset")
		fmt.Fprintf(&buf, "--%s\r\n", batchBoundary)
		fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", changesetBoundary)
		if err := writePart(&buf, changesetBoundary, req, servicePath); err != nil {
			return nil, "", err
		}
		fmt.Fprintf(&buf, "--%s--\r\n", changesetBoundary)
	}
	fmt.Fprintf(&buf, "--%s--\r\n", batchBoundary)

	return buf.Bytes(), "multipart/mixed; boundary=" + batchBoundary, nil
}

func writePart(buf *bytes.Buffer, boundary string, req *http.Request, servicePath string) error {
	var bodyBytes []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("read sub-request body: %w", err)
		}
		bodyBytes = b
	}

	fmt.Fprintf(buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: application/http\r\n")
	buf.WriteString("Content-Transfer-Encoding: binary\r\n\r\n")

	relPath := strings.TrimPrefix(req.URL.RequestURI(), servicePath)
	fmt.Fprintf(buf, "%s %s HTTP/1.1\r\n", req.Method, relPath)
	for key, values := range req.Header {
		for _, v := range values {
			fmt.Fprintf(buf, "%s: %s\r\n", key, v)
		}
	}
	if len(bodyBytes) > 0 {
		fmt.Fprintf(buf, "Content-Length: %d\r\n", len(bodyBytes))
	}
	buf.WriteString("\r\n")
	buf.Write(bodyBytes)
	buf.WriteString("\r\n")
	return nil
}

// Parse splits a $batch response (contentType from the outer HTTP
// response's own Content-Type header) into one Result per sub-request, in
// the same order Build's requests were given — assuming the server
// preserves order, which every implementation encountered in practice
// does (the spec doesn't strictly require it, but there'd be no way to
// correlate results back to requests if it didn't, since batch parts
// aren't otherwise labeled).
func Parse(contentType string, body []byte) ([]Result, error) {
	return parseMultipart(contentType, body)
}

func parseMultipart(contentType string, body []byte) ([]Result, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, fmt.Errorf("parse batch response Content-Type: %w", err)
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, fmt.Errorf("expected a multipart/mixed $batch response, got %q", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("$batch response Content-Type has no boundary")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var results []Result
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read batch part: %w", err)
		}
		partBody, err := io.ReadAll(part)
		if err != nil {
			return nil, fmt.Errorf("read batch part body: %w", err)
		}
		partCT := part.Header.Get("Content-Type")

		if strings.HasPrefix(strings.ToLower(partCT), "multipart/") {
			// A changeset's response: itself a nested multipart/mixed
			// containing one HTTP response (or, on a changeset failure,
			// one HTTP response carrying the error for the whole group).
			nested, err := parseMultipart(partCT, partBody)
			if err != nil {
				return nil, err
			}
			results = append(results, nested...)
			continue
		}

		result, err := parseHTTPResponsePart(partBody)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// parseHTTPResponsePart parses a raw "HTTP/1.1 200 OK\r\nHeader: v\r\n\r\nbody"
// blob — the content of one application/http batch part — using the same
// parser Go's own http.Client uses for real responses on the wire.
func parseHTTPResponsePart(raw []byte) (Result, error) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		return Result{}, fmt.Errorf("parse batch sub-response: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read batch sub-response body: %w", err)
	}
	headers := map[string][]string{}
	for k, v := range resp.Header {
		headers[textproto.CanonicalMIMEHeaderKey(k)] = v
	}
	return Result{Status: resp.StatusCode, Headers: headers, Body: string(bodyBytes)}, nil
}
