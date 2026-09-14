package store

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hapidays/internal/model"
)

// cookieMu guards cookies.json separately from Store.mu so a cookie write
// during request execution never blocks on an unrelated collection save.
var cookieMu sync.Mutex

func (s *Store) cookiesPath() string {
	return filepath.Join(s.dir, "cookies.json")
}

func (s *Store) loadCookieRecords() []model.CookieRecord {
	data, err := os.ReadFile(s.cookiesPath())
	if err != nil {
		return nil
	}
	var records []model.CookieRecord
	_ = json.Unmarshal(data, &records)
	return records
}

func (s *Store) saveCookieRecords(records []model.CookieRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.cookiesPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.cookiesPath())
}

func domainMatches(host, cookieDomain string) bool {
	cookieDomain = strings.TrimPrefix(cookieDomain, ".")
	return host == cookieDomain || strings.HasSuffix(host, "."+cookieDomain)
}

// pathMatches implements RFC 6265 §5.1.4's path-match algorithm. An empty
// cookiePath (a record from before path-scoping existed, or added by hand
// with none given) matches everything, same as "/" — the old
// sent-to-every-path behavior, rather than a silent behavior change for
// existing data.
func pathMatches(requestPath, cookiePath string) bool {
	if cookiePath == "" || cookiePath == "/" || requestPath == cookiePath {
		return true
	}
	if strings.HasPrefix(requestPath, cookiePath) {
		if strings.HasSuffix(cookiePath, "/") {
			return true
		}
		if len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/' {
			return true
		}
	}
	return false
}

// CookiesForHost implements client.CookieJar: every non-expired cookie
// whose Domain matches host (exact or subdomain) and whose Path path-
// matches the outgoing request path.
func (s *Store) CookiesForHost(host, path string) []*http.Cookie {
	cookieMu.Lock()
	records := s.loadCookieRecords()
	cookieMu.Unlock()

	now := time.Now()
	var out []*http.Cookie
	for _, r := range records {
		if !r.Expires.IsZero() && r.Expires.Before(now) {
			continue
		}
		if domainMatches(host, r.Domain) && pathMatches(path, r.Path) {
			out = append(out, &http.Cookie{Name: r.Name, Value: r.Value})
		}
	}
	return out
}

// StoreCookies implements client.CookieJar, persisting Set-Cookie results
// keyed by the request host (cookies with an explicit Domain attribute use
// that instead, matching normal browser behavior).
func (s *Store) StoreCookies(host string, cookies []*http.Cookie) {
	if len(cookies) == 0 {
		return
	}
	cookieMu.Lock()
	defer cookieMu.Unlock()

	records := s.loadCookieRecords()
	for _, c := range cookies {
		domain := c.Domain
		if domain == "" {
			domain = host
		}
		found := false
		for i, r := range records {
			if r.Domain == domain && r.Path == c.Path && r.Name == c.Name {
				records[i].Value = c.Value
				records[i].Expires = c.Expires
				records[i].Secure = c.Secure
				records[i].HTTPOnly = c.HttpOnly
				found = true
				break
			}
		}
		if !found {
			records = append(records, model.CookieRecord{
				Domain: domain, Path: c.Path, Name: c.Name, Value: c.Value,
				Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HttpOnly,
			})
		}
	}
	_ = s.saveCookieRecords(records)
}

// SetCookie upserts a single cookie record directly — the manual "add a
// cookie" path from the UI, as opposed to StoreCookies, which persists
// Set-Cookie results parsed off a real response.
func (s *Store) SetCookie(rec model.CookieRecord) error {
	cookieMu.Lock()
	defer cookieMu.Unlock()
	records := s.loadCookieRecords()
	for i, r := range records {
		if r.Domain == rec.Domain && r.Name == rec.Name {
			records[i] = rec
			return s.saveCookieRecords(records)
		}
	}
	records = append(records, rec)
	return s.saveCookieRecords(records)
}

func (s *Store) ListCookies() []model.CookieRecord {
	cookieMu.Lock()
	defer cookieMu.Unlock()
	records := s.loadCookieRecords()
	if records == nil {
		return []model.CookieRecord{} // never nil: encodes as JSON [], not null
	}
	return records
}

func (s *Store) ClearCookies() error {
	cookieMu.Lock()
	defer cookieMu.Unlock()
	return s.saveCookieRecords(nil)
}

func (s *Store) DeleteCookie(domain, name string) error {
	cookieMu.Lock()
	defer cookieMu.Unlock()
	records := s.loadCookieRecords()
	out := make([]model.CookieRecord, 0, len(records))
	for _, r := range records {
		if r.Domain == domain && r.Name == name {
			continue
		}
		out = append(out, r)
	}
	return s.saveCookieRecords(out)
}
