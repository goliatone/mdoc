package state

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJournalEntryFreezesResolvedFields(t *testing.T) {
	entry := JournalEntry{
		SourceKey: "publication:report", ResolvedFields: map[string]any{"nested": map[string]any{"value": "frozen"}, "null": nil},
		FieldsHash: "sha256:fields", FieldFileHashes: []string{"sha256:file-one", "sha256:file-two"},
		ComputedValues: map[string]any{"operation_id": "operation", "generation": float64(2)}, ComputedHash: "sha256:computed",
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var decoded JournalEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FieldsHash != entry.FieldsHash || len(decoded.FieldFileHashes) != 2 || decoded.ResolvedFields["null"] != nil || decoded.ComputedHash != entry.ComputedHash || decoded.ComputedValues["operation_id"] != "operation" {
		t.Fatalf("decoded entry = %#v", decoded)
	}
	nested := decoded.ResolvedFields["nested"].(map[string]any)
	if nested["value"] != "frozen" {
		t.Fatalf("resolved fields = %#v", decoded.ResolvedFields)
	}
	if !strings.Contains(string(data), "resolved_fields") {
		t.Fatalf("journal omitted frozen fields: %s", data)
	}
}
