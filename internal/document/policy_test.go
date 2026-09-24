package document

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyAllowsNoH1AndUsesFileName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-notes.md")
	document, issues, err := ParsePandocJSONWithPolicy("release-notes.md", path, []byte("Body\n"), []byte(`{"blocks":[{"t":"Para","c":[]}]}`), Policy{
		Title: "optional", HeadingJumps: "allow", NamingSource: "file", MaxTableColumns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "release-notes" || hasIssueCode(issues, "title_count") || hasIssueCode(issues, "document_name") {
		t.Fatalf("document=%#v issues=%#v", document, issues)
	}
}

func TestPolicyControlsHeadingJumpSeverity(t *testing.T) {
	ast := []byte(`{"blocks":[{"t":"Header","c":[1,["",[],[]],[{"t":"Str","c":"Title"}]]},{"t":"Header","c":[3,["",[],[]],[{"t":"Str","c":"Deep"}]]}]}`)
	for _, test := range []struct {
		policy string
		want   Severity
	}{
		{"error", SeverityError},
		{"warning", SeverityWarning},
		{"allow", ""},
	} {
		t.Run(test.policy, func(t *testing.T) {
			_, issues, err := ParsePandocJSONWithPolicy("doc.md", "doc.md", []byte("# Title"), ast, Policy{Title: "required", HeadingJumps: test.policy, NamingSource: "h1"})
			if err != nil {
				t.Fatal(err)
			}
			severity := issueSeverity(issues, "heading_jump")
			if severity != test.want {
				t.Fatalf("heading severity = %q, want %q; issues=%#v", severity, test.want, issues)
			}
		})
	}
}

func TestPolicyNamesFromFrontMatterAndMapping(t *testing.T) {
	ast := []byte(`{"blocks":[]}`)
	front, issues, err := ParsePandocJSONWithPolicy("front.md", "front.md", []byte("---\ntitle: Front Title\n---\nBody\n"), ast, Policy{Title: "optional", NamingSource: "front_matter"})
	if err != nil || front.Title != "Front Title" || hasIssueCode(issues, "document_name") {
		t.Fatalf("front matter document=%#v issues=%#v err=%v", front, issues, err)
	}
	mapped, issues, err := ParsePandocJSONWithPolicy("mapped.md", "mapped.md", []byte("Body\n"), ast, Policy{Title: "optional", NamingSource: "mapping", NameMapping: map[string]string{"mapped.md": "Mapped Title"}})
	if err != nil || mapped.Title != "Mapped Title" || hasIssueCode(issues, "document_name") {
		t.Fatalf("mapped document=%#v issues=%#v err=%v", mapped, issues, err)
	}
}

func TestPolicyControlsUnpublishedLinksAndImageTypes(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.md")
	targetPath := filepath.Join(root, "target.md")
	imagePath := filepath.Join(root, "diagram.gif")
	for _, path := range []string{sourcePath, targetPath, imagePath} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	document := &Document{SourceKey: "source.md", Path: sourcePath}
	for _, test := range []struct {
		policy string
		want   Severity
	}{
		{"error", SeverityError},
		{"warning", SeverityWarning},
		{"preserve", ""},
	} {
		graph := &Graph{Root: root, ByKey: map[string]*Document{"source.md": document}, Policy: Policy{UnpublishedMarkdownLinks: test.policy, ImageTypes: map[string]bool{"gif": true}}}
		validateLink(graph, document, Link{Target: "target.md"})
		if got := issueSeverity(graph.Issues, "unpublished_link"); got != test.want {
			t.Fatalf("link policy %s severity=%q issues=%#v", test.policy, got, graph.Issues)
		}
		validateLink(graph, document, Link{Target: "diagram.gif", Image: true})
		if hasIssueCode(graph.Issues, "unsupported_asset") {
			t.Fatalf("configured GIF type was rejected: %#v", graph.Issues)
		}
	}
}

func TestPolicyRejectsNonLocalImageSourcesAndAllowsRemoteLinks(t *testing.T) {
	root := t.TempDir()
	document := &Document{SourceKey: "source.md", Path: filepath.Join(root, "source.md")}
	for _, target := range []string{
		"file:///tmp/secret.png",
		"https://example.com/image.png",
		"data:image/png;base64,AAAA",
		"//example.com/image.png",
	} {
		graph := &Graph{Root: root, Policy: Policy{ImageTypes: map[string]bool{"png": true}}}
		validateLink(graph, document, Link{Target: target, Image: true})
		if !hasIssueCode(graph.Issues, "unsafe_asset_source") {
			t.Fatalf("image source %q was accepted: %#v", target, graph.Issues)
		}
	}

	graph := &Graph{Root: root}
	validateLink(graph, document, Link{Target: "https://example.com/page"})
	if len(graph.Issues) != 0 {
		t.Fatalf("remote hyperlink was rejected: %#v", graph.Issues)
	}
}

func TestAssetPathsUsesApprovedRelativeImagePath(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(sourceDir, "image.png")
	if err := os.WriteFile(asset, []byte("source asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	document := &Document{SourceKey: "docs/source.md", Path: filepath.Join(sourceDir, "source.md"), Links: []Link{{Target: "image.png", Image: true}, {Target: "https://example.com/image.png", Image: true}}}
	graph := &Graph{Root: root, Policy: Policy{ImageTypes: map[string]bool{"png": true}}}
	validateLink(graph, document, document.Links[0])
	if len(graph.Issues) != 0 {
		t.Fatalf("approved local asset was rejected: %#v", graph.Issues)
	}
	paths := graph.AssetPaths(document)
	if len(paths) != 1 || paths[0] != asset {
		t.Fatalf("asset paths = %#v, want %q", paths, asset)
	}
}

func hasIssueCode(issues []Issue, code string) bool { return issueSeverity(issues, code) != "" }

func issueSeverity(issues []Issue, code string) Severity {
	for _, issue := range issues {
		if issue.Code == code {
			return issue.Severity
		}
	}
	return ""
}
