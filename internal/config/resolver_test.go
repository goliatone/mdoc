package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveProviderPrecedenceAndOrigins(t *testing.T) {
	root := t.TempDir()
	globalDir := filepath.Join(root, "global")
	projectDir := filepath.Join(root, "project")
	workingDir := filepath.Join(root, "work")
	for _, directory := range []string{globalDir, projectDir, workingDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	globalPath := filepath.Join(globalDir, "config.yaml")
	projectPath := filepath.Join(projectDir, ".mdoc.yaml")
	writeTestFile(t, globalPath, `version: 1
default_profile: review
pandoc_binary: global-pandoc
output:
  format: json
  quiet: true
profile_defaults:
  entry: global-entry.md
  sources:
    include: [global.md]
    exclude: [drafts/**]
`)
	writeTestFile(t, projectPath, `version: 2
project_id: project-one
default_profile: review
root: .
pandoc_binary: project-pandoc
output:
  quiet: false
profiles:
  review:
    sources:
      include: [docs/*.md]
      exclude: []
    entry: docs/README.md
    validation:
      max_table_columns: 0
`)

	resolved, err := Resolve(context.Background(), ResolverOptions{
		WorkingDir:  workingDir,
		ProjectPath: projectPath,
		GlobalPath:  globalPath,
		Environment: map[string]string{
			"MDOC_PANDOC_BINARY":     "env-pandoc",
			"MDOC_OUTPUT_QUIET":      "true",
			"MDOC_SOURCES":           `["env.md"]`,
			"MDOC_EXCLUDES":          `[]`,
			"MDOC_MAX_TABLE_COLUMNS": "2",
		},
		Overrides: map[string]any{
			"pandoc_binary": "flag-pandoc",
			"output":        map[string]any{"quiet": false},
			"profile": map[string]any{
				"sources":    map[string]any{"include": []string{}},
				"validation": map[string]any{"max_table_columns": 0},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.PandocBinary != "flag-pandoc" {
		t.Fatalf("pandoc binary = %q", resolved.PandocBinary)
	}
	if resolved.Output.Quiet {
		t.Fatal("explicit false flag did not override true environment value")
	}
	if resolved.Output.Format != "json" {
		t.Fatalf("output format = %q", resolved.Output.Format)
	}
	if len(resolved.Profile.Sources.Include) != 0 {
		t.Fatalf("explicit empty source list was not preserved: %#v", resolved.Profile.Sources.Include)
	}
	if len(resolved.Profile.Sources.Exclude) != 0 {
		t.Fatalf("environment empty list was not preserved: %#v", resolved.Profile.Sources.Exclude)
	}
	if resolved.Profile.Validation.MaxTableColumns != 0 {
		t.Fatalf("explicit zero = %d", resolved.Profile.Validation.MaxTableColumns)
	}
	if origin := resolved.Origins["pandoc_binary"]; origin.Kind != OriginFlag || origin.Base != workingDir {
		t.Fatalf("pandoc origin = %#v", origin)
	}
	if origin := resolved.Origins["output.format"]; origin.Kind != OriginGlobal || origin.Path != globalPath {
		t.Fatalf("format origin = %#v", origin)
	}
}

func TestResolveStrictFileErrorsIdentifySource(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	globalPath := filepath.Join(root, "config.yaml")
	writeTestFile(t, projectPath, `version: 2
project_id: project-one
profiles:
  review:
    sources:
      include: [README.md]
`)
	writeTestFile(t, globalPath, "version: 1\nunknown_global_key: true\n")
	_, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: globalPath, Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), globalPath) || !strings.Contains(err.Error(), "unknown_global_key") {
		t.Fatalf("global strict error = %v", err)
	}

	writeTestFile(t, globalPath, "version: 1\n")
	writeTestFile(t, projectPath, `version: 2
project_id: project-one
unknown_project_key: true
profiles:
  review:
    sources:
      include: [README.md]
`)
	_, err = Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: globalPath, Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), projectPath) || !strings.Contains(err.Error(), "unknown_project_key") {
		t.Fatalf("project strict error = %v", err)
	}
}

func TestResolveUsesWinningPathBase(t *testing.T) {
	root := t.TempDir()
	globalDir := filepath.Join(root, "global")
	projectDir := filepath.Join(root, "project")
	workingDir := filepath.Join(root, "work")
	for _, directory := range []string{globalDir, projectDir, workingDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	globalPath := filepath.Join(globalDir, "config.yaml")
	projectPath := filepath.Join(projectDir, ".mdoc.yaml")
	writeTestFile(t, globalPath, "version: 1\nprofile_defaults:\n  style:\n    reference_docx: style.docx\n")
	writeTestFile(t, projectPath, `version: 2
project_id: project-one
profiles:
  review:
    sources:
      include: [docs/*.md]
`)

	resolved, err := Resolve(context.Background(), ResolverOptions{WorkingDir: workingDir, ProjectPath: projectPath, GlobalPath: globalPath, Environment: map[string]string{"MDOC_ENTRY": "README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Profile.Style.ReferenceDOCX != filepath.Join(globalDir, "style.docx") {
		t.Fatalf("global reference path = %q", resolved.Profile.Style.ReferenceDOCX)
	}
	if resolved.Profile.Entry != filepath.Join(workingDir, "README.md") {
		t.Fatalf("environment entry path = %q", resolved.Profile.Entry)
	}
	if got := resolved.Profile.Sources.Include[0]; got != filepath.Join(projectDir, "docs", "*.md") {
		t.Fatalf("project source path = %q", got)
	}
}

func TestDiscoverProjectUsesNearestConfig(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, ".mdoc.yaml")
	nested := filepath.Join(root, "one", ".mdoc.yaml")
	working := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(working, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, outer, "outer")
	writeTestFile(t, nested, "nested")
	found, err := DiscoverProject(working)
	if err != nil {
		t.Fatal(err)
	}
	if found != nested {
		t.Fatalf("found = %q, want %q", found, nested)
	}
}

func TestResolveDiscoversProjectAndExplicitPathBypassesDiscovery(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	discovered := filepath.Join(root, ".mdoc.yaml")
	explicit := filepath.Join(root, "explicit.yaml")
	writeTestFile(t, discovered, `version: 2
project_id: discovered-project
profiles:
  review:
    sources:
      include: [README.md]
`)
	writeTestFile(t, explicit, `version: 2
project_id: explicit-project
profiles:
  review:
    sources:
      include: [README.md]
`)

	resolved, err := Resolve(context.Background(), ResolverOptions{WorkingDir: nested, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProjectID != "discovered-project" || resolved.ProjectPath != discovered {
		t.Fatalf("discovered config = %#v", resolved)
	}
	resolved, err = Resolve(context.Background(), ResolverOptions{WorkingDir: nested, ProjectPath: explicit, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProjectID != "explicit-project" || resolved.ProjectPath != explicit {
		t.Fatalf("explicit config = %#v", resolved)
	}
}

func TestResolveDirectModeUsesRootWithoutProjectIdentity(t *testing.T) {
	working := t.TempDir()
	root := filepath.Join(working, "content")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(context.Background(), ResolverOptions{
		WorkingDir:          working,
		AllowMissingProject: true,
		GlobalPath:          filepath.Join(working, "missing.yaml"),
		Environment:         map[string]string{},
		DirectSources:       []string{"README.md"},
		Root:                "content",
		Entry:               "README.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Direct || resolved.Identity() != "" || resolved.ProjectPath != "" || resolved.ProfileName != "direct" {
		t.Fatalf("direct identity = %#v", resolved)
	}
	if resolved.Root != root || resolved.Profile.Entry != filepath.Join(root, "README.md") || resolved.Profile.Sources.Include[0] != filepath.Join(root, "README.md") {
		t.Fatalf("direct paths = root %q entry %q sources %#v", resolved.Root, resolved.Profile.Entry, resolved.Profile.Sources.Include)
	}
}

func TestResolveKeepsOmittedEntryEmptyUntilSourceSelection(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeTestFile(t, projectPath, `version: 2
project_id: entry-inference
profiles:
  review:
    sources:
      include: [docs]
`)

	resolved, err := Resolve(context.Background(), ResolverOptions{
		ProjectPath: projectPath,
		Profile:     "review",
		GlobalPath:  filepath.Join(root, "missing.yaml"),
		Environment: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Profile.Entry != "" {
		t.Fatalf("project entry = %q", resolved.Profile.Entry)
	}

	direct, err := Resolve(context.Background(), ResolverOptions{
		WorkingDir:          root,
		AllowMissingProject: true,
		GlobalPath:          filepath.Join(root, "missing.yaml"),
		Environment:         map[string]string{},
		DirectSources:       []string{"docs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Profile.Entry != "" {
		t.Fatalf("direct entry = %q", direct.Profile.Entry)
	}
}

func TestResolvePreservesSyntheticExternalEntryKey(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeTestFile(t, projectPath, `version: 2
project_id: external-entry
profiles:
  review:
    sources:
      include: [README.md]
`)

	resolved, err := Resolve(context.Background(), ResolverOptions{
		ProjectPath: projectPath,
		Profile:     "review",
		Overrides: map[string]any{"profile": map[string]any{
			"entry": "external/1/entry.md",
		}},
		GlobalPath:  filepath.Join(root, "missing.yaml"),
		Environment: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Profile.Entry != "external/1/entry.md" {
		t.Fatalf("entry = %q", resolved.Profile.Entry)
	}
}

func TestResolveNormalizesVersionOneWithoutNeutralDefaultDrift(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	globalPath := filepath.Join(root, "global.yaml")
	writeTestFile(t, projectPath, `version: 1
workspace_id: momentum-documents
profiles:
  work:
    sources: [docs/*.md]
    entry: docs/README.md
    review_folder_name: Momentum Review
    staging_folder_name: Momentum Staging
    reference_docx: document-styles/work-reference.docx
    max_table_columns: 5
    state_file: ""
`)
	writeTestFile(t, globalPath, `version: 1
pandoc_binary: custom-pandoc
profile_defaults:
  validation:
    title: optional
    heading_jumps: warning
  style:
    reference_docx: neutral.docx
    heading_filter: none
  destination:
    staging_folder_name: Global Staging
    review_folder_name: Global Review
  state_file: global-state.json
`)
	resolved, err := Resolve(context.Background(), ResolverOptions{
		ProjectPath: projectPath,
		GlobalPath:  globalPath,
		Environment: map[string]string{"MDOC_OUTPUT_QUIET": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != 1 || resolved.Identity() != "momentum-documents" || resolved.ProfileName != "work" {
		t.Fatalf("legacy identity = %#v", resolved)
	}
	if resolved.Profile.Validation.Title != "required" || resolved.Profile.Validation.HeadingJumps != "error" || resolved.Profile.Validation.UnpublishedMarkdownLinks != "error" {
		t.Fatalf("legacy validation drifted: %#v", resolved.Profile.Validation)
	}
	if resolved.Profile.Style.ReferenceDOCX != filepath.Join(root, "document-styles", "work-reference.docx") || resolved.Profile.Style.HeadingFilter != "title" {
		t.Fatalf("legacy style drifted: %#v", resolved.Profile.Style)
	}
	if resolved.Profile.Destination.StagingFolderName != "Momentum Staging" || resolved.Profile.Destination.ReviewFolderName != "Momentum Review" {
		t.Fatalf("legacy destination drifted: %#v", resolved.Profile.Destination)
	}
	if resolved.Profile.StateFile != "" {
		t.Fatalf("legacy empty state path drifted to %q", resolved.Profile.StateFile)
	}
	if resolved.PandocBinary != "custom-pandoc" || !resolved.Output.Quiet {
		t.Fatalf("top level precedence did not apply: pandoc=%q output=%#v", resolved.PandocBinary, resolved.Output)
	}
}

func TestResolveRejectsUnsupportedProjectVersionBeforeRuntimeWork(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeTestFile(t, projectPath, "version: 99\n")
	_, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "unsupported version 99") || !strings.Contains(err.Error(), projectPath) {
		t.Fatalf("unsupported version error = %v", err)
	}
}

func TestResolveVersionThreePublicationsStrictly(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeTestFile(t, projectPath, `version: 3
project_id: generic-documents
default_profile: review
profiles:
  review:
    sources:
      include: [docs/*.md]
    entry: combined-report
    field_defaults:
      classification: Internal
    publications:
      source-copy:
        kind: source
        source: docs/one.md
      combined-report:
        kind: bundle
        members:
          - source: docs/one.md
            start: after_cover
            title_mode: document_title
          - source: docs/two.md
            start: new_page
            title_mode: heading_1
        reference_links:
          - id: operations
            pattern: '\bOP[0-9]{2}\b'
            definition:
              source: docs/two.md
              match: heading_prefix
            unresolved: error
        fields:
          nested:
            arbitrary: true
        layout:
          page:
            size: letter
            margins:
              top: 1in
              right: 1in
              bottom: 1in
              left: 1in
          table_of_contents:
            enabled: true
            mode: static
            title: Contents
            depth: 3
            members:
              - source: docs/one.md
                depth: 2
                exclude_headings: [references]
              - source: docs/two.md
                enabled: false
`)
	resolved, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != ProjectVersion3 || resolved.Profile.Entry != "combined-report" || len(resolved.Profile.Publications) != 2 {
		t.Fatalf("version 3 resolution = %#v", resolved)
	}
	bundle := resolved.Profile.Publications["combined-report"]
	if got := bundle.Members[0].Source; got != filepath.Join(root, "docs", "one.md") {
		t.Fatalf("resolved member source = %q", got)
	}
	if got := bundle.Layout.TableOfContents.Members[0].Source; got != filepath.Join(root, "docs", "one.md") {
		t.Fatalf("resolved TOC member source = %q", got)
	}
	if selector := bundle.Layout.TableOfContents.Members[1]; selector.Source != filepath.Join(root, "docs", "two.md") || selector.Enabled == nil || *selector.Enabled {
		t.Fatalf("resolved disabled TOC member = %#v", selector)
	}
	if got := bundle.ReferenceLinks[0].Definition.Source; got != filepath.Join(root, "docs", "two.md") {
		t.Fatalf("resolved reference definition source = %q", got)
	}

	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectPath, strings.Replace(string(data), "            unresolved: error", "            unresolved: error\n            unknown_reference_key: true", 1))
	_, err = Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), projectPath) || !strings.Contains(err.Error(), "unknown_reference_key") {
		t.Fatalf("strict reference rule error = %v", err)
	}
	writeTestFile(t, projectPath, strings.Replace(string(data), "depth: 3", "depth: 3\n            unknown_layout_key: true", 1))
	_, err = Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), projectPath) || !strings.Contains(err.Error(), "unknown_layout_key") {
		t.Fatalf("strict version 3 error = %v", err)
	}
}

func TestResolveVersionThreeRejectsAbsolutePublicationPaths(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeTestFile(t, projectPath, `version: 3
project_id: generic-documents
profiles:
  review:
    sources:
      include: [docs/*.md]
    entry: report
    publications:
      report:
        kind: source
        source: /tmp/report.md
`)
	_, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "stay relative") {
		t.Fatalf("absolute path error = %v", err)
	}
}

func TestResolveVersionThreeReviewPullIsStrictAndDefaultsDisabled(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	write := func(review string) {
		writeTestFile(t, projectPath, `version: 3
project_id: generic-documents
profiles:
  review:
    sources:
      include: [docs/*.md]
    entry: report
    publications:
      report:
        kind: source
        source: docs/report.md
`+review)
	}
	write("")
	resolved, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Profile.Publications["report"].Review.Pull.Enabled {
		t.Fatal("review pull defaulted to enabled")
	}
	write("        review:\n          pull:\n            enabled: true\n")
	resolved, err = Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err != nil || !resolved.Profile.Publications["report"].Review.Pull.Enabled {
		t.Fatalf("enabled review pull = %#v, %v", resolved, err)
	}
	write("        review:\n          pull:\n            enabled: true\n            unknown_review_key: true\n")
	_, err = Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "unknown_review_key") {
		t.Fatalf("strict review error = %v", err)
	}
}

func TestMomentumVersionOneCompatibilityFixture(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "compat", "momentum-v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(context.Background(), ResolverOptions{
		ProjectPath: fixture,
		GlobalPath:  filepath.Join(t.TempDir(), "missing.yaml"),
		Environment: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != ProjectVersion1 || resolved.Identity() != "momentum-electrical" || resolved.ProfileName != "work" {
		t.Fatalf("legacy identity changed: %#v", resolved)
	}
	if resolved.Profile.Validation.Title != "required" || resolved.Profile.Style.HeadingFilter != "title" || resolved.Profile.Destination.Mode != "immutable_review" {
		t.Fatalf("legacy policy changed: %#v", resolved.Profile)
	}
	if resolved.Profile.Destination.StagingFolderName != "Momentum Staging" || resolved.Profile.Destination.ReviewFolderName != "Momentum Review" {
		t.Fatalf("legacy destination changed: %#v", resolved.Profile.Destination)
	}
}

func TestResolveValidatesTrustedFilterRootsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	insideFilter := filepath.Join(root, "filters", "inside.lua")
	externalFilter := filepath.Join(external, "outside.lua")
	if err := os.MkdirAll(filepath.Dir(insideFilter), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, insideFilter, "return {}\n")
	writeTestFile(t, externalFilter, "return {}\n")

	writeFilterProject := func(filter string, styleRoots []string) {
		roots := "[]"
		if len(styleRoots) > 0 {
			roots = "[" + strings.Join(styleRoots, ", ") + "]"
		}
		writeTestFile(t, projectPath, `version: 2
project_id: project-one
profiles:
  review:
    sources:
      include: [README.md]
    style:
      filters: [`+filter+`]
      roots: `+roots+`
`)
	}
	writeFilterProject("filters/inside.lua", nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err != nil {
		t.Fatalf("inside filter: %v", err)
	}

	writeFilterProject(externalFilter, nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("outside filter error = %v", err)
	}

	writeFilterProject(externalFilter, []string{external})
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err != nil {
		t.Fatalf("approved external filter: %v", err)
	}

	link := filepath.Join(root, "filters", "linked.lua")
	if err := os.Symlink(externalFilter, link); err != nil {
		t.Fatal(err)
	}
	writeFilterProject("filters/linked.lua", nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("filter symlink error = %v", err)
	}
}

func TestResolveValidatesReferenceDOCXRootsWithoutRequiringAvailability(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	externalReference := filepath.Join(external, "reference.docx")
	writeTestFile(t, externalReference, "reference")

	writeReferenceProject := func(reference string, styleRoots []string) {
		roots := "[]"
		if len(styleRoots) > 0 {
			roots = "[" + strings.Join(styleRoots, ", ") + "]"
		}
		writeTestFile(t, projectPath, `version: 2
project_id: project-one
profiles:
  review:
    sources:
      include: [README.md]
    style:
      reference_docx: `+reference+`
      roots: `+roots+`
`)
	}

	writeReferenceProject("missing-reference.docx", nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err != nil {
		t.Fatalf("missing in-root reference should remain an optional capability: %v", err)
	}

	writeReferenceProject(externalReference, nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "reference DOCX") || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("outside reference error = %v", err)
	}

	writeReferenceProject(externalReference, []string{external})
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err != nil {
		t.Fatalf("approved external reference: %v", err)
	}

	link := filepath.Join(root, "linked-reference.docx")
	if err := os.Symlink(externalReference, link); err != nil {
		t.Fatal(err)
	}
	writeReferenceProject("linked-reference.docx", nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("reference symlink error = %v", err)
	}

	writeReferenceProject(filepath.Join(external, "missing.docx"), nil)
	if _, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("missing outside reference error = %v", err)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOutputUsesIndependentPrecedence(t *testing.T) {
	root := t.TempDir()
	globalPath := filepath.Join(root, "global.yaml")
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeTestFile(t, globalPath, "version: 1\noutput:\n  format: json\n  quiet: true\n")
	writeTestFile(t, projectPath, "version: 2\noutput:\n  format: human\nprofiles:\n  work:\n    unexpected_semantic_shape: true\n")

	output, err := ResolveOutput(ResolverOptions{
		ProjectPath: projectPath,
		GlobalPath:  globalPath,
		Environment: map[string]string{"MDOC_OUTPUT_FORMAT": "json", "MDOC_OUTPUT_QUIET": "false"},
		Overrides:   map[string]any{"output": map[string]any{"format": "human", "quiet": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Format != "human" || !output.Quiet {
		t.Fatalf("resolved output = %#v", output)
	}

	output, err = ResolveOutput(ResolverOptions{ProjectPath: projectPath, GlobalPath: globalPath, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Format != "human" || !output.Quiet {
		t.Fatalf("project/global output = %#v", output)
	}
}

func TestResolveRejectsVersionTwoProfilesSharingStatePath(t *testing.T) {
	root := t.TempDir()
	realStateDir := filepath.Join(root, "real-state")
	if err := os.MkdirAll(realStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedStateDir := filepath.Join(root, "linked-state")
	if err := os.Symlink(realStateDir, linkedStateDir); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		a    string
		b    string
	}{
		{name: "relative", a: "state/../private/state.json", b: "private/state.json"},
		{name: "case", a: "private/STATE.json", b: "private/state.json"},
		{name: "unicode", a: "private/caf\u00e9.json", b: "private/cafe\u0301.json"},
		{name: "symlink", a: filepath.Join(realStateDir, "state.json"), b: filepath.Join(linkedStateDir, "state.json")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), ".mdoc.yaml")
			writeStateIsolationProject(t, projectPath, test.a, test.b)
			_, err := Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, Profile: "alpha", GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
			if err == nil || !strings.Contains(err.Error(), "same state_file") || !strings.Contains(err.Error(), `"alpha"`) || !strings.Contains(err.Error(), `"beta"`) {
				t.Fatalf("duplicate state error = %v", err)
			}
		})
	}
}

func TestResolveRejectsSharedGlobalStateDefaultAndSelectedOverride(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	writeStateIsolationProject(t, projectPath, "alpha.json", "beta.json")
	globalPath := filepath.Join(root, "global.yaml")
	writeTestFile(t, globalPath, "version: 1\nprofile_defaults:\n  state_file: shared.json\n")

	project := ProjectConfigV2{}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &project); err != nil {
		t.Fatal(err)
	}
	for name, profile := range project.Profiles {
		profile.StateFile = ""
		project.Profiles[name] = profile
	}
	data, err = yaml.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) != `state_file: ""` {
			filtered = append(filtered, line)
		}
	}
	writeTestFile(t, projectPath, strings.Join(filtered, "\n"))
	_, err = Resolve(context.Background(), ResolverOptions{ProjectPath: projectPath, Profile: "alpha", GlobalPath: globalPath, Environment: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "same state_file") {
		t.Fatalf("global duplicate state error = %v", err)
	}

	writeStateIsolationProject(t, projectPath, "alpha.json", "beta.json")
	_, err = Resolve(context.Background(), ResolverOptions{
		ProjectPath: projectPath,
		Profile:     "alpha",
		GlobalPath:  filepath.Join(root, "missing.yaml"),
		Environment: map[string]string{},
		Overrides:   map[string]any{"profile": map[string]any{"state_file": filepath.Join(root, "beta.json")}},
	})
	if err == nil || !strings.Contains(err.Error(), "same state_file") {
		t.Fatalf("override duplicate state error = %v", err)
	}
}

func writeStateIsolationProject(t *testing.T, path, alphaState, betaState string) {
	t.Helper()
	project := ProjectConfigV2{
		Version: ProjectVersion2, ProjectID: "state-isolation", DefaultProfile: "alpha", Root: ".", PandocBinary: "pandoc", Output: OutputConfig{Format: "human"},
		Profiles: map[string]RuntimeProfile{"alpha": NeutralProfile(), "beta": NeutralProfile()},
	}
	alpha := project.Profiles["alpha"]
	alpha.StateFile = alphaState
	project.Profiles["alpha"] = alpha
	beta := project.Profiles["beta"]
	beta.StateFile = betaState
	project.Profiles["beta"] = beta
	data, err := yaml.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(data))
}
