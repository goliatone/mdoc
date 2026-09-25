package workingdraft_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	wd "github.com/goliatone/mdoc/workingdraft"
	"google.golang.org/api/docs/v1"
)

func parsedMarkdown(t *testing.T, body string) map[string]any {
	t.Helper()
	binary, err := exec.LookPath("pandoc")
	if err != nil {
		t.Skip("Pandoc is required for Markdown semantic regression checks")
	}
	cmd := exec.Command(binary, "-f", "commonmark_x", "-t", "json")
	cmd.Stdin = strings.NewReader(body)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}
func nodesOfType(value any, kind string) []map[string]any {
	var result []map[string]any
	switch v := value.(type) {
	case map[string]any:
		if v["t"] == kind {
			result = append(result, v)
		}
		result = append(result, nodesOfType(v["c"], kind)...)
	case []any:
		for _, item := range v {
			result = append(result, nodesOfType(item, kind)...)
		}
	}
	return result
}
func captureDocument(t *testing.T, d *docs.Document) wd.Snapshot {
	t.Helper()
	service, err := wd.New(wd.Options{Provider: &provider{doc: d}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Capture(context.Background(), wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func TestListStructureRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ids      []string
		depths   []int64
		start    int64
		starts   []float64
		topLists int
	}{
		{"adjacent restarts", []string{"a", "b"}, []int64{0, 0}, 1, []float64{1, 1}, 2},
		{"blank paragraph between lists", []string{"a", "b"}, []int64{0, 0}, 1, []float64{1, 1}, 2},
		{"wide parent marker", []string{"a", "a"}, []int64{0, 1}, 100, []float64{100, 1}, 1},
		{"nested restart", []string{"a", "a", "b"}, []int64{0, 1, 1}, 100, []float64{100, 1, 1}, 1},
		{"parent marker grows", []string{"a", "a", "a", "a"}, []int64{0, 1, 0, 1}, 99, []float64{99, 1, 1}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := document("unused")
			tab := d.Tabs[0].DocumentTab
			tab.Body.Content = nil
			tab.Lists = map[string]docs.List{}
			for index, id := range tc.ids {
				if tc.name == "blank paragraph between lists" && index == 1 {
					tab.Body.Content = append(tab.Body.Content, document("").Tabs[0].DocumentTab.Body.Content[1])
				}
				tab.Lists[id] = docs.List{ListProperties: &docs.ListProperties{NestingLevels: []*docs.NestingLevel{{GlyphType: "DECIMAL", GlyphFormat: "%0.", StartNumber: tc.start}, {GlyphType: "DECIMAL", GlyphFormat: "%1.", StartNumber: 1}}}}
				tab.Body.Content = append(tab.Body.Content, &docs.StructuralElement{Paragraph: &docs.Paragraph{Bullet: &docs.Bullet{ListId: id, NestingLevel: tc.depths[index]}, Elements: []*docs.ParagraphElement{{TextRun: &docs.TextRun{Content: "item\ncontinued\n"}}}}})
			}
			snapshot := captureDocument(t, d)
			if !snapshot.BodyUsable {
				t.Fatalf("blocked: %v", snapshot.Diagnostics)
			}
			blocks := parsedMarkdown(t, snapshot.NormalizedContent.Body)["blocks"].([]any)
			var starts []float64
			for _, node := range nodesOfType(blocks, "OrderedList") {
				starts = append(starts, node["c"].([]any)[0].([]any)[0].(float64))
			}
			top := 0
			for _, block := range blocks {
				if block.(map[string]any)["t"] == "OrderedList" {
					top++
				}
			}
			if !reflect.DeepEqual(starts, tc.starts) || top != tc.topLists || len(nodesOfType(blocks, "CodeBlock")) != 0 || len(nodesOfType(blocks, "LineBreak")) != len(tc.ids) {
				t.Fatalf("structure changed: starts=%v top=%d markdown=%q", starts, top, snapshot.NormalizedContent.Body)
			}
		})
	}
	d := document("orphan")
	d.Tabs[0].DocumentTab.Lists = map[string]docs.List{"a": {ListProperties: &docs.ListProperties{NestingLevels: []*docs.NestingLevel{{GlyphType: "DECIMAL", StartNumber: 1}, {GlyphType: "DECIMAL", StartNumber: 1}}}}}
	d.Tabs[0].DocumentTab.Body.Content[1].Paragraph.Bullet = &docs.Bullet{ListId: "a", NestingLevel: 1}
	if captureDocument(t, d).BodyUsable {
		t.Fatal("accepted nested item without a parent")
	}
}

func TestLinkDestinationsRoundTrip(t *testing.T) {
	for _, target := range []string{
		"https://example.com/?a=&copy;",
		"https://example.com/?a=&#169;&b=&amp;copy;",
		`https://example.com/?a=\&copy;`,
		`https://example.com/end\`,
		"https://example.com/?x=<tag>&y=(literal)&z=%26copy%3B",
		"mailto:user@example.com?subject=&copy;",
	} {
		t.Run(target, func(t *testing.T) {
			d := document("label")
			d.Tabs[0].DocumentTab.Body.Content[1].Paragraph.Elements[0].TextRun.TextStyle = &docs.TextStyle{Link: &docs.Link{Url: target}}
			snapshot := captureDocument(t, d)
			if !snapshot.BodyUsable {
				t.Fatalf("blocked: %v", snapshot.Diagnostics)
			}
			links := nodesOfType(parsedMarkdown(t, snapshot.NormalizedContent.Body)["blocks"], "Link")
			if len(links) != 1 {
				t.Fatalf("lost link: %q", snapshot.NormalizedContent.Body)
			}
			got := links[0]["c"].([]any)[2].([]any)[0].(string)
			if got != target {
				t.Fatalf("destination changed: got %q want %q", got, target)
			}
		})
	}
}

func TestMixedIndentationCannotBecomeCode(t *testing.T) {
	for _, prefix := range []string{" \t", "  \t", "   \t", "\t ", "    "} {
		t.Run(prefix, func(t *testing.T) {
			d := document(prefix + "body")
			if captureDocument(t, d).BodyUsable {
				t.Fatal("accepted code-like mixed indentation")
			}
			d = document("first\n" + prefix + "body")
			if captureDocument(t, d).BodyUsable {
				t.Fatal("accepted code-like indentation after a line break")
			}
		})
	}
	snapshot := captureDocument(t, document("body\ttext"))
	if !snapshot.BodyUsable {
		t.Fatalf("blocked inline tab: %v", snapshot.Diagnostics)
	}
	if len(nodesOfType(parsedMarkdown(t, snapshot.NormalizedContent.Body)["blocks"], "CodeBlock")) != 0 {
		t.Fatal("inline tab became code")
	}
}
