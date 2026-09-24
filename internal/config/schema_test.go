package config

import (
	"strings"
	"testing"
)

func TestRuntimeConfigRejectsUnsupportedOutputAndDestinationValues(t *testing.T) {
	valid := func() RuntimeConfig {
		return RuntimeConfig{
			Version: ProjectVersion2, ProjectID: "project-one", ProfileName: "review", Root: ".", PandocBinary: "pandoc",
			Output: OutputConfig{Format: "human"}, Profile: NeutralProfile(),
		}
	}
	tests := []struct {
		name    string
		change  func(*RuntimeConfig)
		message string
	}{
		{name: "output format", change: func(cfg *RuntimeConfig) { cfg.Output.Format = "xml" }, message: "output.format"},
		{name: "destination mode", change: func(cfg *RuntimeConfig) { cfg.Profile.Destination.Mode = "replace" }, message: "destination.mode"},
		{name: "empty staging name", change: func(cfg *RuntimeConfig) { cfg.Profile.Destination.StagingFolderName = " " }, message: "staging_folder_name"},
		{name: "empty review name", change: func(cfg *RuntimeConfig) { cfg.Profile.Destination.ReviewFolderName = "" }, message: "review_folder_name"},
		{name: "same trimmed names", change: func(cfg *RuntimeConfig) {
			cfg.Profile.Destination.StagingFolderName = "Review"
			cfg.Profile.Destination.ReviewFolderName = " Review "
		}, message: "must differ"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid()
			test.change(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestVersionThreePublicationAndLayoutValidation(t *testing.T) {
	pageStart := func(value int) *int { return &value }
	valid := func() RuntimeConfig {
		tocMemberEnabled := true
		profile := NeutralProfile()
		profile.Entry = "review-bundle"
		profile.FieldDefaults = map[string]any{"classification": "Internal"}
		profile.Publications = map[string]PublicationConfig{
			"review-bundle": {
				Kind:           PublicationBundle,
				Members:        []MemberConfig{{Source: "/tmp/one.md", Start: "new_page", TitleMode: "heading_1"}},
				ReferenceLinks: []ReferenceLinkConfig{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, Definition: ReferenceDefinitionConfig{Source: "/tmp/one.md", Match: "heading_prefix"}, Unresolved: "error"}},
				Layout: LayoutConfig{
					TableOfContents: TOCConfig{Enabled: true, Mode: "static", Title: "Contents", Depth: 3, Members: []TOCMemberConfig{{Source: "/tmp/one.md", Enabled: &tocMemberEnabled, Depth: 2, IncludeHeadings: []string{"summary"}, ExcludeHeadings: []string{"references"}}}},
					Page:            PageConfig{Size: "letter", Orientation: "portrait", Margins: MarginsConfig{Top: "1in", Right: "1in", Bottom: "1in", Left: "1in"}, PageNumberStart: pageStart(1)},
					Tables:          TableLayoutConfig{WideTable: "landscape_section", WideTableColumns: 6},
				},
			},
		}
		return RuntimeConfig{
			Version: ProjectVersion3, ProjectID: "project-one", ProfileName: "review", Root: ".", PandocBinary: "pandoc",
			Output: OutputConfig{Format: "human"}, Profile: profile,
		}
	}
	validConfig := valid()
	if err := validConfig.Validate(); err != nil {
		t.Fatalf("valid version 3 config: %v", err)
	}
	reviewConfig := valid()
	reviewPublication := reviewConfig.Profile.Publications["review-bundle"]
	reviewPublication.Review.Pull.Enabled = true
	reviewConfig.Profile.Publications["review-bundle"] = reviewPublication
	if err := reviewConfig.Validate(); err != nil {
		t.Fatalf("review pull enabled config: %v", err)
	}
	sourceConfig := valid()
	sourceConfig.Profile.Publications["source-document"] = PublicationConfig{Kind: PublicationSource, Source: "/tmp/one.md", Layout: LayoutConfig{TableOfContents: TOCConfig{Enabled: false}}}
	if err := sourceConfig.Validate(); err != nil {
		t.Fatalf("source publication with disabled table of contents: %v", err)
	}
	for _, mode := range []string{"inherit", "override", "remove"} {
		cfg := valid()
		publication := cfg.Profile.Publications["review-bundle"]
		publication.Layout.Header.Mode = mode
		if mode == "override" {
			publication.Layout.Header.Right = "Page"
		}
		cfg.Profile.Publications["review-bundle"] = publication
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid header mode %s: %v", mode, err)
		}
	}

	tests := []struct {
		name    string
		change  func(*RuntimeConfig)
		message string
	}{
		{name: "publication id", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			delete(cfg.Profile.Publications, "review-bundle")
			cfg.Profile.Publications["Review"] = publication
			cfg.Profile.Entry = "Review"
		}, message: "publication ID"},
		{name: "reserved defaults", change: func(cfg *RuntimeConfig) { cfg.Profile.FieldDefaults["mdoc"] = true }, message: "reserved root"},
		{name: "reserved publication fields", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Fields = map[string]any{"page": "bad"}
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "reserved root"},
		{name: "page unit", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Page.Margins.Left = "1px"
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "ASCII decimal"},
		{name: "usable width", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Page.Margins.Left = "4.1in"
			publication.Layout.Page.Margins.Right = "4in"
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "usable width"},
		{name: "page number", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Page.PageNumberStart = pageStart(32768)
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "32767"},
		{name: "page number zero", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Page.PageNumberStart = pageStart(0)
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "between 1 and 32767"},
		{name: "wide threshold", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Tables.WideTableColumns = -1
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "cannot be negative"},
		{name: "TOC member depth", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.TableOfContents.Members[0].Depth = 7
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "members[0].depth"},
		{name: "duplicate TOC member", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.TableOfContents.Members = append(publication.Layout.TableOfContents.Members, publication.Layout.TableOfContents.Members[0])
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "duplicate source"},
		{name: "duplicate TOC heading", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.TableOfContents.Members[0].IncludeHeadings = []string{"summary", "summary"}
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "duplicate heading ID"},
		{name: "conflicting TOC heading", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.TableOfContents.Members[0].ExcludeHeadings = []string{"summary"}
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "both included and excluded"},
		{name: "source publication TOC", change: func(cfg *RuntimeConfig) {
			cfg.Profile.Publications["source-document"] = PublicationConfig{
				Kind: PublicationSource, Source: "/tmp/one.md",
				Layout: LayoutConfig{TableOfContents: TOCConfig{Enabled: true, Mode: "static", Depth: 1}},
			}
		}, message: "source publication cannot define table_of_contents settings"},
		{name: "duplicate reference id", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.ReferenceLinks = append(publication.ReferenceLinks, publication.ReferenceLinks[0])
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "duplicate ID"},
		{name: "invalid reference id", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.ReferenceLinks[0].ID = "Operations"
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "lowercase letters"},
		{name: "invalid reference pattern", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.ReferenceLinks[0].Pattern = "["
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "pattern is invalid"},
		{name: "empty matching reference pattern", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.ReferenceLinks[0].Pattern = `\b|OP[0-9]{2}`
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "empty string"},
		{name: "reference definition mode", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.ReferenceLinks[0].Definition.Match = "paragraph"
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "heading_prefix"},
		{name: "reference policy", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.ReferenceLinks[0].Unresolved = "ignore"
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "error, warning, or preserve"},
		{name: "source publication reference links", change: func(cfg *RuntimeConfig) {
			cfg.Profile.Publications["source-document"] = PublicationConfig{
				Kind: PublicationSource, Source: "/tmp/one.md",
				ReferenceLinks: []ReferenceLinkConfig{{ID: "operations", Pattern: `\bOP[0-9]{2}\b`, Definition: ReferenceDefinitionConfig{Source: "/tmp/one.md", Match: "heading_prefix"}, Unresolved: "error"}},
			}
		}, message: "source publication cannot define reference_links"},
		{name: "header mode", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Header.Mode = "merge"
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "header.mode"},
		{name: "inherit with content", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Header = HeaderFooterConfig{Mode: "inherit", Left: "invalid"}
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "cannot include content"},
		{name: "remove with first page", change: func(cfg *RuntimeConfig) {
			publication := cfg.Profile.Publications["review-bundle"]
			publication.Layout.Footer = HeaderFooterConfig{Mode: "remove", FirstPage: &HeaderFooterContent{Center: "invalid"}}
			cfg.Profile.Publications["review-bundle"] = publication
		}, message: "cannot include content"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid()
			test.change(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestHasTableOfContentsSettingsDetectsEveryActiveSourceSetting(t *testing.T) {
	enabled := true
	tests := []struct {
		name string
		toc  TOCConfig
	}{
		{name: "enabled", toc: TOCConfig{Enabled: true}},
		{name: "mode", toc: TOCConfig{Mode: "static"}},
		{name: "title", toc: TOCConfig{Title: "Contents"}},
		{name: "depth", toc: TOCConfig{Depth: 1}},
		{name: "member", toc: TOCConfig{Members: []TOCMemberConfig{{Source: "one.md", Enabled: &enabled}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !hasTableOfContentsSettings(test.toc) {
				t.Fatalf("active table of contents settings were not detected: %#v", test.toc)
			}
		})
	}
	if hasTableOfContentsSettings(TOCConfig{Enabled: false}) {
		t.Fatal("an explicit disabled zero-value table of contents was treated as active")
	}
}

func TestParseLayoutTwipsRoundsHalfAwayFromZero(t *testing.T) {
	for value, want := range map[string]int64{
		"1in":    1440,
		"72pt":   1440,
		"2.54cm": 1440,
		"25.4mm": 1440,
		"0.5pt":  10,
	} {
		got, err := parseLayoutTwips(value)
		if err != nil || got != want {
			t.Fatalf("parseLayoutTwips(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
}

func TestHeaderFooterDefaultVariantIsExplicit(t *testing.T) {
	layout := LayoutConfig{Header: HeaderFooterConfig{Mode: "override"}}
	if err := validateLayout(layout); err == nil || !strings.Contains(err.Error(), "requires default") {
		t.Fatalf("empty override error = %v", err)
	}
	layout.Header = HeaderFooterConfig{Mode: "override", Default: &HeaderFooterContent{}}
	if err := validateLayout(layout); err != nil {
		t.Fatalf("blank default override: %v", err)
	}
	layout.Header = HeaderFooterConfig{Mode: "override", Default: &HeaderFooterContent{}, Left: "legacy"}
	if err := validateLayout(layout); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("mixed default error = %v", err)
	}
}

func TestPreserveWidthStylesRejectEmptyAndDuplicateValues(t *testing.T) {
	layout := LayoutConfig{Tables: TableLayoutConfig{PreserveWidthStyles: []string{"Layout Grid", "Layout Grid"}}}
	if err := validateLayout(layout); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate style error = %v", err)
	}
	layout.Tables.PreserveWidthStyles = []string{" "}
	if err := validateLayout(layout); err == nil || !strings.Contains(err.Error(), "empty style") {
		t.Fatalf("empty style error = %v", err)
	}
}
