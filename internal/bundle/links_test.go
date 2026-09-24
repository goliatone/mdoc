package bundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
)

func TestAssembleRewritesInternalLinksAndResolvesMemberAssets(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "one", "member.md")
	secondPath := filepath.Join(root, "two", "member.md")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("member"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "logo.png"), []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstBlocks := []any{
		heading(1, "same", "Same"),
		paragraph(link("#same"), link("../two/member.md#same"), link("../two/member.md"), link("https://example.com"), imageNode("logo.png")),
	}
	secondBlocks := []any{heading(1, "same", "Same"), paragraph(imageNode("logo.png"))}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", FragmentPolicy: "error", AllowedRoots: []string{root}, Members: []publication.Member{
		{SourceKey: "one/member.md", Document: &document.Document{Path: firstPath}},
		{SourceKey: "two/member.md", Document: &document.Document{Path: secondPath}},
	}}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{firstPath: encodeDocument(t, firstBlocks), secondPath: encodeDocument(t, secondBlocks)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Assets) != 2 || result.Assets[0].Path == result.Assets[1].Path {
		t.Fatalf("assets = %#v", result.Assets)
	}
	if len(result.Topology.Dependencies) != 5 {
		t.Fatalf("dependencies = %#v", result.Topology.Dependencies)
	}
	firstHeading := result.Topology.Headings[0].ID
	secondHeading := result.Topology.Headings[1].ID
	if result.Topology.Dependencies[0].Target != "#"+firstHeading || result.Topology.Dependencies[1].Target != "#"+secondHeading || result.Topology.Dependencies[2].Target != "#"+result.Topology.Markers[1].ID {
		t.Fatalf("rewritten links = %#v", result.Topology.Dependencies[:3])
	}
	if strings.Contains(string(result.JSON), "https://example.com") == false {
		t.Fatal("external URL was not preserved")
	}
	if err := VerifyTopology(result.Topology, result.JSON); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(result.JSON), "#"+secondHeading, "#changed", 1)
	if err := VerifyTopology(result.Topology, []byte(changed)); err == nil || !strings.Contains(err.Error(), "dependency") {
		t.Fatalf("topology error = %v", err)
	}
}

func TestAssembleResolvesExternalPublicationTargets(t *testing.T) {
	root := t.TempDir()
	memberPath := filepath.Join(root, "member.md")
	externalPath := filepath.Join(root, "external.md")
	if err := os.WriteFile(externalPath, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	ast := encodeDocument(t, []any{paragraph(link("external.md#section"))})
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, AllowedRoots: []string{root}, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: memberPath}}}}
	result, err := AssembleWithOptions(context.Background(), target, staticRunner{values: map[string][]byte{memberPath: ast}}, AssembleOptions{
		PathTargets:     map[string]string{canonicalPath(externalPath): "source:external.md"},
		ExternalTargets: map[string]string{"source:external.md": "https://pending.example/doc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ExternalLinks) != 1 || !result.ExternalLinks[0].HadFragment || result.Topology.Dependencies[0].Target != "https://pending.example/doc" {
		t.Fatalf("external links = %#v, dependencies = %#v", result.ExternalLinks, result.Topology.Dependencies)
	}
}

func TestAssembleAppliesMissingAndAmbiguousFragmentPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member.md")
	ast := encodeDocument(t, []any{heading(1, "duplicate", "One"), heading(2, "duplicate", "Two"), paragraph(link("#duplicate"), link("#missing"))})
	for _, policy := range []string{"error", "warning_root", "preserve"} {
		t.Run(policy, func(t *testing.T) {
			target := &publication.Publication{ID: "report", Kind: publication.KindBundle, HeadingPolicy: "error", FragmentPolicy: policy, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
			result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: ast}})
			if policy == "error" {
				if err == nil || !strings.Contains(err.Error(), "did not resolve uniquely") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy == "warning_root" && len(result.Issues) != 2 {
				t.Fatalf("issues = %#v", result.Issues)
			}
			if policy == "preserve" && (result.Topology.Dependencies[0].Target != "#duplicate" || result.Topology.Dependencies[1].Target != "#missing") {
				t.Fatalf("dependencies = %#v", result.Topology.Dependencies)
			}
		})
	}
}

func TestVerifyTopologyRejectsIntroducedImage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "member.md")
	asset := filepath.Join(root, "added.png")
	if err := os.WriteFile(asset, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := &publication.Publication{ID: "report", Kind: publication.KindBundle, Members: []publication.Member{{SourceKey: "member.md", Document: &document.Document{Path: path}}}}
	result, err := Assemble(context.Background(), target, staticRunner{values: map[string][]byte{path: encodeDocument(t, []any{})}})
	if err != nil {
		t.Fatal(err)
	}
	memberBlocks := result.Document["blocks"].([]any)[0].(map[string]any)["c"].([]any)[1].([]any)
	memberBlocks = append(memberBlocks, paragraph(imageNode(filepath.ToSlash(asset))))
	result.Document["blocks"].([]any)[0].(map[string]any)["c"].([]any)[1] = memberBlocks
	changed, err := json.Marshal(result.Document)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTopology(result.Topology, changed); err == nil || !strings.Contains(err.Error(), "dependency count changed") {
		t.Fatalf("topology error = %v", err)
	}
}

func paragraph(nodes ...any) map[string]any { return map[string]any{"t": "Para", "c": nodes} }

func link(target string) map[string]any {
	return map[string]any{"t": "Link", "c": []any{[]any{"", []any{}, []any{}}, []any{map[string]any{"t": "Str", "c": "link"}}, []any{target, ""}}}
}

func imageNode(target string) map[string]any {
	return map[string]any{"t": "Image", "c": []any{[]any{"", []any{}, []any{}}, []any{map[string]any{"t": "Str", "c": "image"}}, []any{target, ""}}}
}
