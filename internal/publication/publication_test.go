package publication

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
)

func TestRepositoryBundleAndGenericSourceFixturesResolveTargets(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRuntime, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: filepath.Join(repositoryRoot, ".mdoc.yaml"), GlobalPath: filepath.Join(t.TempDir(), "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	repositoryConfig, repositoryProfile := repositoryRuntime.Legacy()
	repositoryGraph, err := document.Discover(context.Background(), repositoryConfig.Root, repositoryProfile, document.CommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	repositoryTargets := Resolve(repositoryRuntime.Version, repositoryProfile, repositoryGraph)
	if len(repositoryTargets.Items) != 1 || repositoryTargets.Entry == nil || repositoryTargets.Entry.ID != "engagement-report" || len(repositoryTargets.Entry.Members) != 13 {
		t.Fatalf("repository targets=%d entry=%#v", len(repositoryTargets.Items), repositoryTargets.Entry)
	}

	genericPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "generic", ".mdoc.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: genericPath, GlobalPath: filepath.Join(t.TempDir(), "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	genericConfig, genericProfile := runtimeConfig.Legacy()
	genericGraph, err := document.Discover(context.Background(), genericConfig.Root, genericProfile, document.CommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	genericTargets := Resolve(config.ProjectVersion2, genericProfile, genericGraph)
	if len(genericTargets.Items) != 2 || genericTargets.Entry == nil || genericTargets.Entry.ID != "docs/README.md" {
		t.Fatalf("generic targets=%d entry=%#v", len(genericTargets.Items), genericTargets.Entry)
	}
}

func TestResolveNormalizesLegacyDocumentsToSourceTargets(t *testing.T) {
	documents := []*document.Document{
		{SourceKey: "docs/a.md", Path: "/repo/docs/a.md", Title: "A"},
		{SourceKey: "docs/b.md", Path: "/repo/docs/b.md", Title: "B"},
	}
	graph := &document.Graph{Documents: documents, EntryKey: "docs/b.md"}

	for _, version := range []int{config.ProjectVersion1, config.ProjectVersion2} {
		resolved := Resolve(version, config.SelectedProfile{}, graph)
		if len(resolved.Items) != 2 || resolved.Items[0].Target != "source:docs/a.md" || resolved.Items[1].Target != "source:docs/b.md" {
			t.Fatalf("version %d targets = %#v", version, resolved.Items)
		}
		if resolved.Entry == nil || resolved.Entry.ID != "docs/b.md" || resolved.Entry.Explicit {
			t.Fatalf("version %d entry = %#v", version, resolved.Entry)
		}
	}
}

func TestResolveVersionThreeUsesOnlyExplicitPublications(t *testing.T) {
	root := t.TempDir()
	documents := []*document.Document{
		{SourceKey: "docs/a.md", Path: filepath.Join(root, "docs", "a.md"), Title: "A"},
		{SourceKey: "docs/b.md", Path: filepath.Join(root, "docs", "b.md"), Title: "B", Headings: []document.Heading{{Level: 1, Text: "Bundle Title"}}},
	}
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		EntryPublication: "bundle",
		Publications: map[string]config.PublicationConfig{
			"bundle": {
				Kind:           config.PublicationBundle,
				Members:        []config.MemberConfig{{Source: documents[1].Path, TitleMode: "document_title"}, {Source: documents[0].Path}},
				ReferenceLinks: []config.ReferenceLinkConfig{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, Definition: config.ReferenceDefinitionConfig{Source: documents[1].Path, Match: "heading_prefix"}, Unresolved: "error"}},
			},
		},
	}}
	resolved := Resolve(config.ProjectVersion3, profile, &document.Graph{Root: root, AllowedRoots: []string{root}, Documents: documents})
	if len(resolved.Items) != 1 || resolved.Entry == nil || resolved.Entry.ID != "bundle" {
		t.Fatalf("explicit publications = %#v", resolved)
	}
	if got := []string{resolved.Entry.Members[0].SourceKey, resolved.Entry.Members[1].SourceKey}; strings.Join(got, ",") != "docs/b.md,docs/a.md" {
		t.Fatalf("member order = %#v", got)
	}
	if _, exists := resolved.ByTarget["source:docs/a.md"]; exists {
		t.Fatal("version 3 created an implicit member target")
	}
	if resolved.Entry.Title != "Bundle Title" {
		t.Fatalf("derived title = %q", resolved.Entry.Title)
	}
	if len(resolved.Entry.ReferenceLinks) != 1 || resolved.Entry.ReferenceLinks[0].DefinitionSourceKey != "docs/b.md" {
		t.Fatalf("resolved reference links = %#v", resolved.Entry.ReferenceLinks)
	}
}

func TestResolveRejectsReferenceDefinitionOutsideBundle(t *testing.T) {
	root := t.TempDir()
	included := &document.Document{SourceKey: "docs/a.md", Path: filepath.Join(root, "docs", "a.md"), Title: "A"}
	definition := &document.Document{SourceKey: "docs/definitions.md", Path: filepath.Join(root, "docs", "definitions.md"), Title: "Definitions"}
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		EntryPublication: "bundle",
		Publications: map[string]config.PublicationConfig{
			"bundle": {
				Kind:           config.PublicationBundle,
				Members:        []config.MemberConfig{{Source: included.Path}},
				ReferenceLinks: []config.ReferenceLinkConfig{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, Definition: config.ReferenceDefinitionConfig{Source: definition.Path, Match: "heading_prefix"}, Unresolved: "error"}},
			},
		},
	}}
	resolved := Resolve(config.ProjectVersion3, profile, &document.Graph{Root: root, AllowedRoots: []string{root}, Documents: []*document.Document{included, definition}})
	if len(resolved.Issues) != 1 || resolved.Issues[0].Code != "publication_reference_source_not_member" {
		t.Fatalf("reference definition issues = %#v", resolved.Issues)
	}
}

func TestResolveNormalizesTOCMemberSourcesAndReportsUnknownMembers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "a.md")
	documentA := &document.Document{SourceKey: "docs/a.md", Path: path, Title: "A", Headings: []document.Heading{{Level: 1, ID: "a", Text: "A"}}}
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		EntryPublication: "bundle",
		Publications: map[string]config.PublicationConfig{
			"bundle": {
				Kind:    config.PublicationBundle,
				Members: []config.MemberConfig{{Source: path}},
				Layout: config.LayoutConfig{TableOfContents: config.TOCConfig{Enabled: true, Depth: 1, Members: []config.TOCMemberConfig{
					{Source: path}, {Source: filepath.Join(root, "docs", "missing.md")},
				}}},
			},
		},
	}}
	resolved := Resolve(config.ProjectVersion3, profile, &document.Graph{Root: root, AllowedRoots: []string{root}, Documents: []*document.Document{documentA}})
	if got := resolved.ByID["bundle"].Layout.TableOfContents.Members[0].Source; got != "docs/a.md" {
		t.Fatalf("normalized TOC source = %q", got)
	}
	if len(resolved.Issues) != 1 || resolved.Issues[0].Code != "publication_toc_member_missing" {
		t.Fatalf("TOC member issues = %#v", resolved.Issues)
	}
}

func TestResolveExplicitSourceTargetsKeepPublicationIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "a.md")
	documentA := &document.Document{SourceKey: "docs/a.md", Path: path, Title: "A"}
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		EntryPublication: "first",
		Publications: map[string]config.PublicationConfig{
			"first":  {Kind: config.PublicationSource, Source: path},
			"second": {Kind: config.PublicationSource, Source: path},
		},
	}}
	resolved := Resolve(config.ProjectVersion3, profile, &document.Graph{Root: root, AllowedRoots: []string{root}, Documents: []*document.Document{documentA}})
	if len(resolved.Issues) != 0 || resolved.ByID["first"].Target != "publication:first" || resolved.ByID["second"].Target != "publication:second" {
		t.Fatalf("source publications = %#v issues=%#v", resolved.Items, resolved.Issues)
	}
	if resolved.ByID["first"].Source != resolved.ByID["second"].Source {
		t.Fatal("source publications did not resolve to the same source input")
	}
}

func TestExplicitTargetIdentityDoesNotDependOnSourcePath(t *testing.T) {
	if ExplicitTargetKey("report") != ExplicitTargetKey("report") {
		t.Fatal("same publication ID produced different target keys")
	}
	if ExplicitTargetKey("report") == ExplicitTargetKey("report-two") {
		t.Fatal("different publication IDs produced the same target key")
	}
	if ExplicitTargetKey("docs-a") == LegacySourceTargetKey("docs-a") {
		t.Fatal("explicit and legacy target namespaces collided")
	}
}

func TestResolveAggregatesInvalidBundleMembership(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	excluded := filepath.Join(docs, "excluded.md")
	if err := os.WriteFile(excluded, []byte("# Excluded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	included := &document.Document{SourceKey: "docs/a.md", Path: filepath.Join(docs, "a.md"), Title: "A"}
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		EntryPublication: "bundle",
		Publications: map[string]config.PublicationConfig{
			"bundle": {Kind: config.PublicationBundle, Members: []config.MemberConfig{
				{Source: included.Path}, {Source: included.Path}, {Source: excluded}, {Source: filepath.Join(docs, "missing.md")}, {Source: filepath.Join(root, "..", "outside.md")},
			}},
		},
	}}
	resolved := Resolve(config.ProjectVersion3, profile, &document.Graph{Root: root, AllowedRoots: []string{root}, Documents: []*document.Document{included}})
	codes := map[string]bool{}
	for _, issue := range resolved.Issues {
		codes[issue.Code] = true
	}
	for _, code := range []string{"publication_member_duplicate", "publication_source_excluded", "publication_source_missing", "publication_source_unsafe"} {
		if !codes[code] {
			t.Fatalf("missing %s in %#v", code, resolved.Issues)
		}
	}
}
