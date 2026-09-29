package store

import (
	"testing"

	"hapidays/internal/model"
)

func TestDeleteEnvironmentsForCollection(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []*model.Environment{
		{ID: "a", CollectionID: "c1", Name: "Dev"},
		{ID: "b", CollectionID: "c1", Name: "PRD"},
		{ID: "c", CollectionID: "c2", Name: "Dev"},
		{ID: "d", Name: "unassigned"},
	} {
		if err := s.SaveEnvironment(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteEnvironmentsForCollection("c1"); err != nil {
		t.Fatal(err)
	}
	left, _ := s.ListEnvironments()
	ids := map[string]bool{}
	for _, e := range left {
		ids[e.ID] = true
	}
	if len(left) != 2 || !ids["c"] || !ids["d"] {
		t.Fatalf("want only c2's and the unassigned environment left, got %v", ids)
	}
}
