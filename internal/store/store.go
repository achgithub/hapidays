// Package store persists collections, environments, settings and history
// as plain JSON files on disk. No database, no admin rights needed —
// just a writable directory.
package store

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"pmclone/internal/model"
)

type Settings struct {
	// ExtraCAFile, if set, is a PEM bundle appended to the system trust
	// store for outgoing requests — for corporate TLS-intercepting proxies.
	ExtraCAFile string `json:"extraCaFile,omitempty"`
	// InsecureSkipVerify disables TLS cert verification. Global default;
	// can be overridden per-request from the UI.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// ClientCertFile/ClientKeyFile enable mTLS for outgoing requests.
	ClientCertFile string `json:"clientCertFile,omitempty"`
	ClientKeyFile  string `json:"clientKeyFile,omitempty"`
	// ProxyURL overrides the environment-derived proxy when set.
	ProxyURL string `json:"proxyUrl,omitempty"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "collections"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "environments"), 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (s *Store) writeJSON(path string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// ---- collections ----

func (s *Store) SaveCollection(c *model.Collection) error {
	return s.writeJSON(filepath.Join(s.dir, "collections", c.ID+".json"), c)
}

func (s *Store) LoadCollection(id string) (*model.Collection, error) {
	return readJSON[model.Collection](filepath.Join(s.dir, "collections", id+".json"))
}

func (s *Store) DeleteCollection(id string) error {
	return os.Remove(filepath.Join(s.dir, "collections", id+".json"))
}

func (s *Store) ListCollections() ([]*model.Collection, error) {
	return listDir[model.Collection](filepath.Join(s.dir, "collections"))
}

// smokeTestSeedMarker, once present, means the bundled smoke-test
// collection has already been seeded into this data directory — whether
// it's still there or the user has since deleted it. Presence of the
// collection file itself can't be used for this check: deleting it must
// not resurrect it on the next launch.
const smokeTestSeedMarker = ".smoke-test-seeded"

// SeedSmokeTestCollection writes the bundled "pmclone smoke test"
// collection (see internal/seed) into a data directory exactly once —
// so a fresh install has something to test the app against without a
// manual import — and is a no-op on every run after that.
func (s *Store) SeedSmokeTestCollection(data []byte) error {
	markerPath := filepath.Join(s.dir, smokeTestSeedMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var col model.Collection
	if err := json.Unmarshal(data, &col); err != nil {
		return fmt.Errorf("parse seed collection: %w", err)
	}
	col.ID = "smoke-test"
	col.UpdatedAt = time.Now()
	if err := s.SaveCollection(&col); err != nil {
		return err
	}
	return os.WriteFile(markerPath, []byte("seeded\n"), 0o600)
}

// ---- environments ----

func (s *Store) SaveEnvironment(e *model.Environment) error {
	return s.writeJSON(filepath.Join(s.dir, "environments", e.ID+".json"), e)
}

func (s *Store) LoadEnvironment(id string) (*model.Environment, error) {
	return readJSON[model.Environment](filepath.Join(s.dir, "environments", id+".json"))
}

func (s *Store) DeleteEnvironment(id string) error {
	return os.Remove(filepath.Join(s.dir, "environments", id+".json"))
}

func (s *Store) ListEnvironments() ([]*model.Environment, error) {
	return listDir[model.Environment](filepath.Join(s.dir, "environments"))
}

func listDir[T any](dir string) ([]*T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*T{}, nil // never nil: encodes as JSON [], not null, for list endpoints
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]*T, 0, len(names))
	for _, n := range names {
		v, err := readJSON[T](filepath.Join(dir, n))
		if err != nil {
			continue // skip corrupt file rather than fail the whole list
		}
		out = append(out, v)
	}
	return out, nil
}

// ---- settings ----

func (s *Store) SaveSettings(cfg Settings) error {
	return s.writeJSON(filepath.Join(s.dir, "settings.json"), cfg)
}

func (s *Store) LoadSettings() (Settings, error) {
	v, err := readJSON[Settings](filepath.Join(s.dir, "settings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, nil
		}
		return Settings{}, err
	}
	return *v, nil
}

// ---- history ----

const maxHistory = 500

func (s *Store) AppendHistory(entry model.HistoryEntry) error {
	path := filepath.Join(s.dir, "history.json")
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []model.HistoryEntry
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	entries = append(entries, entry)
	if len(entries) > maxHistory {
		entries = entries[len(entries)-maxHistory:]
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) ListHistory() ([]model.HistoryEntry, error) {
	path := filepath.Join(s.dir, "history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []model.HistoryEntry{}, nil // never nil: encodes as JSON [], not null
		}
		return nil, err
	}
	var entries []model.HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
