package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	goconfig "github.com/goliatone/go-config/config"
	"github.com/goliatone/mdoc/internal/sourcekey"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

type OriginKind string

const (
	OriginDefault OriginKind = "default"
	OriginGlobal  OriginKind = "global"
	OriginProject OriginKind = "project"
	OriginEnv     OriginKind = "environment"
	OriginFlag    OriginKind = "flag"
)

type ValueOrigin struct {
	Kind OriginKind `json:"kind"`
	Path string     `json:"path,omitempty"`
	Base string     `json:"base,omitempty"`
}

type OriginIndex map[string]ValueOrigin

type ResolverOptions struct {
	WorkingDir          string
	ProjectPath         string
	AllowMissingProject bool
	GlobalPath          string
	Profile             string
	Environment         map[string]string
	Overrides           map[string]any
	UserConfigDir       string
	DirectSources       []string
	Root                string
	Entry               string
}

type projectDocument struct {
	version        int
	workspaceID    string
	projectID      string
	defaultProfile string
	root           string
	pandocBinary   string
	output         OutputConfig
	profiles       map[string]RuntimeProfile
	rawLayer       map[string]any
}

type quietConfigLogger struct{}

func (quietConfigLogger) Debug(string, ...any) {}
func (quietConfigLogger) Info(string, ...any)  {}
func (quietConfigLogger) Error(string, ...any) {}

// ResolveOutput resolves the output settings without selecting or validating a
// profile. Commands use it to keep success and error encoding consistent even
// when another part of the project configuration is invalid.
func ResolveOutput(options ResolverOptions) (OutputConfig, error) {
	workingDir, err := resolveWorkingDir(options.WorkingDir)
	if err != nil {
		return OutputConfig{}, err
	}
	result := OutputConfig{Format: "human"}
	globalPath, err := resolveGlobalPath(options, workingDir)
	if err != nil {
		return OutputConfig{}, err
	}
	if output, found, err := readOutputSettings(globalPath, true, false); err != nil {
		return OutputConfig{}, err
	} else if found {
		applyOutputSettings(&result, output)
	}

	projectPath, err := absoluteOptionalPath(options.ProjectPath, workingDir)
	if err != nil {
		return OutputConfig{}, fmt.Errorf("resolve project config path: %w", err)
	}
	if projectPath == "" {
		projectPath, err = DiscoverProject(workingDir)
		if errors.Is(err, os.ErrNotExist) {
			projectPath = ""
			err = nil
		}
		if err != nil {
			return OutputConfig{}, err
		}
	}
	if projectPath != "" {
		if output, found, err := readOutputSettings(projectPath, false, true); err != nil {
			return OutputConfig{}, err
		} else if found {
			applyOutputSettings(&result, output)
		}
	}

	environment := options.Environment
	if environment == nil {
		environment = currentEnvironment()
	}
	if value, ok := environment["MDOC_OUTPUT_FORMAT"]; ok {
		result.Format = value
	}
	if value, ok := environment["MDOC_OUTPUT_QUIET"]; ok {
		quiet, err := strconv.ParseBool(value)
		if err != nil {
			return OutputConfig{}, fmt.Errorf("MDOC_OUTPUT_QUIET: %w", err)
		}
		result.Quiet = quiet
	}
	if output, ok := options.Overrides["output"].(map[string]any); ok {
		if value, ok := output["format"].(string); ok {
			result.Format = value
		}
		if value, ok := output["quiet"].(bool); ok {
			result.Quiet = value
		}
	}
	if !oneOf(result.Format, "human", "json") {
		return OutputConfig{}, errors.New("output.format must be human or json")
	}
	return result, nil
}

type optionalOutputSettings struct {
	Format *string `yaml:"format"`
	Quiet  *bool   `yaml:"quiet"`
}

type outputSettingsDocument struct {
	Version int                    `yaml:"version"`
	Output  optionalOutputSettings `yaml:"output"`
}

func readOutputSettings(path string, optional, project bool) (optionalOutputSettings, bool, error) {
	data, err := os.ReadFile(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return optionalOutputSettings{}, false, nil
	}
	if err != nil {
		return optionalOutputSettings{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var document outputSettingsDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		return optionalOutputSettings{}, false, fmt.Errorf("decode config %s: %w", path, err)
	}
	if project && document.Version != ProjectVersion2 && document.Version != ProjectVersion3 {
		return optionalOutputSettings{}, false, nil
	}
	return document.Output, document.Output.Format != nil || document.Output.Quiet != nil, nil
}

func applyOutputSettings(target *OutputConfig, source optionalOutputSettings) {
	if source.Format != nil {
		target.Format = *source.Format
	}
	if source.Quiet != nil {
		target.Quiet = *source.Quiet
	}
}

func Resolve(ctx context.Context, options ResolverOptions) (*RuntimeConfig, error) {
	workingDir, err := resolveWorkingDir(options.WorkingDir)
	if err != nil {
		return nil, err
	}
	projectPath, err := absoluteOptionalPath(options.ProjectPath, workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project config path: %w", err)
	}
	if projectPath == "" {
		projectPath, err = DiscoverProject(workingDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if errors.Is(err, os.ErrNotExist) {
			projectPath = ""
		}
	}
	if projectPath == "" {
		if !options.AllowMissingProject {
			return nil, errors.New("project config is required; use --config or run mdoc init")
		}
		return resolveDirect(ctx, workingDir, options)
	}
	project, err := readProject(projectPath)
	if err != nil {
		return nil, err
	}

	globalPath, err := resolveGlobalPath(options, workingDir)
	if err != nil {
		return nil, err
	}
	global, globalRaw, globalFound, err := readGlobal(globalPath)
	if err != nil {
		return nil, err
	}

	environment := options.Environment
	if environment == nil {
		environment = currentEnvironment()
	}
	profileName, err := selectRuntimeProfile(options.Profile, environment["MDOC_PROFILE"], project.defaultProfile, global.DefaultProfile, project.profiles)
	if err != nil {
		return nil, err
	}

	projectBase := filepath.Dir(projectPath)
	globalBase := filepath.Dir(globalPath)
	defaults := cloneMap(defaultRuntimeLayer(profileName))
	globalLayer := cloneMap(globalRuntimeLayer(global, globalRaw))
	projectLayer := cloneMap(projectRuntimeLayer(project, profileName))
	envLayer, err := environmentRuntimeLayer(environment)
	if err != nil {
		return nil, err
	}
	envLayer = cloneMap(envLayer)
	overrideLayer := cloneMap(options.Overrides)

	origins := OriginIndex{}
	mergeOrigins(origins, defaults, ValueOrigin{Kind: OriginDefault, Base: projectBase})
	if globalFound {
		mergeOrigins(origins, globalLayer, ValueOrigin{Kind: OriginGlobal, Path: globalPath, Base: globalBase})
	}
	mergeOrigins(origins, projectLayer, ValueOrigin{Kind: OriginProject, Path: projectPath, Base: projectBase})
	mergeOrigins(origins, envLayer, ValueOrigin{Kind: OriginEnv, Base: workingDir})
	mergeOrigins(origins, overrideLayer, ValueOrigin{Kind: OriginFlag, Base: workingDir})

	runtime := &RuntimeConfig{ProfileName: profileName, ProjectPath: projectPath, Origins: origins}
	if globalFound {
		runtime.GlobalPath = globalPath
	}
	container := goconfig.New(runtime).
		WithStrictDecode(true).
		WithDefaultTransformers(false).
		WithLogger(quietConfigLogger{}).
		WithProvider(
			goconfig.DefaultValuesProvider[*RuntimeConfig](defaults, 0),
			goconfig.DefaultValuesProvider[*RuntimeConfig](globalLayer, 10),
			goconfig.DefaultValuesProvider[*RuntimeConfig](projectLayer, 20),
			goconfig.DefaultValuesProvider[*RuntimeConfig](envLayer, 30),
			goconfig.DefaultValuesProvider[*RuntimeConfig](overrideLayer, 40),
		)
	if err := container.Load(ctx); err != nil {
		return nil, fmt.Errorf("resolve effective config: %w", err)
	}
	runtime.ProfileName = profileName
	runtime.ProjectPath = projectPath
	runtime.Origins = origins
	if globalFound {
		runtime.GlobalPath = globalPath
	}
	if err := resolveRuntimePaths(runtime); err != nil {
		return nil, err
	}
	if project.version == ProjectVersion2 || project.version == ProjectVersion3 {
		if err := validateV2StatePaths(project, global, globalFound, projectPath, globalPath, profileName, runtime.Profile.StateFile); err != nil {
			return nil, err
		}
	}
	if err := validateStylePaths(runtime); err != nil {
		return nil, err
	}
	if err := runtime.Validate(); err != nil {
		return nil, annotateOrigin(err, origins)
	}
	return runtime, nil
}

func validateV2StatePaths(project projectDocument, global GlobalConfig, globalFound bool, projectPath, globalPath, selectedProfile, selectedState string) error {
	names := profileNames(project.profiles)
	owners := map[string]string{}
	projectProfiles, _ := project.rawLayer["profiles"].(map[string]any)
	for _, name := range names {
		value := ""
		base := ""
		if globalFound && global.ProfileDefaults.StateFile != "" {
			value = global.ProfileDefaults.StateFile
			base = filepath.Dir(globalPath)
		}
		profileLayer, _ := projectProfiles[name].(map[string]any)
		if raw, exists := profileLayer["state_file"]; exists {
			configured, ok := raw.(string)
			if !ok {
				return fmt.Errorf("profile %q state_file must be a string", name)
			}
			value = configured
			base = filepath.Dir(projectPath)
		}
		if name == selectedProfile {
			value = selectedState
			base = ""
		}
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(base, filepath.FromSlash(value))
		}
		canonical, err := canonicalConfigPath(value)
		if err != nil {
			return fmt.Errorf("resolve profile %q state_file: %w", name, err)
		}
		key := strings.ToLower(norm.NFC.String(filepath.ToSlash(filepath.Clean(canonical))))
		if owner, exists := owners[key]; exists {
			return fmt.Errorf("profiles %q and %q use the same state_file %q; each profile must own a separate state file", owner, name, canonical)
		}
		owners[key] = name
	}
	return nil
}

func (runtime *RuntimeConfig) Legacy() (*Config, SelectedProfile) {
	entry := runtime.Profile.Entry
	entryPublication := ""
	if runtime.Version == ProjectVersion3 {
		entry = ""
		entryPublication = runtime.Profile.Entry
	}
	profile := Profile{
		Sources:           append([]string(nil), runtime.Profile.Sources.Include...),
		Excludes:          append([]string(nil), runtime.Profile.Sources.Exclude...),
		ExternalRoots:     append([]string(nil), runtime.Profile.Sources.ExternalRoots...),
		Entry:             entry,
		ReviewFolderName:  runtime.Profile.Destination.ReviewFolderName,
		StagingFolderName: runtime.Profile.Destination.StagingFolderName,
		ReferenceDOCX:     runtime.Profile.Style.ReferenceDOCX,
		MaxTableColumns:   runtime.Profile.Validation.MaxTableColumns,
		StateFile:         runtime.Profile.StateFile,
		ParentFolderID:    runtime.Profile.Destination.ParentFolderID,
		StagingFolderID:   runtime.Profile.Destination.StagingFolderID,
		ReviewFolderID:    runtime.Profile.Destination.ReviewFolderID,
		NamingSource:      runtime.Profile.Naming.Source,
		NameMapping:       runtime.Profile.Naming.Mapping,
		TitlePolicy:       runtime.Profile.Validation.Title,
		HeadingJumps:      runtime.Profile.Validation.HeadingJumps,
		LinkPolicy:        runtime.Profile.Validation.UnpublishedMarkdownLinks,
		ImageTypes:        append([]string(nil), runtime.Profile.Validation.ImageTypes...),
		HeadingFilter:     runtime.Profile.Style.HeadingFilter,
		Filters:           append([]string(nil), runtime.Profile.Style.Filters...),
		StyleRoots:        append([]string(nil), runtime.Profile.Style.Roots...),
		Reader:            runtime.Profile.Style.Reader,
		EntryPublication:  entryPublication,
		FieldDefaults:     cloneMap(runtime.Profile.FieldDefaults),
		Publications:      clonePublications(runtime.Profile.Publications),
		FieldRoots:        runtime.fieldRoots(),
	}
	legacy := &Config{
		Version:      runtime.Version,
		WorkspaceID:  runtime.Identity(),
		Profiles:     map[string]Profile{runtime.ProfileName: profile},
		Path:         runtime.ProjectPath,
		Root:         runtime.Root,
		PandocBinary: runtime.PandocBinary,
		Output:       runtime.Output,
	}
	return legacy, SelectedProfile{Name: runtime.ProfileName, Profile: profile}
}

func (runtime *RuntimeConfig) fieldRoots() []string {
	roots := []string{runtime.Root}
	if runtime.GlobalPath != "" {
		roots = append(roots, filepath.Dir(runtime.GlobalPath))
	}
	return roots
}

func (runtime *RuntimeConfig) Redacted() RuntimeConfig {
	redacted := *runtime
	redacted.Profile = runtime.Profile
	redacted.Profile.FieldDefaults = nil
	redacted.Profile.Publications = clonePublications(runtime.Profile.Publications)
	summary := &FieldConfigSummary{
		DefaultRoots: len(runtime.Profile.FieldDefaults),
		Publications: map[string]PublicationFieldSummary{},
		Warning:      "field values become document content; do not use fields for secrets",
	}
	for id, publication := range redacted.Profile.Publications {
		summary.Publications[id] = PublicationFieldSummary{InlineRoots: len(publication.Fields), FieldFiles: len(publication.FieldFiles)}
		publication.Fields = nil
		redacted.Profile.Publications[id] = publication
	}
	redacted.Profile.FieldSummary = summary
	return redacted
}

func clonePublications(source map[string]PublicationConfig) map[string]PublicationConfig {
	if source == nil {
		return nil
	}
	result := make(map[string]PublicationConfig, len(source))
	for id, publication := range source {
		publication.Members = append([]MemberConfig(nil), publication.Members...)
		publication.ReferenceLinks = append([]ReferenceLinkConfig(nil), publication.ReferenceLinks...)
		publication.FieldFiles = append([]string(nil), publication.FieldFiles...)
		publication.Fields = cloneMap(publication.Fields)
		publication.Layout.Cover.Fields = append([]CoverFieldConfig(nil), publication.Layout.Cover.Fields...)
		publication.Layout.TableOfContents.Members = cloneTOCMembers(publication.Layout.TableOfContents.Members)
		publication.Layout.Headings.CaptionStyles = append([]string(nil), publication.Layout.Headings.CaptionStyles...)
		publication.Layout.Tables.PreserveWidthStyles = append([]string(nil), publication.Layout.Tables.PreserveWidthStyles...)
		result[id] = publication
	}
	return result
}

func cloneTOCMembers(source []TOCMemberConfig) []TOCMemberConfig {
	if source == nil {
		return nil
	}
	result := make([]TOCMemberConfig, len(source))
	for index, member := range source {
		result[index] = member
		if member.Enabled != nil {
			enabled := *member.Enabled
			result[index].Enabled = &enabled
		}
		result[index].IncludeHeadings = append([]string(nil), member.IncludeHeadings...)
		result[index].ExcludeHeadings = append([]string(nil), member.ExcludeHeadings...)
	}
	return result
}

func DiscoverProject(start string) (string, error) {
	directory, err := resolveWorkingDir(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(directory, ".mdoc.yaml")
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() {
			return candidate, nil
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project config %s: %w", candidate, statErr)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", os.ErrNotExist
		}
		directory = parent
	}
}

func resolveDirect(ctx context.Context, workingDir string, options ResolverOptions) (*RuntimeConfig, error) {
	globalPath, err := resolveGlobalPath(options, workingDir)
	if err != nil {
		return nil, err
	}
	global, globalRaw, globalFound, err := readGlobal(globalPath)
	if err != nil {
		return nil, err
	}
	environment := options.Environment
	if environment == nil {
		environment = currentEnvironment()
	}
	defaults := cloneMap(defaultRuntimeLayer("direct"))
	delete(defaults, "project_id")
	delete(defaults, "workspace_id")
	globalLayer := cloneMap(globalRuntimeLayer(global, globalRaw))
	delete(globalLayer, "default_profile")
	envLayer, err := environmentRuntimeLayer(environment)
	if err != nil {
		return nil, err
	}
	envLayer = cloneMap(envLayer)
	overrideLayer := cloneMap(options.Overrides)
	directRoot := options.Root
	if directRoot == "" {
		directRoot = workingDir
	} else if !filepath.IsAbs(directRoot) {
		directRoot = filepath.Join(workingDir, directRoot)
	}
	directRoot, err = filepath.Abs(directRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve direct root: %w", err)
	}
	setPath(overrideLayer, "root", directRoot)
	if options.DirectSources != nil {
		setPath(overrideLayer, "profile.sources.include", append([]string(nil), options.DirectSources...))
	}
	if options.Entry != "" {
		setPath(overrideLayer, "profile.entry", options.Entry)
	}
	overrideLayer = cloneMap(overrideLayer)

	origins := OriginIndex{}
	mergeOrigins(origins, defaults, ValueOrigin{Kind: OriginDefault, Base: workingDir})
	if globalFound {
		mergeOrigins(origins, globalLayer, ValueOrigin{Kind: OriginGlobal, Path: globalPath, Base: filepath.Dir(globalPath)})
	}
	mergeOrigins(origins, envLayer, ValueOrigin{Kind: OriginEnv, Base: workingDir})
	mergeOrigins(origins, overrideLayer, ValueOrigin{Kind: OriginFlag, Base: directRoot})
	runtime := &RuntimeConfig{ProfileName: "direct", Direct: true, Origins: origins}
	if globalFound {
		runtime.GlobalPath = globalPath
	}
	container := goconfig.New(runtime).
		WithStrictDecode(true).
		WithDefaultTransformers(false).
		WithLogger(quietConfigLogger{}).
		WithProvider(
			goconfig.DefaultValuesProvider[*RuntimeConfig](defaults, 0),
			goconfig.DefaultValuesProvider[*RuntimeConfig](globalLayer, 10),
			goconfig.DefaultValuesProvider[*RuntimeConfig](envLayer, 30),
			goconfig.DefaultValuesProvider[*RuntimeConfig](overrideLayer, 40),
		)
	if err := container.Load(ctx); err != nil {
		return nil, fmt.Errorf("resolve direct config: %w", err)
	}
	runtime.ProfileName = "direct"
	runtime.Direct = true
	runtime.Origins = origins
	if globalFound {
		runtime.GlobalPath = globalPath
	}
	if err := resolveRuntimePaths(runtime); err != nil {
		return nil, err
	}
	if err := validateStylePaths(runtime); err != nil {
		return nil, err
	}
	if err := runtime.Validate(); err != nil {
		return nil, annotateOrigin(err, origins)
	}
	return runtime, nil
}

func defaultRuntimeLayer(profileName string) map[string]any {
	return map[string]any{
		"version":         ProjectVersion2,
		"root":            ".",
		"pandoc_binary":   "pandoc",
		"default_profile": profileName,
		"output":          map[string]any{"format": "human", "quiet": false},
		"profile": map[string]any{
			"sources": map[string]any{"include": []string{}, "exclude": []string{}, "external_roots": []string{}},
			"entry":   "",
			"naming":  map[string]any{"source": "file", "mapping": map[string]string{}},
			"validation": map[string]any{
				"title": "optional", "heading_jumps": "warning", "unpublished_markdown_links": "warning",
				"max_table_columns": 5, "image_types": []string{"png", "jpg", "jpeg", "svg"},
			},
			"style": map[string]any{"reference_docx": "", "heading_filter": "none", "filters": []string{}, "roots": []string{}, "reader": "gfm"},
			"destination": map[string]any{
				"mode": "immutable_review", "parent_folder_id": "", "staging_folder_id": "", "review_folder_id": "",
				"staging_folder_name": "Documents Staging", "review_folder_name": "Documents Review",
			},
			"state_file": "",
		},
	}
}

func globalRuntimeLayer(_ GlobalConfig, raw map[string]any) map[string]any {
	layer := cloneMap(raw)
	delete(layer, "version")
	if defaults, ok := layer["profile_defaults"]; ok {
		layer["profile"] = defaults
		delete(layer, "profile_defaults")
	}
	return layer
}

func projectRuntimeLayer(project projectDocument, profileName string) map[string]any {
	if project.version == ProjectVersion2 || project.version == ProjectVersion3 {
		layer := cloneMap(project.rawLayer)
		profiles, _ := layer["profiles"].(map[string]any)
		layer["profile"] = profiles[profileName]
		delete(layer, "profiles")
		return layer
	}
	layer := map[string]any{
		"version":      project.version,
		"workspace_id": project.workspaceID,
		"profile":      structMap(project.profiles[profileName]),
	}
	return layer
}

func readProject(path string) (projectDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return projectDocument{}, fmt.Errorf("read project config %s: %w", path, err)
	}
	var header struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return projectDocument{}, fmt.Errorf("decode project config %s: %w", path, err)
	}
	rawLayer, err := yamlMap(data)
	if err != nil {
		return projectDocument{}, fmt.Errorf("decode project config %s: %w", path, err)
	}
	switch header.Version {
	case ProjectVersion1:
		var source ProjectConfigV1
		if err := decodeStrictYAML(path, data, &source); err != nil {
			return projectDocument{}, err
		}
		if !validID(source.WorkspaceID) {
			return projectDocument{}, fmt.Errorf("project config %s: workspace_id must contain lowercase letters, digits, and single hyphens", path)
		}
		legacyProfiles := make(map[string]Profile, len(source.Profiles))
		for name, legacy := range source.Profiles {
			legacyProfiles[name] = Profile{
				Sources: legacy.Sources, Entry: legacy.Entry, ReviewFolderName: legacy.ReviewFolderName,
				StagingFolderName: legacy.StagingFolderName, ReferenceDOCX: legacy.ReferenceDOCX,
				MaxTableColumns: legacy.MaxTableColumns, StateFile: legacy.StateFile,
			}
		}
		legacyConfig := &Config{Version: 1, WorkspaceID: source.WorkspaceID, Profiles: legacyProfiles}
		if err := legacyConfig.Validate(); err != nil {
			return projectDocument{}, fmt.Errorf("project config %s: %w", path, err)
		}
		profiles := make(map[string]RuntimeProfile, len(source.Profiles))
		for name, legacy := range source.Profiles {
			profiles[name] = normalizeV1Profile(legacy)
		}
		return projectDocument{version: 1, workspaceID: source.WorkspaceID, root: ".", pandocBinary: "pandoc", profiles: profiles}, nil
	case ProjectVersion2:
		var source ProjectConfigV2
		if err := decodeStrictYAML(path, data, &source); err != nil {
			return projectDocument{}, err
		}
		if !validID(source.ProjectID) {
			return projectDocument{}, fmt.Errorf("project config %s: project_id must contain lowercase letters, digits, and single hyphens", path)
		}
		if len(source.Profiles) == 0 {
			return projectDocument{}, fmt.Errorf("project config %s: at least one profile is required", path)
		}
		for _, name := range profileNames(source.Profiles) {
			if !validID(name) {
				return projectDocument{}, fmt.Errorf("project config %s: invalid profile name %q", path, name)
			}
		}
		return projectDocument{version: 2, projectID: source.ProjectID, defaultProfile: source.DefaultProfile, root: source.Root, pandocBinary: source.PandocBinary, output: source.Output, profiles: source.Profiles, rawLayer: rawLayer}, nil
	case ProjectVersion3:
		var source ProjectConfigV3
		if err := decodeStrictYAML(path, data, &source); err != nil {
			return projectDocument{}, err
		}
		if !validID(source.ProjectID) {
			return projectDocument{}, fmt.Errorf("project config %s: project_id must contain lowercase letters, digits, and single hyphens", path)
		}
		if len(source.Profiles) == 0 {
			return projectDocument{}, fmt.Errorf("project config %s: at least one profile is required", path)
		}
		for _, name := range profileNames(source.Profiles) {
			if !validID(name) {
				return projectDocument{}, fmt.Errorf("project config %s: invalid profile name %q", path, name)
			}
			if err := validateVersion3Profile(name, source.Profiles[name], true); err != nil {
				return projectDocument{}, fmt.Errorf("project config %s: %w", path, err)
			}
		}
		return projectDocument{version: 3, projectID: source.ProjectID, defaultProfile: source.DefaultProfile, root: source.Root, pandocBinary: source.PandocBinary, output: source.Output, profiles: source.Profiles, rawLayer: rawLayer}, nil
	default:
		return projectDocument{}, fmt.Errorf("project config %s: unsupported version %d", path, header.Version)
	}
}

func readGlobal(path string) (GlobalConfig, map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return GlobalConfig{}, nil, false, nil
	}
	if err != nil {
		return GlobalConfig{}, nil, false, fmt.Errorf("read global config %s: %w", path, err)
	}
	var global GlobalConfig
	if err := decodeStrictYAML(path, data, &global); err != nil {
		return GlobalConfig{}, nil, false, err
	}
	if global.Version != GlobalVersion1 {
		return GlobalConfig{}, nil, false, fmt.Errorf("global config %s: unsupported version %d", path, global.Version)
	}
	raw, err := yamlMap(data)
	if err != nil {
		return GlobalConfig{}, nil, false, fmt.Errorf("decode global config %s: %w", path, err)
	}
	return global, raw, true, nil
}

func yamlMap(data []byte) (map[string]any, error) {
	var result map[string]any
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeStrictYAML(path string, data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode config %s: %w", path, err)
		}
		return fmt.Errorf("decode config %s: multiple YAML documents are not supported", path)
	}
	return nil
}

func normalizeV1Profile(source ProfileV1) RuntimeProfile {
	maxColumns := source.MaxTableColumns
	if maxColumns == 0 {
		maxColumns = 5
	}
	return RuntimeProfile{
		Sources:     SourceConfig{Include: append([]string(nil), source.Sources...), Exclude: []string{}, ExternalRoots: []string{}},
		Entry:       source.Entry,
		Naming:      NamingConfig{Source: "h1", Mapping: map[string]string{}},
		Validation:  ValidationConfig{Title: "required", HeadingJumps: "error", UnpublishedMarkdownLinks: "error", MaxTableColumns: maxColumns, ImageTypes: []string{"png", "jpg", "jpeg", "svg"}},
		Style:       StyleConfig{ReferenceDOCX: source.ReferenceDOCX, HeadingFilter: "title", Filters: []string{}, Roots: []string{}, Reader: "gfm"},
		Destination: DestinationConfig{Mode: "immutable_review", StagingFolderName: source.StagingFolderName, ReviewFolderName: source.ReviewFolderName},
		StateFile:   source.StateFile,
	}
}

func selectRuntimeProfile(explicit, environment, projectDefault, globalDefault string, profiles map[string]RuntimeProfile) (string, error) {
	for _, candidate := range []string{explicit, environment, projectDefault, globalDefault} {
		if candidate == "" {
			continue
		}
		if _, ok := profiles[candidate]; !ok {
			return "", fmt.Errorf("unknown profile %q; available profiles: %s", candidate, strings.Join(profileNames(profiles), ", "))
		}
		return candidate, nil
	}
	if len(profiles) == 1 {
		for name := range profiles {
			return name, nil
		}
	}
	if len(profiles) == 0 {
		return "", errors.New("project config must define at least one profile")
	}
	return "", fmt.Errorf("profile is required; available profiles: %s; use --profile or set default_profile", strings.Join(profileNames(profiles), ", "))
}

func environmentRuntimeLayer(environment map[string]string) (map[string]any, error) {
	layer := map[string]any{}
	set := func(path string, value any) { setPath(layer, path, value) }
	for key, value := range environment {
		switch key {
		case "MDOC_PROFILE":
			continue
		case "MDOC_PANDOC_BINARY":
			set("pandoc_binary", value)
		case "MDOC_ROOT":
			set("root", value)
		case "MDOC_OUTPUT_FORMAT":
			set("output.format", value)
		case "MDOC_OUTPUT_QUIET":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("MDOC_OUTPUT_QUIET: %w", err)
			}
			set("output.quiet", parsed)
		case "MDOC_ENTRY":
			set("profile.entry", value)
		case "MDOC_REFERENCE_DOCX":
			set("profile.style.reference_docx", value)
		case "MDOC_STATE_FILE":
			set("profile.state_file", value)
		case "MDOC_MAX_TABLE_COLUMNS":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("MDOC_MAX_TABLE_COLUMNS: %w", err)
			}
			set("profile.validation.max_table_columns", parsed)
		case "MDOC_SOURCES", "MDOC_EXCLUDES", "MDOC_FILTERS":
			var values []string
			if err := json.Unmarshal([]byte(value), &values); err != nil {
				return nil, fmt.Errorf("%s must be a JSON string list: %w", key, err)
			}
			path := map[string]string{"MDOC_SOURCES": "profile.sources.include", "MDOC_EXCLUDES": "profile.sources.exclude", "MDOC_FILTERS": "profile.style.filters"}[key]
			set(path, values)
		}
	}
	return layer, nil
}

func resolveRuntimePaths(runtime *RuntimeConfig) error {
	paths := []struct {
		key   string
		value *string
	}{
		{"root", &runtime.Root},
		{"profile.style.reference_docx", &runtime.Profile.Style.ReferenceDOCX},
		{"profile.state_file", &runtime.Profile.StateFile},
	}
	if runtime.Version != ProjectVersion3 && !isExternalSourceKey(runtime.Profile.Entry) {
		paths = append(paths, struct {
			key   string
			value *string
		}{"profile.entry", &runtime.Profile.Entry})
	}
	for _, item := range paths {
		resolved, err := resolveValuePath(*item.value, runtime.Origins[item.key])
		if err != nil {
			return fmt.Errorf("resolve %s: %w", item.key, err)
		}
		*item.value = resolved
	}
	for key, values := range map[string]*[]string{
		"profile.sources.include":        &runtime.Profile.Sources.Include,
		"profile.sources.exclude":        &runtime.Profile.Sources.Exclude,
		"profile.sources.external_roots": &runtime.Profile.Sources.ExternalRoots,
		"profile.style.filters":          &runtime.Profile.Style.Filters,
		"profile.style.roots":            &runtime.Profile.Style.Roots,
	} {
		origin := runtime.Origins[key]
		for index, value := range *values {
			resolved, err := resolveValuePath(value, origin)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			(*values)[index] = resolved
		}
	}
	for id, publication := range runtime.Profile.Publications {
		baseKey := "profile.publications." + id
		if publication.Source != "" {
			origin := runtime.Origins[baseKey+".source"]
			if origin.Base == "" && runtime.ProjectPath != "" {
				origin = ValueOrigin{Kind: OriginProject, Path: runtime.ProjectPath, Base: filepath.Dir(runtime.ProjectPath)}
			}
			resolved, err := resolveValuePath(publication.Source, origin)
			if err != nil {
				return fmt.Errorf("resolve %s.source: %w", baseKey, err)
			}
			publication.Source = resolved
		}
		for index := range publication.Members {
			key := fmt.Sprintf("%s.members.%d.source", baseKey, index)
			origin := runtime.Origins[key]
			if origin.Base == "" && runtime.ProjectPath != "" {
				origin = ValueOrigin{Kind: OriginProject, Path: runtime.ProjectPath, Base: filepath.Dir(runtime.ProjectPath)}
			}
			resolved, err := resolveValuePath(publication.Members[index].Source, origin)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			publication.Members[index].Source = resolved
		}
		for index := range publication.ReferenceLinks {
			key := fmt.Sprintf("%s.reference_links.%d.definition.source", baseKey, index)
			origin := runtime.Origins[key]
			if origin.Base == "" && runtime.ProjectPath != "" {
				origin = ValueOrigin{Kind: OriginProject, Path: runtime.ProjectPath, Base: filepath.Dir(runtime.ProjectPath)}
			}
			resolved, err := resolveValuePath(publication.ReferenceLinks[index].Definition.Source, origin)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			publication.ReferenceLinks[index].Definition.Source = resolved
		}
		for index := range publication.Layout.TableOfContents.Members {
			key := fmt.Sprintf("%s.layout.table_of_contents.members.%d.source", baseKey, index)
			origin := runtime.Origins[key]
			if origin.Base == "" && runtime.ProjectPath != "" {
				origin = ValueOrigin{Kind: OriginProject, Path: runtime.ProjectPath, Base: filepath.Dir(runtime.ProjectPath)}
			}
			resolved, err := resolveValuePath(publication.Layout.TableOfContents.Members[index].Source, origin)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			publication.Layout.TableOfContents.Members[index].Source = resolved
		}
		for index := range publication.FieldFiles {
			key := fmt.Sprintf("%s.field_files.%d", baseKey, index)
			origin := runtime.Origins[key]
			if origin.Base == "" && runtime.ProjectPath != "" {
				origin = ValueOrigin{Kind: OriginProject, Path: runtime.ProjectPath, Base: filepath.Dir(runtime.ProjectPath)}
			}
			resolved, err := resolveValuePath(publication.FieldFiles[index], origin)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			publication.FieldFiles[index] = resolved
		}
		runtime.Profile.Publications[id] = publication
	}
	return nil
}

func isExternalSourceKey(value string) bool {
	key, err := sourcekey.Normalize(value)
	if err != nil {
		return false
	}
	parts := strings.Split(key, "/")
	if len(parts) < 3 || parts[0] != "external" {
		return false
	}
	index, err := strconv.Atoi(parts[1])
	return err == nil && index > 0
}

func resolveValuePath(value string, origin ValueOrigin) (string, error) {
	if value == "" || filepath.IsAbs(value) {
		return value, nil
	}
	base := origin.Base
	if base == "" {
		return value, nil
	}
	return filepath.Abs(filepath.Join(base, filepath.FromSlash(value)))
}

func validateStylePaths(runtime *RuntimeConfig) error {
	if runtime.Profile.Style.ReferenceDOCX == "" && len(runtime.Profile.Style.Filters) == 0 {
		return nil
	}
	allowed := []string{runtime.Root}
	if runtime.GlobalPath != "" {
		allowed = append(allowed, filepath.Dir(runtime.GlobalPath))
	}
	allowed = append(allowed, runtime.Profile.Style.Roots...)
	realAllowed := make([]string, 0, len(allowed))
	for _, root := range allowed {
		real, err := canonicalConfigPath(root)
		if err != nil {
			return fmt.Errorf("resolve approved style root %q: %w", root, err)
		}
		realAllowed = append(realAllowed, real)
	}
	if reference := runtime.Profile.Style.ReferenceDOCX; reference != "" {
		real, err := canonicalConfigPath(reference)
		if err != nil {
			return fmt.Errorf("resolve reference DOCX %q: %w", reference, err)
		}
		if !pathWithinAny(realAllowed, real) {
			return fmt.Errorf("configured reference DOCX %q resolves outside the project root, global config directory, and approved style roots", reference)
		}
	}
	for _, filter := range runtime.Profile.Style.Filters {
		info, err := os.Stat(filter)
		if err != nil {
			return fmt.Errorf("configured Lua filter %q is unavailable: %w", filter, err)
		}
		if info.IsDir() {
			return fmt.Errorf("configured Lua filter %q is a directory", filter)
		}
		real, err := canonicalConfigPath(filter)
		if err != nil {
			return fmt.Errorf("resolve Lua filter %q: %w", filter, err)
		}
		if !pathWithinAny(realAllowed, real) {
			return fmt.Errorf("configured Lua filter %q resolves outside the project root, global config directory, and approved style roots", filter)
		}
	}
	return nil
}

func canonicalConfigPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	candidate := abs
	suffix := []string{}
	for {
		real, evalErr := filepath.EvalSymlinks(candidate)
		if evalErr == nil {
			return filepath.Join(append([]string{real}, suffix...)...), nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", evalErr
		}
		suffix = append([]string{filepath.Base(candidate)}, suffix...)
		candidate = parent
	}
}

func pathWithinAny(roots []string, path string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func mergeOrigins(index OriginIndex, layer map[string]any, origin ValueOrigin) {
	flattenLayer("", layer, func(path string, _ any) { index[path] = origin })
}

func flattenLayer(prefix string, value any, visit func(string, any)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flattenLayer(path, child, visit)
		}
	default:
		visit(prefix, value)
	}
}

func structMap(value any) map[string]any {
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	data, _ := json.Marshal(input)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func setPath(target map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := target
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func annotateOrigin(err error, origins OriginIndex) error {
	message := err.Error()
	for path, origin := range origins {
		if strings.Contains(message, path) {
			return fmt.Errorf("%s (from %s %s)", message, origin.Kind, origin.Path)
		}
	}
	return err
}

func resolveWorkingDir(value string) (string, error) {
	if value == "" {
		return os.Getwd()
	}
	return filepath.Abs(value)
}

func absoluteOptionalPath(value, base string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Abs(value)
}

func resolveGlobalPath(options ResolverOptions, workingDir string) (string, error) {
	if options.GlobalPath != "" {
		return absoluteOptionalPath(options.GlobalPath, workingDir)
	}
	root := options.UserConfigDir
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config directory: %w", err)
		}
	}
	return filepath.Join(root, "mdoc", "config.yaml"), nil
}

func currentEnvironment() map[string]string {
	result := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
