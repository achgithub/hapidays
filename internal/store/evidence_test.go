package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hapidays/internal/model"
)

func TestEvidenceListAndDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListEvidence(); err != nil || len(list) != 0 {
		t.Fatalf("empty store: %v %v", list, err)
	}

	older := &model.EvidencePack{ID: strings.Repeat("a", 32), SavedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Who: "old",
		CredentialsRedacted: true, Items: []model.EvidenceItem{{Passed: true}, {Passed: false}}}
	newer := &model.EvidencePack{ID: strings.Repeat("b", 32), SavedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Who: "new",
		Notes: "n", Items: []model.EvidenceItem{{Passed: true}}}
	for _, p := range []*model.EvidencePack{older, newer} {
		if err := s.SaveEvidence(p); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file and a damaged pack must not hide the good ones.
	_ = os.WriteFile(filepath.Join(dir, "evidence", "notes.txt"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "evidence", strings.Repeat("c", 32)+".json"), []byte("{broken"), 0o600)

	list, err := s.ListEvidence()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Who != "new" || list[1].Who != "old" {
		t.Fatalf("want newest first, got %+v", list)
	}
	if list[1].Items != 2 || list[1].Passed != 1 || list[1].Failed != 1 || !list[1].CredentialsRedacted {
		t.Errorf("summary counts wrong: %+v", list[1])
	}

	if err := s.DeleteEvidence(older.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListEvidence(); len(list) != 1 || list[0].Who != "new" {
		t.Errorf("after delete: %+v", list)
	}
}

// Ids arrive in URLs and become file names.
func TestEvidenceRejectsUnsafeIDs(t *testing.T) {
	s, _ := New(t.TempDir())
	for _, id := range []string{"../../etc/passwd", "", "abc", strings.Repeat("A", 32), strings.Repeat("a", 31) + "/"} {
		if err := s.DeleteEvidence(id); err == nil {
			t.Errorf("DeleteEvidence(%q) should be refused", id)
		}
		if _, err := s.LoadEvidence(id); err == nil {
			t.Errorf("LoadEvidence(%q) should be refused", id)
		}
	}
}
