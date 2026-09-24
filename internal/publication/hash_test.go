package publication

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/render"
)

func TestBundleSourceHashIsOrderedAndScoped(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.md")
	secondPath := filepath.Join(root, "second.md")
	unrelatedPath := filepath.Join(root, "unrelated.md")
	for path, data := range map[string]string{firstPath: "first", secondPath: "second", unrelatedPath: "unrelated"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := Member{SourceKey: "first.md", Document: &document.Document{Path: firstPath}}
	second := Member{SourceKey: "second.md", Document: &document.Document{Path: secondPath}}
	target := &Publication{ID: "report", Kind: KindBundle, Members: []Member{first, second}}
	baseline, err := SourceHash(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelatedPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated, err := SourceHash(target)
	if err != nil || unrelated != baseline {
		t.Fatalf("unrelated hash = %q, error = %v", unrelated, err)
	}
	target.Members = []Member{second, first}
	reordered, err := SourceHash(target)
	if err != nil || reordered == baseline {
		t.Fatalf("reordered hash = %q, error = %v", reordered, err)
	}
	target.Members = []Member{first, second}
	if err := os.WriteFile(firstPath, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited, err := SourceHash(target)
	if err != nil || edited == baseline {
		t.Fatalf("edited hash = %q, error = %v", edited, err)
	}
}

func TestRenderHashPartsCoverBundleInputsAndCanonicalMaps(t *testing.T) {
	root := t.TempDir()
	fieldFile := filepath.Join(root, "fields.json")
	asset := filepath.Join(root, "image.png")
	if err := os.WriteFile(fieldFile, []byte(`{"client":"A"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := &Publication{ID: "report", Kind: KindBundle, Title: "Report", FragmentPolicy: "error", HeadingPolicy: "error", Layout: config.LayoutConfig{TableOfContents: config.TOCConfig{Enabled: true, Depth: 2}}, Members: []Member{{SourceKey: "first.md", Config: config.MemberConfig{Start: "new_page"}}}, ReferenceLinks: []ReferenceLink{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, DefinitionSourceKey: "first.md", DefinitionMatch: "heading_prefix", Unresolved: "error"}}}
	options := RenderPartOptions{Fields: map[string]any{"b": 2, "a": 1}, FieldFiles: []HashFile{{Label: "fields.json", Path: fieldFile}}, StableComputed: map[string]any{"date": "2026-08-01"}, Assets: []HashFile{{Label: "first.md:image.png", Path: asset}}, ExternalTargets: map[string]string{"source:other.md": "https://example.com/doc"}}
	parts, err := RenderHashParts(target, "sha256:source", options)
	if err != nil {
		t.Fatal(err)
	}
	base := render.HashInput{Source: []byte("assembled"), PandocVersion: "3.10.1", RendererVersion: "2", Reader: "json", Parts: parts}
	baseline := render.ComputeHash(base)
	target.ReviewPullEnabled = true
	reviewParts, err := RenderHashParts(target, "sha256:source", options)
	if err != nil {
		t.Fatal(err)
	}
	if render.ComputeHash(render.HashInput{Source: base.Source, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion, Reader: base.Reader, Parts: reviewParts}) != baseline {
		t.Fatal("review pull setting changed render hash")
	}
	canonical := options
	canonical.Fields = map[string]any{"a": 1, "b": 2}
	canonicalParts, err := RenderHashParts(target, "sha256:source", canonical)
	if err != nil {
		t.Fatal(err)
	}
	if render.ComputeHash(render.HashInput{Source: base.Source, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion, Reader: base.Reader, Parts: canonicalParts}) != baseline {
		t.Fatal("map insertion order changed render hash")
	}
	mutations := []func(){
		func() { options.Fields = map[string]any{"a": 3} },
		func() { options.StableComputed = map[string]any{"date": "2026-08-02"} },
		func() { options.ExternalTargets = map[string]string{"source:other.md": "https://example.com/changed"} },
		func() { target.Layout.TableOfContents.Depth = 3 },
		func() { target.Layout.TableOfContents.Title = "Contents" },
		func() {
			target.Layout.TableOfContents.Members = []config.TOCMemberConfig{{Source: "first.md", Depth: 1}}
		},
		func() { target.Members[0].Config.Start = "odd_page" },
		func() { target.ReferenceLinks[0].Pattern = `\bPKG[0-9]{2}\b` },
		func() { _ = os.WriteFile(fieldFile, []byte(`{"client":"B"}`), 0o600) },
		func() { _ = os.WriteFile(asset, []byte("changed"), 0o600) },
	}
	for index, mutate := range mutations {
		copyOptions := options
		copyTarget := *target
		copyTarget.Members = append([]Member(nil), target.Members...)
		copyTarget.ReferenceLinks = append([]ReferenceLink(nil), target.ReferenceLinks...)
		options = copyOptions
		target = &copyTarget
		mutate()
		changedParts, buildErr := RenderHashParts(target, "sha256:source", options)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		changed := base
		changed.Parts = changedParts
		if render.ComputeHash(changed) == baseline {
			t.Fatalf("mutation %d did not change render hash", index)
		}
		options = canonical
		target.Layout.TableOfContents.Depth = 2
		target.Layout.TableOfContents.Title = ""
		target.Layout.TableOfContents.Members = nil
		target.Members[0].Config.Start = "new_page"
		target.ReferenceLinks[0].Pattern = `\bOP[0-9]{2}\b`
		if err := os.WriteFile(fieldFile, []byte(`{"client":"A"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(asset, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderHashPartsUsesLoadedFieldFileBytes(t *testing.T) {
	target := &Publication{ID: "report", Kind: KindBundle}
	parts, err := RenderHashParts(target, "sha256:source", RenderPartOptions{
		FieldFiles: []HashFile{{Label: "fields.json", Path: "relative/path/that/must/not/be-opened.json", Data: []byte(`{"large":9007199254740993}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 5 || string(parts[4].Data) != `{"large":9007199254740993}` {
		t.Fatalf("field hash parts = %#v", parts)
	}
}
