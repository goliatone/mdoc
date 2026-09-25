package config

import (
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	ProjectVersion1 = 1
	ProjectVersion2 = 2
	ProjectVersion3 = 3
	GlobalVersion1  = 1
)

var layoutLengthPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.[0-9]+)?(in|pt|cm|mm)$`)

type OutputConfig struct {
	Format string `koanf:"format" yaml:"format" json:"format"`
	Quiet  bool   `koanf:"quiet" yaml:"quiet" json:"quiet"`
}

type SourceConfig struct {
	Include       []string `koanf:"include" yaml:"include" json:"include"`
	Exclude       []string `koanf:"exclude" yaml:"exclude" json:"exclude"`
	ExternalRoots []string `koanf:"external_roots" yaml:"external_roots" json:"external_roots"`
}

type NamingConfig struct {
	Source  string            `koanf:"source" yaml:"source" json:"source"`
	Mapping map[string]string `koanf:"mapping" yaml:"mapping" json:"mapping"`
}

type ValidationConfig struct {
	Title                    string   `koanf:"title" yaml:"title" json:"title"`
	HeadingJumps             string   `koanf:"heading_jumps" yaml:"heading_jumps" json:"heading_jumps"`
	UnpublishedMarkdownLinks string   `koanf:"unpublished_markdown_links" yaml:"unpublished_markdown_links" json:"unpublished_markdown_links"`
	MaxTableColumns          int      `koanf:"max_table_columns" yaml:"max_table_columns" json:"max_table_columns"`
	ImageTypes               []string `koanf:"image_types" yaml:"image_types" json:"image_types"`
}

type StyleConfig struct {
	ReferenceDOCX string   `koanf:"reference_docx" yaml:"reference_docx" json:"reference_docx"`
	HeadingFilter string   `koanf:"heading_filter" yaml:"heading_filter" json:"heading_filter"`
	Filters       []string `koanf:"filters" yaml:"filters" json:"filters"`
	Roots         []string `koanf:"roots" yaml:"roots" json:"roots"`
	Reader        string   `koanf:"reader" yaml:"reader" json:"reader"`
}

type DestinationConfig struct {
	Mode              string `koanf:"mode" yaml:"mode" json:"mode"`
	ParentFolderID    string `koanf:"parent_folder_id" yaml:"parent_folder_id" json:"parent_folder_id"`
	StagingFolderID   string `koanf:"staging_folder_id" yaml:"staging_folder_id" json:"staging_folder_id"`
	ReviewFolderID    string `koanf:"review_folder_id" yaml:"review_folder_id" json:"review_folder_id"`
	StagingFolderName string `koanf:"staging_folder_name" yaml:"staging_folder_name" json:"staging_folder_name"`
	ReviewFolderName  string `koanf:"review_folder_name" yaml:"review_folder_name" json:"review_folder_name"`
}

type PublicationKind string

const (
	PublicationSource PublicationKind = "source"
	PublicationBundle PublicationKind = "bundle"
)

type MemberConfig struct {
	Source            string        `koanf:"source" yaml:"source" json:"source"`
	Start             string        `koanf:"start" yaml:"start" json:"start"`
	TitleMode         string        `koanf:"title_mode" yaml:"title_mode" json:"title_mode"`
	HeadingAdjustment int           `koanf:"heading_adjustment" yaml:"heading_adjustment" json:"heading_adjustment"`
	Section           SectionConfig `koanf:"section" yaml:"section" json:"section"`
	Appendix          bool          `koanf:"appendix" yaml:"appendix" json:"appendix"`
	AppendixTitle     string        `koanf:"appendix_title" yaml:"appendix_title" json:"appendix_title"`
	AppendixPrefix    string        `koanf:"appendix_prefix" yaml:"appendix_prefix" json:"appendix_prefix"`
}

type PublicationConfig struct {
	Kind           PublicationKind       `koanf:"kind" yaml:"kind" json:"kind"`
	Title          string                `koanf:"title" yaml:"title" json:"title"`
	Source         string                `koanf:"source" yaml:"source" json:"source"`
	Members        []MemberConfig        `koanf:"members" yaml:"members" json:"members"`
	ReferenceLinks []ReferenceLinkConfig `koanf:"reference_links" yaml:"reference_links" json:"reference_links"`
	FieldFiles     []string              `koanf:"field_files" yaml:"field_files" json:"field_files"`
	Fields         map[string]any        `koanf:"fields" yaml:"fields" json:"fields"`
	Layout         LayoutConfig          `koanf:"layout" yaml:"layout" json:"layout"`
	Review         ReviewConfig          `koanf:"review" yaml:"review" json:"review"`
	FragmentPolicy string                `koanf:"fragment_policy" yaml:"fragment_policy" json:"fragment_policy"`
}

type ReviewConfig struct {
	Pull ReviewPullConfig `koanf:"pull" yaml:"pull" json:"pull"`
}

type ReviewPullConfig struct {
	Enabled bool `koanf:"enabled" yaml:"enabled" json:"enabled"`
}

type ReferenceLinkConfig struct {
	ID         string                    `koanf:"id" yaml:"id" json:"id"`
	Pattern    string                    `koanf:"pattern" yaml:"pattern" json:"pattern"`
	Definition ReferenceDefinitionConfig `koanf:"definition" yaml:"definition" json:"definition"`
	Unresolved string                    `koanf:"unresolved" yaml:"unresolved" json:"unresolved"`
}

type ReferenceDefinitionConfig struct {
	Source string `koanf:"source" yaml:"source" json:"source"`
	Match  string `koanf:"match" yaml:"match" json:"match"`
}

type LayoutConfig struct {
	Cover           CoverConfig         `koanf:"cover" yaml:"cover" json:"cover"`
	TableOfContents TOCConfig           `koanf:"table_of_contents" yaml:"table_of_contents" json:"table_of_contents"`
	Page            PageConfig          `koanf:"page" yaml:"page" json:"page"`
	Header          HeaderFooterConfig  `koanf:"header" yaml:"header" json:"header"`
	Footer          HeaderFooterConfig  `koanf:"footer" yaml:"footer" json:"footer"`
	Headings        HeadingLayoutConfig `koanf:"headings" yaml:"headings" json:"headings"`
	Tables          TableLayoutConfig   `koanf:"tables" yaml:"tables" json:"tables"`
}

type CoverConfig struct {
	Enabled  bool               `koanf:"enabled" yaml:"enabled" json:"enabled"`
	Title    string             `koanf:"title" yaml:"title" json:"title"`
	Subtitle string             `koanf:"subtitle" yaml:"subtitle" json:"subtitle"`
	Prose    string             `koanf:"prose" yaml:"prose" json:"prose"`
	Fields   []CoverFieldConfig `koanf:"fields" yaml:"fields" json:"fields"`
}

type CoverFieldConfig struct {
	Key       string `koanf:"key" yaml:"key" json:"key"`
	Label     string `koanf:"label" yaml:"label" json:"label"`
	Format    string `koanf:"format" yaml:"format" json:"format"`
	StyleRole string `koanf:"style_role" yaml:"style_role" json:"style_role"`
	Optional  bool   `koanf:"optional" yaml:"optional" json:"optional"`
	Visible   *bool  `koanf:"visible" yaml:"visible" json:"visible"`
}

type TOCConfig struct {
	Enabled bool              `koanf:"enabled" yaml:"enabled" json:"enabled"`
	Mode    string            `koanf:"mode" yaml:"mode" json:"mode"`
	Title   string            `koanf:"title" yaml:"title" json:"title"`
	Depth   int               `koanf:"depth" yaml:"depth" json:"depth"`
	Members []TOCMemberConfig `koanf:"members" yaml:"members" json:"members"`
}

type TOCMemberConfig struct {
	Source          string   `koanf:"source" yaml:"source" json:"source"`
	Enabled         *bool    `koanf:"enabled" yaml:"enabled" json:"enabled"`
	Depth           int      `koanf:"depth" yaml:"depth" json:"depth"`
	IncludeHeadings []string `koanf:"include_headings" yaml:"include_headings" json:"include_headings"`
	ExcludeHeadings []string `koanf:"exclude_headings" yaml:"exclude_headings" json:"exclude_headings"`
}

type MarginsConfig struct {
	Top    string `koanf:"top" yaml:"top" json:"top"`
	Right  string `koanf:"right" yaml:"right" json:"right"`
	Bottom string `koanf:"bottom" yaml:"bottom" json:"bottom"`
	Left   string `koanf:"left" yaml:"left" json:"left"`
}

type PageConfig struct {
	Size            string        `koanf:"size" yaml:"size" json:"size"`
	Width           string        `koanf:"width" yaml:"width" json:"width"`
	Height          string        `koanf:"height" yaml:"height" json:"height"`
	Orientation     string        `koanf:"orientation" yaml:"orientation" json:"orientation"`
	Margins         MarginsConfig `koanf:"margins" yaml:"margins" json:"margins"`
	PageNumberStart *int          `koanf:"page_number_start" yaml:"page_number_start" json:"page_number_start"`
}

type SectionConfig struct {
	Orientation string        `koanf:"orientation" yaml:"orientation" json:"orientation"`
	Margins     MarginsConfig `koanf:"margins" yaml:"margins" json:"margins"`
}

type HeaderFooterContent struct {
	Left   string `koanf:"left" yaml:"left" json:"left"`
	Center string `koanf:"center" yaml:"center" json:"center"`
	Right  string `koanf:"right" yaml:"right" json:"right"`
}

type HeaderFooterConfig struct {
	Mode      string               `koanf:"mode" yaml:"mode" json:"mode"`
	Default   *HeaderFooterContent `koanf:"default" yaml:"default" json:"default"`
	Left      string               `koanf:"left" yaml:"left" json:"left"`
	Center    string               `koanf:"center" yaml:"center" json:"center"`
	Right     string               `koanf:"right" yaml:"right" json:"right"`
	FirstPage *HeaderFooterContent `koanf:"first_page" yaml:"first_page" json:"first_page"`
}

type HeadingLayoutConfig struct {
	MemberTitleLevel int      `koanf:"member_title_level" yaml:"member_title_level" json:"member_title_level"`
	KeepWithNext     bool     `koanf:"keep_with_next" yaml:"keep_with_next" json:"keep_with_next"`
	CaptionStyles    []string `koanf:"caption_styles" yaml:"caption_styles" json:"caption_styles"`
}

type TableLayoutConfig struct {
	RepeatHeaderRows    bool     `koanf:"repeat_header_rows" yaml:"repeat_header_rows" json:"repeat_header_rows"`
	WideTable           string   `koanf:"wide_table" yaml:"wide_table" json:"wide_table"`
	WideTableColumns    int      `koanf:"wide_table_columns" yaml:"wide_table_columns" json:"wide_table_columns"`
	PreserveWidthStyles []string `koanf:"preserve_width_styles" yaml:"preserve_width_styles" json:"preserve_width_styles"`
}

type RuntimeProfile struct {
	Sources       SourceConfig                 `koanf:"sources" yaml:"sources" json:"sources"`
	Entry         string                       `koanf:"entry" yaml:"entry" json:"entry"`
	Naming        NamingConfig                 `koanf:"naming" yaml:"naming" json:"naming"`
	Validation    ValidationConfig             `koanf:"validation" yaml:"validation" json:"validation"`
	Style         StyleConfig                  `koanf:"style" yaml:"style" json:"style"`
	Destination   DestinationConfig            `koanf:"destination" yaml:"destination" json:"destination"`
	StateFile     string                       `koanf:"state_file" yaml:"state_file" json:"state_file"`
	FieldDefaults map[string]any               `koanf:"field_defaults" yaml:"field_defaults" json:"field_defaults"`
	Publications  map[string]PublicationConfig `koanf:"publications" yaml:"publications" json:"publications"`
	FieldSummary  *FieldConfigSummary          `koanf:"-" yaml:"-" json:"field_summary,omitempty"`
}

type FieldConfigSummary struct {
	DefaultRoots int                                `json:"default_roots"`
	Publications map[string]PublicationFieldSummary `json:"publications"`
	Warning      string                             `json:"warning"`
}

type PublicationFieldSummary struct {
	InlineRoots int `json:"inline_roots"`
	FieldFiles  int `json:"field_files"`
}

type RuntimeConfig struct {
	Version        int            `koanf:"version" yaml:"version" json:"version"`
	ProjectID      string         `koanf:"project_id" yaml:"project_id" json:"project_id"`
	WorkspaceID    string         `koanf:"workspace_id" yaml:"workspace_id" json:"workspace_id"`
	DefaultProfile string         `koanf:"default_profile" yaml:"default_profile" json:"default_profile"`
	Root           string         `koanf:"root" yaml:"root" json:"root"`
	PandocBinary   string         `koanf:"pandoc_binary" yaml:"pandoc_binary" json:"pandoc_binary"`
	Output         OutputConfig   `koanf:"output" yaml:"output" json:"output"`
	Profile        RuntimeProfile `koanf:"profile" yaml:"profile" json:"profile"`

	ProfileName string      `koanf:"-" yaml:"-" json:"profile_name"`
	ProjectPath string      `koanf:"-" yaml:"-" json:"project_path,omitempty"`
	GlobalPath  string      `koanf:"-" yaml:"-" json:"global_path,omitempty"`
	Origins     OriginIndex `koanf:"-" yaml:"-" json:"origins,omitempty"`
	Direct      bool        `koanf:"-" yaml:"-" json:"direct,omitempty"`
}

func (c *RuntimeConfig) Validate() error {
	if c.Version != ProjectVersion1 && c.Version != ProjectVersion2 && c.Version != ProjectVersion3 {
		return fmt.Errorf("unsupported project config version %d", c.Version)
	}
	if !c.Direct && c.Identity() == "" {
		return errors.New("project identity is required")
	}
	if c.ProfileName == "" {
		return errors.New("profile selection is required")
	}
	if strings.TrimSpace(c.Root) == "" {
		return errors.New("root is required")
	}
	if strings.TrimSpace(c.PandocBinary) == "" {
		return errors.New("pandoc_binary is required")
	}
	if !oneOf(c.Output.Format, "human", "json") {
		return errors.New("output.format must be human or json")
	}
	if c.Profile.Destination.Mode != "immutable_review" {
		return fmt.Errorf("profile %q destination.mode must be immutable_review", c.ProfileName)
	}
	stagingName := strings.TrimSpace(c.Profile.Destination.StagingFolderName)
	reviewName := strings.TrimSpace(c.Profile.Destination.ReviewFolderName)
	if stagingName == "" || reviewName == "" {
		return fmt.Errorf("profile %q destination staging_folder_name and review_folder_name are required", c.ProfileName)
	}
	if c.Profile.Validation.MaxTableColumns < 0 {
		return fmt.Errorf("profile %q max_table_columns cannot be negative", c.ProfileName)
	}
	if stagingName == reviewName {
		return fmt.Errorf("profile %q staging and review folder names must differ", c.ProfileName)
	}
	if !oneOf(c.Profile.Validation.Title, "required", "optional") {
		return fmt.Errorf("profile %q validation.title must be required or optional", c.ProfileName)
	}
	if !oneOf(c.Profile.Validation.HeadingJumps, "error", "warning", "allow") {
		return fmt.Errorf("profile %q validation.heading_jumps must be error, warning, or allow", c.ProfileName)
	}
	if !oneOf(c.Profile.Validation.UnpublishedMarkdownLinks, "error", "warning", "preserve") {
		return fmt.Errorf("profile %q validation.unpublished_markdown_links must be error, warning, or preserve", c.ProfileName)
	}
	if !oneOf(c.Profile.Naming.Source, "h1", "front_matter", "file", "mapping") {
		return fmt.Errorf("profile %q naming.source is invalid", c.ProfileName)
	}
	if !oneOf(c.Profile.Style.HeadingFilter, "title", "none") {
		return fmt.Errorf("profile %q style.heading_filter must be title or none", c.ProfileName)
	}
	if strings.TrimSpace(c.Profile.Style.Reader) == "" || strings.ContainsAny(c.Profile.Style.Reader, " \t\r\n") {
		return fmt.Errorf("profile %q style.reader must be one Pandoc reader expression", c.ProfileName)
	}
	if c.Version == ProjectVersion3 {
		if err := validateVersion3Profile(c.ProfileName, c.Profile, false); err != nil {
			return err
		}
	}
	return nil
}

func validateVersion3Profile(name string, profile RuntimeProfile, pathsMustBeRelative bool) error {
	if len(profile.Publications) == 0 {
		return fmt.Errorf("profile %q must define at least one publication", name)
	}
	if !validPublicationID(profile.Entry) {
		return fmt.Errorf("profile %q entry must name a valid publication ID", name)
	}
	if _, ok := profile.Publications[profile.Entry]; !ok {
		return fmt.Errorf("profile %q entry publication %q is not defined", name, profile.Entry)
	}
	if err := validateFieldRoots("profile field_defaults", profile.FieldDefaults); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	for _, id := range profileNames(profile.Publications) {
		publication := profile.Publications[id]
		if !validPublicationID(id) {
			return fmt.Errorf("profile %q publication ID %q must be 1 through 63 lowercase letters, digits, and single hyphens", name, id)
		}
		if err := validatePublication(name, id, publication, pathsMustBeRelative); err != nil {
			return err
		}
	}
	return nil
}

func validatePublication(profileName, id string, publication PublicationConfig, pathsMustBeRelative bool) error {
	prefix := fmt.Sprintf("profile %q publication %q", profileName, id)
	if publication.Kind != PublicationSource && publication.Kind != PublicationBundle {
		return fmt.Errorf("%s kind must be source or bundle", prefix)
	}
	if err := validateFieldRoots("fields", publication.Fields); err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	for _, path := range publication.FieldFiles {
		if err := validateConfigPath(path, pathsMustBeRelative); err != nil {
			return fmt.Errorf("%s field_files path %q: %w", prefix, path, err)
		}
	}
	if publication.FragmentPolicy != "" && !oneOf(publication.FragmentPolicy, "error", "warning_root", "preserve") {
		return fmt.Errorf("%s fragment_policy must be error, warning_root, or preserve", prefix)
	}
	seenReferenceIDs := map[string]bool{}
	for index, rule := range publication.ReferenceLinks {
		if err := validateReferenceLink(rule, pathsMustBeRelative); err != nil {
			return fmt.Errorf("%s reference_links[%d]: %w", prefix, index, err)
		}
		if seenReferenceIDs[rule.ID] {
			return fmt.Errorf("%s reference_links contains duplicate ID %q", prefix, rule.ID)
		}
		seenReferenceIDs[rule.ID] = true
	}
	switch publication.Kind {
	case PublicationSource:
		if err := validateConfigPath(publication.Source, pathsMustBeRelative); err != nil {
			return fmt.Errorf("%s source: %w", prefix, err)
		}
		if len(publication.Members) != 0 {
			return fmt.Errorf("%s source publication cannot define members", prefix)
		}
		if len(publication.ReferenceLinks) != 0 {
			return fmt.Errorf("%s source publication cannot define reference_links", prefix)
		}
		if hasTableOfContentsSettings(publication.Layout.TableOfContents) {
			return fmt.Errorf("%s source publication cannot define table_of_contents settings", prefix)
		}
	case PublicationBundle:
		if publication.Source != "" {
			return fmt.Errorf("%s bundle publication cannot define source", prefix)
		}
		if len(publication.Members) == 0 {
			return fmt.Errorf("%s bundle must define at least one member", prefix)
		}
		for index, member := range publication.Members {
			if err := validateMember(member, pathsMustBeRelative); err != nil {
				return fmt.Errorf("%s member %d: %w", prefix, index+1, err)
			}
		}
	}
	for index, member := range publication.Layout.TableOfContents.Members {
		if err := validateConfigPath(member.Source, pathsMustBeRelative); err != nil {
			return fmt.Errorf("%s layout table_of_contents member %d source: %w", prefix, index+1, err)
		}
	}
	if err := validateLayout(publication.Layout); err != nil {
		return fmt.Errorf("%s layout: %w", prefix, err)
	}
	return nil
}

func validateReferenceLink(rule ReferenceLinkConfig, pathsMustBeRelative bool) error {
	if !validPublicationID(rule.ID) {
		return fmt.Errorf("id %q must be 1 through 63 lowercase letters, digits, and single hyphens", rule.ID)
	}
	if strings.TrimSpace(rule.Pattern) == "" {
		return errors.New("pattern is required")
	}
	pattern, err := regexp.Compile(rule.Pattern)
	if err != nil {
		return fmt.Errorf("pattern is invalid: %w", err)
	}
	expression, err := syntax.Parse(rule.Pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("pattern is invalid: %w", err)
	}
	if referencePatternCanMatchEmpty(expression) || pattern.MatchString("") {
		return errors.New("pattern must not match an empty string")
	}
	if err := validateConfigPath(rule.Definition.Source, pathsMustBeRelative); err != nil {
		return fmt.Errorf("definition.source: %w", err)
	}
	if rule.Definition.Match != "heading_prefix" {
		return errors.New("definition.match must be heading_prefix")
	}
	if !oneOf(rule.Unresolved, "error", "warning", "preserve") {
		return errors.New("unresolved must be error, warning, or preserve")
	}
	return nil
}

func referencePatternCanMatchEmpty(expression *syntax.Regexp) bool {
	switch expression.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpCapture:
		return len(expression.Sub) == 0 || referencePatternCanMatchEmpty(expression.Sub[0])
	case syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpPlus:
		return len(expression.Sub) == 0 || referencePatternCanMatchEmpty(expression.Sub[0])
	case syntax.OpRepeat:
		return expression.Min == 0 || len(expression.Sub) == 0 || referencePatternCanMatchEmpty(expression.Sub[0])
	case syntax.OpConcat:
		for _, child := range expression.Sub {
			if !referencePatternCanMatchEmpty(child) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		return slices.ContainsFunc(expression.Sub, referencePatternCanMatchEmpty)
	default:
		return false
	}
}

func hasTableOfContentsSettings(toc TOCConfig) bool {
	return toc.Enabled || toc.Mode != "" || toc.Title != "" || toc.Depth != 0 || len(toc.Members) != 0
}

func validateMember(member MemberConfig, pathsMustBeRelative bool) error {
	if err := validateConfigPath(member.Source, pathsMustBeRelative); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if member.Start != "" && !oneOf(member.Start, "continuous", "after_cover", "new_page", "odd_page", "new_section") {
		return fmt.Errorf("start %q is unsupported", member.Start)
	}
	if member.TitleMode != "" && !oneOf(member.TitleMode, "document_title", "heading_1", "heading_2", "heading_3", "heading_4", "heading_5", "heading_6", "keep", "remove") {
		return fmt.Errorf("title_mode %q is unsupported", member.TitleMode)
	}
	if member.HeadingAdjustment < -5 || member.HeadingAdjustment > 5 {
		return errors.New("heading_adjustment must be between -5 and 5")
	}
	return validateSection(member.Section)
}

func validateLayout(layout LayoutConfig) error {
	toc := layout.TableOfContents
	if toc.Mode != "" && toc.Mode != "static" {
		return errors.New("table_of_contents.mode must be static")
	}
	if toc.Depth < 0 || toc.Depth > 6 {
		return errors.New("table_of_contents.depth must be between 0 and 6")
	}
	if toc.Enabled && toc.Depth == 0 {
		return errors.New("table_of_contents.depth must be between 1 and 6 when enabled")
	}
	seenTOCMembers := map[string]bool{}
	for index, member := range toc.Members {
		if member.Depth < 0 || member.Depth > 6 {
			return fmt.Errorf("table_of_contents.members[%d].depth must be between 0 and 6", index)
		}
		source := filepath.Clean(member.Source)
		if seenTOCMembers[source] {
			return fmt.Errorf("table_of_contents.members contains duplicate source %q", member.Source)
		}
		seenTOCMembers[source] = true
		include, err := validateHeadingIDs("include_headings", member.IncludeHeadings)
		if err != nil {
			return fmt.Errorf("table_of_contents member %q: %w", member.Source, err)
		}
		exclude, err := validateHeadingIDs("exclude_headings", member.ExcludeHeadings)
		if err != nil {
			return fmt.Errorf("table_of_contents member %q: %w", member.Source, err)
		}
		for heading := range include {
			if exclude[heading] {
				return fmt.Errorf("table_of_contents member %q heading %q cannot be both included and excluded", member.Source, heading)
			}
		}
	}
	if layout.Headings.MemberTitleLevel < 0 || layout.Headings.MemberTitleLevel > 6 {
		return errors.New("headings.member_title_level must be between 0 and 6")
	}
	if layout.Tables.WideTableColumns < 0 {
		return errors.New("tables.wide_table_columns cannot be negative")
	}
	if layout.Tables.WideTable != "" && !oneOf(layout.Tables.WideTable, "warning", "error", "normal_flow", "landscape_section") {
		return errors.New("tables.wide_table must be warning, error, normal_flow, or landscape_section")
	}
	seenTableStyles := map[string]bool{}
	for _, style := range layout.Tables.PreserveWidthStyles {
		style = strings.TrimSpace(style)
		if style == "" {
			return errors.New("tables.preserve_width_styles cannot contain an empty style")
		}
		if seenTableStyles[style] {
			return fmt.Errorf("tables.preserve_width_styles contains duplicate style %q", style)
		}
		seenTableStyles[style] = true
	}
	if err := validateHeaderFooter("header", layout.Header); err != nil {
		return err
	}
	if err := validateHeaderFooter("footer", layout.Footer); err != nil {
		return err
	}
	if err := validatePage(layout.Page); err != nil {
		return err
	}
	return nil
}

func validateHeadingIDs(label string, values []string) (map[string]bool, error) {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("%s contains an empty or padded heading ID", label)
		}
		if seen[value] {
			return nil, fmt.Errorf("%s contains duplicate heading ID %q", label, value)
		}
		seen[value] = true
	}
	return seen, nil
}

func validateHeaderFooter(name string, value HeaderFooterConfig) error {
	if value.Mode != "" && !oneOf(value.Mode, "inherit", "override", "remove") {
		return fmt.Errorf("%s.mode must be inherit, override, or remove", name)
	}
	flatConfigured := value.Left != "" || value.Center != "" || value.Right != ""
	if value.Default != nil && flatConfigured {
		return fmt.Errorf("%s.default cannot be combined with left, center, or right", name)
	}
	configured := value.Default != nil || flatConfigured || value.FirstPage != nil
	if configured && (value.Mode == "inherit" || value.Mode == "remove") {
		return fmt.Errorf("%s.mode %s cannot include content", name, value.Mode)
	}
	if value.Mode == "override" && !configured {
		return fmt.Errorf("%s.mode override requires default, first_page, or legacy position content", name)
	}
	return nil
}

func validatePage(page PageConfig) error {
	if page.Size != "" && !oneOf(page.Size, "letter", "legal", "a4") {
		return errors.New("page.size must be letter, legal, or a4")
	}
	if page.Orientation != "" && !oneOf(page.Orientation, "portrait", "landscape") {
		return errors.New("page.orientation must be portrait or landscape")
	}
	if (page.Width == "") != (page.Height == "") {
		return errors.New("page.width and page.height must be set together")
	}
	if page.Size != "" && page.Width != "" {
		return errors.New("page.size cannot be combined with explicit page.width and page.height")
	}
	width, height := namedPageTwips(page.Size)
	if page.Width != "" {
		var err error
		width, err = parseLayoutTwips(page.Width)
		if err != nil {
			return fmt.Errorf("page.width: %w", err)
		}
		height, err = parseLayoutTwips(page.Height)
		if err != nil {
			return fmt.Errorf("page.height: %w", err)
		}
		if width < 1440 || width > 31680 || height < 1440 || height > 31680 {
			return errors.New("page dimensions must be between 1in and 22in")
		}
	}
	if page.Orientation == "landscape" && width > 0 && height > 0 && width < height {
		width, height = height, width
	}
	margins := []struct {
		name  string
		value string
	}{
		{"top", page.Margins.Top}, {"right", page.Margins.Right}, {"bottom", page.Margins.Bottom}, {"left", page.Margins.Left},
	}
	values := map[string]int64{}
	for _, margin := range margins {
		if margin.value == "" {
			continue
		}
		twips, err := parseLayoutTwips(margin.value)
		if err != nil {
			return fmt.Errorf("page.margins.%s: %w", margin.name, err)
		}
		if twips > 14400 {
			return fmt.Errorf("page.margins.%s must not exceed 10in", margin.name)
		}
		values[margin.name] = twips
	}
	if width > 0 && values["left"]+values["right"] > width-720 {
		return errors.New("page left and right margins must leave at least 0.5in of usable width")
	}
	if height > 0 && values["top"]+values["bottom"] > height-720 {
		return errors.New("page top and bottom margins must leave at least 0.5in of usable height")
	}
	if page.PageNumberStart != nil && (*page.PageNumberStart < 1 || *page.PageNumberStart > 32767) {
		return errors.New("page.page_number_start must be between 1 and 32767 when set")
	}
	return nil
}

func validateSection(section SectionConfig) error {
	if section.Orientation != "" && !oneOf(section.Orientation, "portrait", "landscape") {
		return errors.New("section.orientation must be portrait or landscape")
	}
	for name, value := range map[string]string{"top": section.Margins.Top, "right": section.Margins.Right, "bottom": section.Margins.Bottom, "left": section.Margins.Left} {
		if value == "" {
			continue
		}
		twips, err := parseLayoutTwips(value)
		if err != nil {
			return fmt.Errorf("section.margins.%s: %w", name, err)
		}
		if twips > 14400 {
			return fmt.Errorf("section.margins.%s must not exceed 10in", name)
		}
	}
	return nil
}

func validPublicationID(value string) bool {
	return len(value) >= 1 && len(value) <= 63 && validID(value)
}

func validateFieldRoots(label string, fields map[string]any) error {
	for key := range fields {
		if key == "mdoc" || key == "page" {
			return fmt.Errorf("%s cannot define reserved root %q", label, key)
		}
	}
	return nil
}

func validateConfigPath(value string, mustBeRelative bool) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("path is required")
	}
	if mustBeRelative && filepathIsAbsOrTraversal(value) {
		return errors.New("path must stay relative to the config")
	}
	return nil
}

func filepathIsAbsOrTraversal(value string) bool {
	return filepath.IsAbs(value) || hasParentTraversal(value)
}

func namedPageTwips(size string) (int64, int64) {
	switch size {
	case "letter":
		return 12240, 15840
	case "legal":
		return 12240, 20160
	case "a4":
		return 11906, 16838
	default:
		return 0, 0
	}
}

func NamedPageTwips(size string) (int64, int64) { return namedPageTwips(size) }

func LayoutTwips(value string) (int64, error) { return parseLayoutTwips(value) }

func ValidatePageLayout(page PageConfig) error { return validatePage(page) }

func ValidateHeaderFooterLayout(name string, value HeaderFooterConfig) error {
	return validateHeaderFooter(name, value)
}

func parseLayoutTwips(value string) (int64, error) {
	match := layoutLengthPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, errors.New("must be an ASCII decimal followed by in, pt, cm, or mm")
	}
	number, ok := new(big.Rat).SetString(strings.TrimSuffix(value, match[2]))
	if !ok {
		return 0, errors.New("invalid decimal length")
	}
	factor := map[string]*big.Rat{
		"in": big.NewRat(1440, 1),
		"pt": big.NewRat(20, 1),
		"cm": big.NewRat(72000, 127),
		"mm": big.NewRat(7200, 127),
	}[match[2]]
	valueRat := new(big.Rat).Mul(number, factor)
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(valueRat.Num(), valueRat.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(valueRat.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("length is too large")
	}
	result, err := strconv.ParseInt(quotient.String(), 10, 64)
	if err != nil {
		return 0, errors.New("length is too large")
	}
	return result, nil
}

func oneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

func (c RuntimeConfig) Identity() string {
	if c.ProjectID != "" {
		return c.ProjectID
	}
	return c.WorkspaceID
}

type GlobalConfig struct {
	Version         int            `yaml:"version" json:"version"`
	DefaultProfile  string         `yaml:"default_profile" json:"default_profile"`
	PandocBinary    string         `yaml:"pandoc_binary" json:"pandoc_binary"`
	Output          OutputConfig   `yaml:"output" json:"output"`
	ProfileDefaults RuntimeProfile `yaml:"profile_defaults" json:"profile_defaults"`
}

type ProjectConfigV1 struct {
	Version     int                  `yaml:"version" json:"version"`
	WorkspaceID string               `yaml:"workspace_id" json:"workspace_id"`
	Profiles    map[string]ProfileV1 `yaml:"profiles" json:"profiles"`
}

type ProfileV1 struct {
	Sources           []string `yaml:"sources" json:"sources"`
	Entry             string   `yaml:"entry" json:"entry"`
	ReviewFolderName  string   `yaml:"review_folder_name" json:"review_folder_name"`
	StagingFolderName string   `yaml:"staging_folder_name" json:"staging_folder_name"`
	ReferenceDOCX     string   `yaml:"reference_docx" json:"reference_docx"`
	MaxTableColumns   int      `yaml:"max_table_columns" json:"max_table_columns"`
	StateFile         string   `yaml:"state_file" json:"state_file"`
}

type ProjectConfigV2 struct {
	Version        int                       `yaml:"version" json:"version"`
	ProjectID      string                    `yaml:"project_id" json:"project_id"`
	DefaultProfile string                    `yaml:"default_profile" json:"default_profile"`
	Root           string                    `yaml:"root" json:"root"`
	PandocBinary   string                    `yaml:"pandoc_binary" json:"pandoc_binary"`
	Output         OutputConfig              `yaml:"output" json:"output"`
	Profiles       map[string]RuntimeProfile `yaml:"profiles" json:"profiles"`
}

type ProjectConfigV3 struct {
	Version        int                       `yaml:"version" json:"version"`
	ProjectID      string                    `yaml:"project_id" json:"project_id"`
	DefaultProfile string                    `yaml:"default_profile" json:"default_profile"`
	Root           string                    `yaml:"root" json:"root"`
	PandocBinary   string                    `yaml:"pandoc_binary" json:"pandoc_binary"`
	Output         OutputConfig              `yaml:"output" json:"output"`
	Profiles       map[string]RuntimeProfile `yaml:"profiles" json:"profiles"`
}

func profileNames[V any](profiles map[string]V) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
