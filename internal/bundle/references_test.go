package bundle

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
)

func TestAssembleCreatesProtectedReferenceLinksAndTargetBookmarks(t *testing.T) {
	root := t.TempDir()
	definitionPath := filepath.Join(root, "definitions.md")
	referencePath := filepath.Join(root, "references.md")
	target := &publication.Publication{
		ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error",
		Members: []publication.Member{
			{SourceKey: "definitions.md", Document: &document.Document{Path: definitionPath}},
			{SourceKey: "references.md", Document: &document.Document{Path: referencePath}},
		},
		ReferenceLinks: []publication.ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", DefinitionMatch: "heading_prefix", Unresolved: "error"}},
	}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{
		definitionPath: encodeDocument(t, []any{heading(2, "op01", "OP01 - One"), heading(2, "op02", "OP02 - Two")}),
		referencePath:  encodeDocument(t, []any{paragraph(referenceString("OP01-OP02"))}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Topology.Dependencies) != 2 || len(result.Topology.Headings) != 2 {
		t.Fatalf("topology = %#v", result.Topology)
	}
	for _, heading := range result.Topology.Headings {
		if heading.ID == "" {
			t.Fatalf("referenced definition has no bookmark: %#v", result.Topology.Headings)
		}
	}
	if strings.Count(string(result.JSON), `"t":"Link"`) != 2 {
		t.Fatalf("definition self-link or missing reference link: %s", result.JSON)
	}
	if err := VerifyTopology(result.Topology, result.JSON); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(result.JSON), result.Topology.Dependencies[0].Target, "#changed", 1)
	if err := VerifyTopology(result.Topology, []byte(changed)); err == nil || !strings.Contains(err.Error(), "dependency") {
		t.Fatalf("changed reference topology error = %v", err)
	}
}

func TestAssembleOrdersGeneratedAndExplicitDependenciesByDocument(t *testing.T) {
	root := t.TempDir()
	definitionPath := filepath.Join(root, "definitions.md")
	referencePath := filepath.Join(root, "references.md")
	target := &publication.Publication{
		ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", FragmentPolicy: "error", AllowedRoots: []string{root},
		Members: []publication.Member{
			{SourceKey: "definitions.md", Document: &document.Document{Path: definitionPath}},
			{SourceKey: "references.md", Document: &document.Document{Path: referencePath}},
		},
		ReferenceLinks: []publication.ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", DefinitionMatch: "heading_prefix", Unresolved: "error"}},
	}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{
		definitionPath: encodeDocument(t, []any{heading(2, "op01", "OP01 - One"), heading(2, "op02", "OP02 - Two")}),
		referencePath: encodeDocument(t, []any{
			paragraph(referenceString("OP01")),
			paragraph(link("definitions.md#op02")),
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Topology.Dependencies) != 2 || result.Topology.Dependencies[0].Target != "#"+result.Topology.Headings[0].ID || result.Topology.Dependencies[1].Target != "#"+result.Topology.Headings[1].ID {
		t.Fatalf("dependency order = %#v, headings = %#v", result.Topology.Dependencies, result.Topology.Headings)
	}
	if err := VerifyTopology(result.Topology, result.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteReferencesPreservesTextAndSkipsProtectedInlineKinds(t *testing.T) {
	definitionBlocks, definitionRefs, _, err := transformHeadings("definitions.md", []any{
		heading(2, "op01", "OP01 - First package"),
		heading(2, "op02", "OP02 - Second package"),
	}, config.MemberConfig{})
	if err != nil {
		t.Fatal(err)
	}
	referenceBlocks := []any{
		paragraph(
			referenceString("OP01-OP02"),
			map[string]any{"t": "Space"},
			map[string]any{"t": "Strong", "c": []any{referenceString("(OP01)")}},
			map[string]any{"t": "Space"},
			referenceLink("OP01", "https://example.com"),
			map[string]any{"t": "Code", "c": []any{[]any{"", []any{}, []any{}}, "OP01"}},
			map[string]any{"t": "RawInline", "c": []any{"html", "OP01"}},
			map[string]any{"t": "Image", "c": []any{[]any{"", []any{}, []any{}}, []any{referenceString("OP02")}, []any{"image.png", ""}}},
		),
		map[string]any{"t": "BulletList", "c": []any{[]any{paragraph(referenceString("OP01"))}}},
		map[string]any{"t": "Table", "c": []any{paragraph(referenceString("OP02"))}},
		map[string]any{"t": "Figure", "c": []any{paragraph(referenceString("OP01"))}},
		heading(3, "decision", "Approve OP02"),
	}
	_, referenceRefs, _, err := transformHeadings("references.md", referenceBlocks, config.MemberConfig{})
	if err != nil {
		t.Fatal(err)
	}
	prepared := []preparedMember{
		{member: publication.Member{SourceKey: "definitions.md", Document: &document.Document{}}, blocks: definitionBlocks, refs: definitionRefs},
		{member: publication.Member{SourceKey: "references.md", Document: &document.Document{}}, blocks: referenceBlocks, refs: referenceRefs},
	}
	rules, err := prepareReferenceRules([]publication.ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", DefinitionMatch: "heading_prefix", Unresolved: "error"}}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	bookmarks := map[string]string{definitionRefs[0].logical: "m_op01", definitionRefs[1].logical: "m_op02"}
	definitionResult, err := rewriteReferences(definitionBlocks, "definitions.md", rules, bookmarks)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitionResult.Dependencies) != 0 {
		t.Fatalf("definition headings linked to themselves: %#v", definitionResult.Dependencies)
	}
	result, err := rewriteReferences(referenceBlocks, "references.md", rules, bookmarks)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 7 {
		t.Fatalf("generated dependencies = %#v", result.Dependencies)
	}
	data, err := json.Marshal(referenceBlocks)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"c":"-"`) || strings.Count(text, `"t":"Link"`) != 8 {
		t.Fatalf("rewritten AST did not preserve the range or expected links: %s", text)
	}
	for _, skipped := range []string{`https://example.com`, `"c":[["",[],[]],"OP01"],"t":"Code"`, `"c":["html","OP01"],"t":"RawInline"`, `"t":"Image"`} {
		if !strings.Contains(text, skipped) {
			t.Fatalf("skipped content changed or disappeared: %s in %s", skipped, text)
		}
	}
}

func TestRewriteReferencesUsesVisibleTextAcrossFormattingBoundaries(t *testing.T) {
	definitionBlocks, definitionRefs, _, err := transformHeadings("definitions.md", []any{heading(2, "op01", "OP01 - One")}, config.MemberConfig{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := prepareReferenceRules([]publication.ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", Unresolved: "error"}}, []preparedMember{{member: publication.Member{SourceKey: "definitions.md"}, blocks: definitionBlocks, refs: definitionRefs}})
	if err != nil {
		t.Fatal(err)
	}
	formatted := func(typeName, value string) map[string]any {
		return map[string]any{"t": typeName, "c": []any{referenceString(value)}}
	}
	blocks := []any{
		paragraph(formatted("Strong", "OP"), referenceString("01")),
		paragraph(referenceString("X"), formatted("Strong", "OP01")),
		paragraph(formatted("Strong", "OP01"), referenceString("X")),
		paragraph(formatted("Emph", "OP"), formatted("Strong", "01")),
		paragraph(map[string]any{"t": "Span", "c": []any{[]any{"span-id", []any{"marked"}, []any{}}, []any{referenceString("Before OP")}}}, referenceString("01")),
		paragraph(map[string]any{"t": "Code", "c": []any{[]any{"", []any{}, []any{}}, "X"}}, formatted("Strong", "OP01")),
	}
	result, err := rewriteReferences(blocks, "references.md", rules, map[string]string{definitionRefs[0].logical: "m_op01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 3 {
		t.Fatalf("formatted reference dependencies = %#v", result.Dependencies)
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"t":"Link"`) != 3 || strings.Count(string(data), "span-id") != 1 {
		t.Fatalf("formatted reference AST = %s", data)
	}
	if text := inlineText(blocks); text != "OP01XOP01OP01XOP01Before OP01XOP01" {
		t.Fatalf("visible text changed to %q", text)
	}
}

func TestPrepareReferenceRulesRejectsDuplicateAndMissingDefinitions(t *testing.T) {
	for _, test := range []struct {
		name     string
		headings []any
		message  string
	}{
		{name: "duplicate", headings: []any{heading(2, "one", "OP01 - One"), heading(2, "two", "OP01 - Duplicate")}, message: "duplicate definitions"},
		{name: "missing", headings: []any{heading(2, "one", "No package code")}, message: "no definition headings"},
	} {
		t.Run(test.name, func(t *testing.T) {
			blocks, refs, _, err := transformHeadings("definitions.md", test.headings, config.MemberConfig{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = prepareReferenceRules([]publication.ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", Unresolved: "error"}}, []preparedMember{{member: publication.Member{SourceKey: "definitions.md"}, blocks: blocks, refs: refs}})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("definition error = %v", err)
			}
		})
	}
}

func TestRewriteReferencesAppliesUnresolvedPolicies(t *testing.T) {
	definitionBlocks, definitionRefs, _, err := transformHeadings("definitions.md", []any{heading(2, "op01", "OP01 - One")}, config.MemberConfig{})
	if err != nil {
		t.Fatal(err)
	}
	bookmarks := map[string]string{definitionRefs[0].logical: "m_op01"}
	for _, policy := range []string{"error", "warning", "preserve"} {
		t.Run(policy, func(t *testing.T) {
			rules, ruleErr := prepareReferenceRules([]publication.ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", Unresolved: policy}}, []preparedMember{{member: publication.Member{SourceKey: "definitions.md"}, blocks: definitionBlocks, refs: definitionRefs}})
			if ruleErr != nil {
				t.Fatal(ruleErr)
			}
			blocks := []any{paragraph(referenceString("OP99"), map[string]any{"t": "Space"}, referenceString("OP99"))}
			result, rewriteErr := rewriteReferences(blocks, "references.md", rules, bookmarks)
			if policy == "error" {
				if rewriteErr == nil || !strings.Contains(rewriteErr.Error(), `key "OP99"`) {
					t.Fatalf("unresolved error = %v", rewriteErr)
				}
				return
			}
			if rewriteErr != nil {
				t.Fatal(rewriteErr)
			}
			if policy == "warning" && len(result.Issues) != 1 {
				t.Fatalf("warning issues = %#v", result.Issues)
			}
			if policy == "preserve" && len(result.Issues) != 0 {
				t.Fatalf("preserve issues = %#v", result.Issues)
			}
		})
	}
}

func TestRewriteReferencesAppliesRulesInOrderWithoutRelinking(t *testing.T) {
	definitionBlocks, definitionRefs, _, err := transformHeadings("definitions.md", []any{heading(2, "op01", "OP01 - One")}, config.MemberConfig{})
	if err != nil {
		t.Fatal(err)
	}
	configured := []publication.ReferenceLink{
		{ID: "first", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", Unresolved: "error"},
		{ID: "second", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "definitions.md", Unresolved: "error"},
	}
	rules, err := prepareReferenceRules(configured, []preparedMember{{member: publication.Member{SourceKey: "definitions.md"}, blocks: definitionBlocks, refs: definitionRefs}})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []any{paragraph(referenceString("OP01"))}
	result, err := rewriteReferences(blocks, "references.md", rules, map[string]string{definitionRefs[0].logical: "m_op01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("later rule relinked generated content: %#v", result.Dependencies)
	}
}
