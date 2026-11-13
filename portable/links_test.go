package portable

import (
	"context"
	"strings"
	"testing"
)

func TestLinkRewriteKeepsSelectorsAndCode(t *testing.T) {
	ctx := context.Background()
	s, _ := New(Options{})
	targetModel, _ := s.converter.Parse(ctx, []byte("# Design\n\n## Details\n\n## Details\n"))
	targetDoc := &localDocument{id: "TDD", path: "architecture/overview.md", capture: finalizedCapture{model: targetModel, evidence: Evidence{Source: SourceRef{FileID: "TDD", TabID: "tab1", Kind: "document"}, CanonicalURL: "https://docs.google.com/document/d/TDD/edit"}}}
	if e := s.prepareAnchors(ctx, targetDoc); e != nil {
		t.Fatal(e)
	}
	body := "[Design](https://docs.google.com/document/d/TDD/edit) [Known](https://docs.google.com/document/d/TDD/edit#details-1) [Missing](https://docs.google.com/document/d/TDD/edit#bookmark=missing) [Tab](https://docs.google.com/document/d/TDD/edit?tab=other) `https://docs.google.com/document/d/TDD/edit`"
	m, _ := s.converter.Parse(ctx, []byte(body))
	from := &localDocument{id: "ADR", path: "decisions/a.md", capture: finalizedCapture{model: m, evidence: Evidence{Source: SourceRef{FileID: "ADR"}}}}
	c := DefaultConfig()
	got, _, diag, e := s.rewriteDocument(ctx, from, c, map[string]*localDocument{"TDD": targetDoc}, map[string]string{"TDD": targetDoc.path}, newCatalog())
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"../architecture/overview.md", "../architecture/overview.md#details-1", "bookmark=missing", "?tab=other", "`https://docs.google.com/document/d/TDD/edit`"} {
		if !strings.Contains(string(got), want) {
			t.Fatal(want, string(got))
		}
	}
	if len(diag) != 2 {
		t.Fatal(diag)
	}
	if _, _, _, e = s.rewriteLink(ctx, "https://docs.google.com/document/d/TDD/edit#missing", from, map[string]*localDocument{"TDD": targetDoc}, map[string]string{"TDD": targetDoc.path}, newCatalog(), "error"); e == nil {
		t.Fatal("strict fragment accepted")
	}
}
func TestLiteralURLRemainsText(t *testing.T) {
	s, _ := New(Options{})
	m, e := s.converter.Parse(context.Background(), []byte("https://docs.google.com/document/d/DOC/edit\n\n[Explicit](https://docs.google.com/document/d/DOC/edit)"))
	if e != nil {
		t.Fatal(e)
	}
	links := 0
	walk(m, func(n map[string]any) {
		if n["t"] == "Link" {
			links++
		}
	})
	if links != 1 {
		t.Fatalf("bare literal became a link: %d", links)
	}
}
