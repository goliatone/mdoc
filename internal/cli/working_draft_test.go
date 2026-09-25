package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/goliatone/mdoc/workingdraft"
)

func TestWorkingDraftStructuredErrorCode(t *testing.T) {
	var out, stderr bytes.Buffer
	err := Execute([]string{"working-draft", "inspect", "https://example.com/document/d/id"}, &out, &stderr, nil)
	if err == nil {
		t.Fatal("accepted arbitrary source URL")
	}
	WriteError(&stderr, err, WantsJSON([]string{"working-draft", "inspect"}))
	var result ErrorOutput
	if json.Unmarshal(stderr.Bytes(), &result) != nil || result.Code != string(workingdraft.InvalidSource) || result.Class != "working_draft" {
		t.Fatalf("lost typed error: %s", stderr.String())
	}
}
