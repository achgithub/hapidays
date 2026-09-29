package importer

import (
	"encoding/json"
	"fmt"
	"testing"

	"hapidays/internal/model"
)

func seqID() func() string {
	n := 0
	return func() string { n++; return fmt.Sprint("id", n) }
}

func TestCollectionBundleRoundTrip(t *testing.T) {
	col := &model.Collection{ID: "c1", Name: "CPI", Root: []*model.Node{}}
	envs := []*model.Environment{{
		ID: "e1", CollectionID: "c1", Name: "PRD",
		Values: []model.KV{
			{Key: "host", Value: "prd.example.com"},
			{Key: "password", Value: "hunter2"},
			{Key: "cpi_x_csrf_token", Value: "abc"},
		},
		ClientCertFile: "/Users/me/cert.pem", ClientKeyFile: "/Users/me/key.pem",
	}}

	data, err := json.Marshal(ExportCollectionBundle(col, envs))
	if err != nil {
		t.Fatal(err)
	}

	got, gotEnvs, err := ImportCollectionBundle(data, seqID())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "c1" {
		t.Fatal("collection ID was not reassigned")
	}
	if len(gotEnvs) != 1 {
		t.Fatalf("want 1 environment, got %d", len(gotEnvs))
	}
	e := gotEnvs[0]
	if e.CollectionID != got.ID {
		t.Errorf("environment owned by %q, want new collection %q", e.CollectionID, got.ID)
	}
	if e.ID == "e1" {
		t.Error("environment ID was not reassigned")
	}
	want := map[string]string{"host": "prd.example.com", "password": "", "cpi_x_csrf_token": ""}
	for _, kv := range e.Values {
		if kv.Value != want[kv.Key] {
			t.Errorf("%s = %q, want %q (secrets blanked, hosts kept)", kv.Key, kv.Value, want[kv.Key])
		}
	}
	if e.ClientCertFile != "" || e.ClientKeyFile != "" {
		t.Error("machine-specific client cert paths should not be exported")
	}
}

func TestExportDoesNotMutateStoredEnvironment(t *testing.T) {
	envs := []*model.Environment{{ID: "e", Values: []model.KV{{Key: "password", Value: "keep"}}}}
	ExportCollectionBundle(&model.Collection{ID: "c"}, envs)
	if envs[0].Values[0].Value != "keep" {
		t.Fatal("export blanked the stored environment's secret")
	}
}

func TestImportEnvironmentClearsForeignOwner(t *testing.T) {
	data := []byte(`{"id":"x","collectionId":"other-machine","name":"Dev","values":[],"updatedAt":"2026-01-01T00:00:00Z"}`)
	env, err := ImportEnvironment(data, seqID())
	if err != nil {
		t.Fatal(err)
	}
	if env.CollectionID != "" {
		t.Errorf("imported environment kept foreign owner %q", env.CollectionID)
	}
}

func TestPlainCollectionHasNoEnvironments(t *testing.T) {
	_, envs, err := ImportCollectionBundle([]byte(`{"id":"c","name":"n","root":[],"updatedAt":"2026-01-01T00:00:00Z"}`), seqID())
	if err != nil || len(envs) != 0 {
		t.Fatalf("envs=%v err=%v", envs, err)
	}
}
