package fields

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTemplateSupportsSafeLookupConditionsIterationAndFormatting(t *testing.T) {
	resolved, err := Resolve(ResolveOptions{PublicationID: "report", Defaults: map[string]any{
		"organization": map[string]any{"name": "Acme"},
		"approved":     false,
		"reviewers":    []any{"A", "B"},
		"tags":         []any{"x", "y"},
		"date":         "2026-08-01",
		"amount":       12.5,
		"amount_text":  "12.5",
	}, AllowedRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	templateValue, err := ParseTemplate("cover", `{{upper (field "organization.name")}}|{{if field "approved"}}yes{{else}}no{{end}}|{{range $i, $v := field "reviewers"}}{{$i}}={{$v}};{{end}}|{{join ", " (field "tags")}}|{{date "Jan 2, 2006" (field "date")}}|{{number 2 (field "amount")}}|{{number 1 (field "amount_text")}}|{{.fields.organization.name}}`)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := templateValue.Evaluate(TemplateContext{PublicationID: "report", Fields: resolved.Values})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Kind != SegmentText {
		t.Fatalf("segments = %#v", segments)
	}
	want := "ACME|no|0=A;1=B;|x, y|Aug 1, 2026|12.50|12.5|Acme"
	if segments[0].Text != want {
		t.Fatalf("text = %q, want %q", segments[0].Text, want)
	}
}

func TestTemplatePreservesExactJSONNumbers(t *testing.T) {
	templateValue, err := ParseTemplate("numbers", `{{if eq (field "first") (field "second")}}same{{else}}different{{end}}|{{if eq (field "second") 9007199254740993}}exact{{end}}|{{number (field "second")}}|{{number 2 (field "second")}}|{{number (field "fraction")}}|{{if field "tiny"}}true{{else}}false{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := templateValue.Evaluate(TemplateContext{PublicationID: "report", Fields: map[string]any{
		"first": json.Number("9007199254740992"), "second": json.Number("9007199254740993"),
		"fraction": json.Number("1e-3"), "tiny": json.Number("1e-400"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Text != "different|exact|9007199254740993|9007199254740993.00|0.001|true" {
		t.Fatalf("exact number template = %#v", segments)
	}
}

func TestTemplateProducesTypedPageSegmentsWithoutSentinels(t *testing.T) {
	templateValue, err := ParseTemplate("footer", `literal PAGE_TOKEN {{field "literal"}} Page {{page "number"}} of {{page "count"}}`)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := templateValue.Evaluate(TemplateContext{PublicationID: "report", Fields: map[string]any{"literal": "{{page number}}"}, AllowPageFields: true})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []SegmentKind{SegmentText, SegmentPage, SegmentText, SegmentPageCount}
	if len(segments) != len(wantKinds) {
		t.Fatalf("segments = %#v", segments)
	}
	for index, kind := range wantKinds {
		if segments[index].Kind != kind {
			t.Fatalf("segment %d = %#v", index, segments[index])
		}
	}
	if !strings.Contains(segments[0].Text, "PAGE_TOKEN") || !strings.Contains(segments[0].Text, "{{page number}}") {
		t.Fatalf("author text changed: %#v", segments)
	}

	_, err = templateValue.Evaluate(TemplateContext{PublicationID: "report", Fields: map[string]any{"literal": "text"}})
	if err == nil || !strings.Contains(err.Error(), "only supported in headers and footers") {
		t.Fatalf("unsupported page placement error = %v", err)
	}
}

func TestTemplateRejectsPageSegmentsConsumedByControlFlowOrHelpers(t *testing.T) {
	for _, source := range []string{
		`{{if page "number"}}bad{{end}}`,
		`{{with page "number"}}{{.}}{{end}}`,
		`{{range page "number"}}{{.}}{{end}}`,
		`{{page "number" | upper}}`,
	} {
		parsed, err := ParseTemplate("footer", source)
		if err != nil {
			t.Fatal(err)
		}
		_, err = parsed.Evaluate(TemplateContext{PublicationID: "report", AllowPageFields: true})
		if err == nil || !strings.Contains(err.Error(), "must be emitted directly") {
			t.Fatalf("source=%s error=%v", source, err)
		}
	}
}

func TestTemplateStrictMissingAndUnsafeFunctions(t *testing.T) {
	templateValue, err := ParseTemplate("title", `{{field "missing.path"}}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = templateValue.Evaluate(TemplateContext{PublicationID: "report", Fields: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), `publication "report"`) || !strings.Contains(err.Error(), `missing.path`) {
		t.Fatalf("missing field error = %v", err)
	}

	for _, source := range []string{`{{env "HOME"}}`, `{{printf "%s" "raw"}}`, `{{html "<w:t/>"}}`} {
		parsed, parseErr := ParseTemplate("unsafe", source)
		if parseErr == nil {
			_, parseErr = parsed.Evaluate(TemplateContext{PublicationID: "report"})
		}
		if parseErr == nil {
			t.Fatalf("unsafe template executed: %s", source)
		}
	}
}

func TestTemplateTextRemainsDataForJSONAndXMLSerialization(t *testing.T) {
	value := `<tag attr="x">& "quoted"` + "\n"
	templateValue, err := ParseTemplate("text", `{{field "value"}}`)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := templateValue.Evaluate(TemplateContext{PublicationID: "report", Fields: map[string]any{"value": value}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(segments)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `\u003ctag`) || segments[0].Text != value {
		t.Fatalf("encoded=%s segments=%#v", encoded, segments)
	}
}

func TestComputedValuesFreezeAndResumeExactly(t *testing.T) {
	plan := PlanComputedValues(PlanComputed{ProjectID: "project", Profile: "review", PublicationID: "report", PublicationKind: "bundle", PublicationTitle: "Report"})
	frozen := FrozenComputed{OperationID: "operation", ReviewSetID: "review-set", Generation: 2, PublishedAt: time.Date(2026, 8, 1, 12, 34, 56, 123, time.UTC)}
	first := FreezeComputed(plan, frozen)
	second := FreezeComputed(plan, frozen)
	firstJSON, err := CanonicalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := CanonicalJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) || first["operation_id"] != "operation" || first["generation"] != 2 {
		t.Fatalf("computed values changed: %s %s", firstJSON, secondJSON)
	}
	templateValue, err := ParseTemplate("header", `{{mdoc "project.id"}}/{{mdoc "publication.id"}}/{{mdoc "generation"}}`)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := templateValue.Evaluate(TemplateContext{PublicationID: "report", Computed: first})
	if err != nil || len(segments) != 1 || segments[0].Text != "project/report/2" {
		t.Fatalf("computed template = %#v, %v", segments, err)
	}
}
