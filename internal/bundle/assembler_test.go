package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
)

type staticRunner struct{ values map[string][]byte }

func (runner staticRunner) PandocJSON(_ context.Context, path string) ([]byte, error) {
	return runner.values[path], nil
}

func TestAssemblePreservesMemberAndBlockOrder(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "one", "a.md")
	secondPath := filepath.Join(t.TempDir(), "two", "b.md")
	firstBlocks := []any{
		map[string]any{"t": "Para", "c": []any{}},
		map[string]any{"t": "BulletList", "c": []any{}},
		map[string]any{"t": "Table", "c": []any{}},
	}
	secondBlocks := []any{
		map[string]any{"t": "CodeBlock", "c": []any{[]any{"", []any{}, []any{}}, "code"}},
		map[string]any{"t": "Figure", "c": []any{}},
		map[string]any{"t": "Para", "c": []any{map[string]any{"t": "Image", "c": []any{}}}},
	}
	encode := func(blocks []any) []byte {
		data, err := json.Marshal(map[string]any{"pandoc-api-version": []any{1, 23, 1}, "meta": map[string]any{}, "blocks": blocks})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, Layout: config.LayoutConfig{Cover: config.CoverConfig{Enabled: true}, TableOfContents: config.TOCConfig{Enabled: true}}, Members: []publication.Member{
		{SourceKey: "docs/a.md", Document: &document.Document{Path: firstPath}, Config: config.MemberConfig{Start: "after_cover"}},
		{SourceKey: "appendix/b.md", Document: &document.Document{Path: secondPath}, Config: config.MemberConfig{Start: "new_page"}},
	}}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{firstPath: encode(firstBlocks), secondPath: encode(secondBlocks)}})
	if err != nil {
		t.Fatal(err)
	}
	blocks := result.Document["blocks"].([]any)
	if len(blocks) != 4 {
		t.Fatalf("assembled blocks = %#v", blocks)
	}
	firstMember := blocks[2].(map[string]any)["c"].([]any)[1].([]any)
	secondMember := blocks[3].(map[string]any)["c"].([]any)[1].([]any)
	if firstMember[0].(map[string]any)["t"] != "Para" || firstMember[2].(map[string]any)["t"] != "Table" || secondMember[0].(map[string]any)["t"] != "CodeBlock" || secondMember[2].(map[string]any)["t"] != "Para" {
		t.Fatalf("member block order changed: %#v %#v", firstMember, secondMember)
	}
	if got := []string{result.Topology.Markers[0].Kind, result.Topology.Markers[1].Kind, result.Topology.Markers[2].SourceKey, result.Topology.Markers[3].SourceKey}; strings.Join(got, ",") != "cover,table_of_contents,docs/a.md,appendix/b.md" {
		t.Fatalf("topology = %#v", result.Topology)
	}
	if len(result.ResourcePaths) != 2 || result.ResourcePaths[0] > result.ResourcePaths[1] {
		t.Fatalf("resource paths = %#v", result.ResourcePaths)
	}
	if err := VerifyTopology(result.Topology, result.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyTopologyRejectsProtectedMarkerChanges(t *testing.T) {
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, Members: []publication.Member{{SourceKey: "docs/a.md", Document: &document.Document{Path: "a.md"}}}}
	ast := []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[]}`)
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{"a.md": ast}})
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(result.JSON), result.Topology.Markers[0].ID, "m_changed", 1)
	if err := VerifyTopology(result.Topology, []byte(changed)); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("topology error = %v", err)
	}
}

func TestAssembleTitleModesAndHeadingAdjustments(t *testing.T) {
	tests := []struct {
		name   string
		config config.MemberConfig
		levels []int
		title  string
	}{
		{name: "document title", config: config.MemberConfig{TitleMode: "document_title"}, levels: []int{1}, title: "Title"},
		{name: "heading one", config: config.MemberConfig{TitleMode: "heading_1"}, levels: []int{1, 2}},
		{name: "heading two", config: config.MemberConfig{TitleMode: "heading_2"}, levels: []int{2, 3}},
		{name: "heading three", config: config.MemberConfig{TitleMode: "heading_3"}, levels: []int{3, 4}},
		{name: "heading four", config: config.MemberConfig{TitleMode: "heading_4"}, levels: []int{4, 5}},
		{name: "heading five", config: config.MemberConfig{TitleMode: "heading_5"}, levels: []int{5, 6}},
		{name: "heading six", config: config.MemberConfig{TitleMode: "heading_6"}, levels: nil},
		{name: "keep shifted", config: config.MemberConfig{TitleMode: "keep", HeadingAdjustment: 1}, levels: []int{2, 3}},
		{name: "remove shifted", config: config.MemberConfig{TitleMode: "remove", HeadingAdjustment: 1}, levels: []int{3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "member.md")
			ast := encodeDocument(t, []any{heading(1, "title", "Title"), heading(2, "section", "Section")})
			target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}, Config: test.config}}}
			result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: ast}})
			if test.config.TitleMode == "heading_6" {
				if err == nil || !strings.Contains(err.Error(), "unsupported H7") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Title != test.title {
				t.Fatalf("title = %q", result.Title)
			}
			levels := make([]int, 0, len(result.Topology.Headings))
			for _, item := range result.Topology.Headings {
				levels = append(levels, item.Level)
			}
			if strings.Trim(strings.ReplaceAll(strings.Trim(fmt.Sprint(levels), "[]"), " ", ","), " ") != strings.Trim(strings.ReplaceAll(strings.Trim(fmt.Sprint(test.levels), "[]"), " ", ","), " ") {
				t.Fatalf("levels = %#v, want %#v", levels, test.levels)
			}
		})
	}
}

func TestAssembleNamespacesStableWordSafeBookmarks(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "one.md")
	secondPath := filepath.Join(t.TempDir(), "two.md")
	ast := encodeDocument(t, []any{heading(1, "same", "Same heading")})
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Layout: config.LayoutConfig{TableOfContents: config.TOCConfig{Enabled: true, Depth: 1}}, Members: []publication.Member{
		{SourceKey: "資料/非常に長いメンバー名/one.md", Document: &document.Document{Path: firstPath}},
		{SourceKey: "資料/非常に長いメンバー名/two.md", Document: &document.Document{Path: secondPath}},
	}}
	runner := staticRunner{values: map[string][]byte{firstPath: ast, secondPath: ast}}
	first, err := Assemble(context.Background(), target, runner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Assemble(context.Background(), target, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Topology.Headings) != 2 || first.Topology.Headings[0].ID == first.Topology.Headings[1].ID {
		t.Fatalf("headings = %#v", first.Topology.Headings)
	}
	valid := regexp.MustCompile(`^m_[A-Za-z0-9_]+$`)
	for index, item := range first.Topology.Headings {
		if item.ID != second.Topology.Headings[index].ID || len(item.ID) > 40 || !valid.MatchString(item.ID) {
			t.Fatalf("bookmark = %q, repeated = %q", item.ID, second.Topology.Headings[index].ID)
		}
	}
}

func TestAssembleOmitsUnusedHeadingBookmarksWhenTOCIsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{heading(1, "one", "One"), heading(2, "two", "Two")})}})
	if err != nil {
		t.Fatal(err)
	}
	for _, heading := range result.Topology.Headings {
		if heading.ID != "" {
			t.Fatalf("unused heading has bookmark %q: %#v", heading.ID, result.Topology.Headings)
		}
	}
	if err := VerifyTopology(result.Topology, result.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestAssembleAppliesHeadingJumpPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	ast := encodeDocument(t, []any{heading(1, "one", "One"), heading(3, "three", "Three")})
	for _, policy := range []string{"error", "warning", "allow"} {
		t.Run(policy, func(t *testing.T) {
			target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: policy, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
			result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: ast}})
			if policy == "error" {
				if err == nil || !strings.Contains(err.Error(), "jumps") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (policy == "warning") != (len(result.Issues) == 1) {
				t.Fatalf("issues = %#v", result.Issues)
			}
		})
	}
}

func TestAssembleBuildsConfiguredCoverFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	visible := true
	hidden := false
	target := &publication.Publication{ID: "general-report", Kind: publication.KindBundle, Title: `{{ field "report.name" }}`, Layout: config.LayoutConfig{Cover: config.CoverConfig{Enabled: true, Subtitle: `For {{ field "organization" }}`, Prose: `{{ range field "notes" }}{{ . }} {{ end }}`, Fields: []config.CoverFieldConfig{
		{Key: "prepared", Label: "Prepared by"},
		{Key: "period.start", Label: "Start", Format: "date:Jan 2, 2006", StyleRole: "Cover Date", Visible: &visible},
		{Key: "missing", Label: "Optional", Optional: true},
		{Key: "hidden", Label: "Hidden", Visible: &hidden},
	}}}, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
	result, err := AssembleWithOptions(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{})}}, AssembleOptions{Fields: map[string]any{
		"report": map[string]any{"name": "Operations Report"}, "organization": "Example Org", "prepared": "Team", "period": map[string]any{"start": "2026-08-01"}, "notes": []any{"One", "Two"}, "hidden": "secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := inlineText(result.Document["blocks"])
	for _, expected := range []string{"Operations Report", "For Example Org", "One Two", "Prepared by", "Team", "Start", "Aug 1, 2026"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("cover text %q is missing from %q", expected, text)
		}
	}
	if strings.Contains(text, "Optional") || strings.Contains(text, "Hidden") || strings.Contains(text, "secret") {
		t.Fatalf("cover included hidden fields: %q", text)
	}
	if !bytes.Contains(result.JSON, []byte("Cover Field Grid")) || !bytes.Contains(result.JSON, []byte("Cover Date")) {
		t.Fatalf("cover roles are missing: %s", result.JSON)
	}
	command := exec.Command("pandoc", "--from=json", "--to=json")
	command.Stdin = bytes.NewReader(result.JSON)
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("Pandoc rejected cover AST: %v: %s", commandErr, output)
	}
}

func TestAssembleRejectsRequiredEmptyCoverField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, Layout: config.LayoutConfig{Cover: config.CoverConfig{Enabled: true, Fields: []config.CoverFieldConfig{{Key: "required", Label: "Required"}}}}, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
	_, err := AssembleWithOptions(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{})}}, AssembleOptions{Fields: map[string]any{"required": ""}})
	if err == nil || !strings.Contains(err.Error(), "required but empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestCoverUsesDocumentTitleFromFirstMember(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Layout: config.LayoutConfig{Cover: config.CoverConfig{Enabled: true}}, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}, Config: config.MemberConfig{TitleMode: "document_title"}}}}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{heading(1, "title", "Member Title")})}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Member Title" || target.Title != "" || !strings.Contains(inlineText(result.Document["blocks"]), "Member Title") {
		t.Fatalf("result title = %q, target title = %q, blocks = %#v", result.Title, target.Title, result.Document["blocks"])
	}
}

func TestFinalizeBuildsStaticLinkedTOCToConfiguredDepth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	layout := config.LayoutConfig{Cover: config.CoverConfig{Enabled: true}, TableOfContents: config.TOCConfig{Enabled: true, Mode: "static", Depth: 2}}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Layout: layout, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
	ast := encodeDocument(t, []any{heading(1, "one", "One"), heading(2, "two", "Two"), heading(3, "three", "Three")})
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: ast}})
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := Finalize(result.Topology, layout, result.JSON)
	if err != nil {
		t.Fatal(err)
	}
	finalDocument, err := decodeDocument(finalized)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inlineText(finalDocument["blocks"]), "Table of Contents") {
		t.Fatalf("default TOC title is missing: %s", finalized)
	}
	if result.Topology.Headings[2].ID != "" {
		t.Fatalf("heading outside TOC depth has bookmark %q", result.Topology.Headings[2].ID)
	}
	if bytes.Contains(finalized, []byte("mdoc_toc_content")) || bytes.Contains(finalized, []byte("mdoc_cover")) {
		t.Fatalf("final document retains a temporary bookmark: %s", finalized)
	}
	if !bytes.Contains(finalized, []byte("#"+result.Topology.Headings[0].ID)) || !bytes.Contains(finalized, []byte("#"+result.Topology.Headings[1].ID)) {
		t.Fatalf("final TOC = %s", finalized)
	}
	changed := strings.Replace(string(result.JSON), result.Topology.Headings[0].Text, "Changed", 1)
	if _, err := Finalize(result.Topology, layout, []byte(changed)); err == nil || !strings.Contains(err.Error(), "heading") {
		t.Fatalf("topology error = %v", err)
	}
}

func TestFinalizeBuildsCustomizedTOCSelection(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.md")
	secondPath := filepath.Join(t.TempDir(), "second.md")
	disabled := false
	layout := config.LayoutConfig{TableOfContents: config.TOCConfig{
		Enabled: true, Mode: "static", Title: "Report map", Depth: 1,
		Members: []config.TOCMemberConfig{
			{Source: "first.md", Depth: 2, IncludeHeadings: []string{"first", "detail"}, ExcludeHeadings: []string{"omitted"}},
			{Source: "second.md", Enabled: &disabled},
		},
	}}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Layout: layout, Members: []publication.Member{
		{SourceKey: "first.md", Document: &document.Document{Path: firstPath}},
		{SourceKey: "second.md", Document: &document.Document{Path: secondPath}},
	}}
	values := map[string][]byte{
		firstPath:  encodeDocument(t, []any{heading(1, "first", "First"), heading(2, "detail", "Detail"), heading(2, "omitted", "Omitted")}),
		secondPath: encodeDocument(t, []any{heading(1, "second", "Second")}),
	}
	result, err := Assemble(context.Background(), target, staticRunner{values: values})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Topology.Headings) != 4 || len(result.Topology.TOCHeadings) != 2 {
		t.Fatalf("topology headings = %#v, TOC headings = %#v", result.Topology.Headings, result.Topology.TOCHeadings)
	}
	finalized, err := Finalize(result.Topology, layout, result.JSON)
	if err != nil {
		t.Fatal(err)
	}
	finalDocument, err := decodeDocument(finalized)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inlineText(finalDocument["blocks"]), "Report map") {
		t.Fatalf("custom TOC title is missing: %s", finalized)
	}
	for _, text := range []string{"#" + result.Topology.Headings[0].ID, "#" + result.Topology.Headings[1].ID} {
		if !bytes.Contains(finalized, []byte(text)) {
			t.Fatalf("custom TOC is missing %q: %s", text, finalized)
		}
	}
	for _, heading := range result.Topology.Headings[2:] {
		if heading.ID != "" {
			t.Fatalf("heading excluded from TOC has bookmark %#v", heading)
		}
	}
}

func TestAssembleRejectsUnknownTOCHeadingID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	target := &publication.Publication{
		ID: "report", Kind: publication.KindBundle,
		Layout:  config.LayoutConfig{TableOfContents: config.TOCConfig{Enabled: true, Depth: 1, Members: []config.TOCMemberConfig{{Source: "member.md", IncludeHeadings: []string{"missing"}}}}},
		Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}},
	}
	_, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{heading(1, "present", "Present")})}})
	if err == nil || !strings.Contains(err.Error(), `unknown heading ID "missing"`) || !strings.Contains(err.Error(), `member "member.md"`) {
		t.Fatalf("unknown TOC heading error = %v", err)
	}
}

func TestAssembleRejectsAmbiguousTOCHeadingID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	for _, field := range []string{"include", "exclude"} {
		t.Run(field, func(t *testing.T) {
			selector := config.TOCMemberConfig{Source: "member.md"}
			if field == "include" {
				selector.IncludeHeadings = []string{"duplicate"}
			} else {
				selector.ExcludeHeadings = []string{"duplicate"}
			}
			target := &publication.Publication{
				ID: "report", Kind: publication.KindBundle,
				Layout:  config.LayoutConfig{TableOfContents: config.TOCConfig{Enabled: true, Depth: 2, Members: []config.TOCMemberConfig{selector}}},
				Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}},
			}
			_, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{
				heading(1, "duplicate", "First"), heading(2, "duplicate", "Second"),
			})}})
			if err == nil || !strings.Contains(err.Error(), `ambiguous heading ID "duplicate"`) || !strings.Contains(err.Error(), `member "member.md"`) || !strings.Contains(err.Error(), "found 2 retained headings") {
				t.Fatalf("ambiguous TOC heading error = %v", err)
			}
		})
	}
}

func TestFinalizePrunesCoverMarkerWhenTOCIsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	layout := config.LayoutConfig{Cover: config.CoverConfig{Enabled: true}}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, Layout: layout, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{heading(1, "one", "One")})}})
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := Finalize(result.Topology, layout, result.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(finalized, []byte("mdoc_cover")) {
		t.Fatalf("final document retains cover bookmark: %s", finalized)
	}
}

func TestAssembleRecordsBoundariesAndAppendixLabelsDeterministically(t *testing.T) {
	starts := []string{"continuous", "after_cover", "new_page", "odd_page", "new_section"}
	members := make([]publication.Member, 0, len(starts))
	values := map[string][]byte{}
	for index, start := range starts {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("%d.md", index))
		configValue := config.MemberConfig{Start: start}
		if index == len(starts)-1 {
			configValue.Appendix = true
			configValue.AppendixPrefix = "Annex"
			configValue.AppendixTitle = `{{ field "appendix_name" }}`
			configValue.Section.Orientation = "landscape"
		}
		members = append(members, publication.Member{SourceKey: fmt.Sprintf("%d.md", index), Document: &document.Document{Path: path}, Config: configValue})
		values[path] = encodeDocument(t, []any{heading(1, "title", "Source Title")})
	}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", Layout: config.LayoutConfig{Headings: config.HeadingLayoutConfig{MemberTitleLevel: 1}}, Members: members}
	first, err := AssembleWithOptions(context.Background(), target, staticRunner{values: values}, AssembleOptions{Fields: map[string]any{"appendix_name": "Evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := AssembleWithOptions(context.Background(), target, staticRunner{values: values}, AssembleOptions{Fields: map[string]any{"appendix_name": "Evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	for index, marker := range first.Topology.Markers {
		if marker.Start != starts[index] || marker.Boundary == "" || marker != second.Topology.Markers[index] {
			t.Fatalf("marker %d = %#v", index, marker)
		}
	}
	if first.Topology.Headings[len(first.Topology.Headings)-2].Text != "Annex Evidence" {
		t.Fatalf("appendix headings = %#v", first.Topology.Headings)
	}
}

func encodeDocument(t *testing.T, blocks []any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"pandoc-api-version": []any{1, 23, 1}, "meta": map[string]any{}, "blocks": blocks})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func heading(level int, id, text string) map[string]any {
	return map[string]any{"t": "Header", "c": []any{level, []any{id, []any{}, []any{}}, []any{map[string]any{"t": "Str", "c": text}}}}
}
