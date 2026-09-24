package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

type Config struct {
	Version      int                `yaml:"version"`
	WorkspaceID  string             `yaml:"workspace_id"`
	Profiles     map[string]Profile `yaml:"profiles"`
	Path         string             `yaml:"-"`
	Root         string             `yaml:"-"`
	PandocBinary string             `yaml:"-"`
	Output       OutputConfig       `yaml:"-"`
}

type Profile struct {
	Sources           []string                     `yaml:"sources"`
	Excludes          []string                     `yaml:"-"`
	ExternalRoots     []string                     `yaml:"-"`
	Entry             string                       `yaml:"entry"`
	ReviewFolderName  string                       `yaml:"review_folder_name"`
	StagingFolderName string                       `yaml:"staging_folder_name"`
	ReferenceDOCX     string                       `yaml:"reference_docx"`
	MaxTableColumns   int                          `yaml:"max_table_columns"`
	StateFile         string                       `yaml:"state_file"`
	ParentFolderID    string                       `yaml:"-"`
	StagingFolderID   string                       `yaml:"-"`
	ReviewFolderID    string                       `yaml:"-"`
	NamingSource      string                       `yaml:"-"`
	NameMapping       map[string]string            `yaml:"-"`
	TitlePolicy       string                       `yaml:"-"`
	HeadingJumps      string                       `yaml:"-"`
	LinkPolicy        string                       `yaml:"-"`
	ImageTypes        []string                     `yaml:"-"`
	HeadingFilter     string                       `yaml:"-"`
	Filters           []string                     `yaml:"-"`
	StyleRoots        []string                     `yaml:"-"`
	Reader            string                       `yaml:"-"`
	EntryPublication  string                       `yaml:"-"`
	FieldDefaults     map[string]any               `yaml:"-"`
	Publications      map[string]PublicationConfig `yaml:"-"`
	FieldRoots        []string                     `yaml:"-"`
}

type SelectedProfile struct {
	Name string
	Profile
}

func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", abs, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", abs, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode config %s: %w", abs, err)
		}
		return nil, fmt.Errorf("decode config %s: multiple YAML documents are not supported", abs)
	}
	cfg.Path = abs
	cfg.Root = filepath.Dir(abs)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.resolvePaths()
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d; expected %d", c.Version, CurrentVersion)
	}
	if !validID(c.WorkspaceID) {
		return errors.New("workspace_id must contain lowercase letters, digits, and single hyphens")
	}
	if len(c.Profiles) == 0 {
		return errors.New("config must define at least one profile")
	}
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	stateOwners := map[string]string{}
	for _, name := range names {
		profile := c.Profiles[name]
		if !validID(name) {
			return fmt.Errorf("profile %q must contain lowercase letters, digits, and single hyphens", name)
		}
		if len(profile.Sources) == 0 {
			return fmt.Errorf("profile %q must define sources", name)
		}
		for _, value := range append(append([]string{}, profile.Sources...), profile.Entry, profile.ReferenceDOCX) {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("profile %q contains an empty required path", name)
			}
			if filepath.IsAbs(value) || hasParentTraversal(value) {
				return fmt.Errorf("profile %q path %q must stay relative to the config", name, value)
			}
		}
		if strings.TrimSpace(profile.ReviewFolderName) == "" || strings.TrimSpace(profile.StagingFolderName) == "" {
			return fmt.Errorf("profile %q must define staging_folder_name and review_folder_name", name)
		}
		if profile.ReviewFolderName == profile.StagingFolderName {
			return fmt.Errorf("profile %q staging and review folder names must differ", name)
		}
		if profile.MaxTableColumns < 0 {
			return fmt.Errorf("profile %q max_table_columns cannot be negative", name)
		}
		if profile.StateFile != "" && (filepath.IsAbs(profile.StateFile) || hasParentTraversal(profile.StateFile)) {
			return fmt.Errorf("profile %q state_file must stay relative to the config", name)
		}
		if profile.StateFile != "" {
			key := strings.ToLower(norm.NFC.String(filepath.ToSlash(filepath.Clean(filepath.FromSlash(profile.StateFile)))))
			if owner, exists := stateOwners[key]; exists {
				return fmt.Errorf("profiles %q and %q use the same state_file; each profile must own a separate state file", owner, name)
			}
			stateOwners[key] = name
		}
	}
	return nil
}

func (c *Config) Select(name string) (SelectedProfile, error) {
	profile, ok := c.Profiles[name]
	if !ok {
		names := make([]string, 0, len(c.Profiles))
		for candidate := range c.Profiles {
			names = append(names, candidate)
		}
		sort.Strings(names)
		return SelectedProfile{}, fmt.Errorf("unknown profile %q; available profiles: %s", name, strings.Join(names, ", "))
	}
	return SelectedProfile{Name: name, Profile: profile}, nil
}

func (c *Config) resolvePaths() {
	for name, profile := range c.Profiles {
		for index, source := range profile.Sources {
			profile.Sources[index] = filepath.Join(c.Root, filepath.FromSlash(source))
		}
		profile.Entry = filepath.Join(c.Root, filepath.FromSlash(profile.Entry))
		profile.ReferenceDOCX = filepath.Join(c.Root, filepath.FromSlash(profile.ReferenceDOCX))
		if profile.StateFile != "" {
			profile.StateFile = filepath.Join(c.Root, filepath.FromSlash(profile.StateFile))
		}
		if profile.MaxTableColumns == 0 {
			profile.MaxTableColumns = 5
		}
		c.Profiles[name] = profile
	}
}

func validID(value string) bool {
	if value == "" || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	previousHyphen := false
	for _, r := range value {
		if r == '-' {
			if previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		previousHyphen = false
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func hasParentTraversal(path string) bool {
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

const DefaultYAML = `version: 1
workspace_id: my-documents

profiles:
  work:
    sources:
      - docs/*.md
    entry: docs/README.md
    review_folder_name: My Documents Review
    staging_folder_name: My Documents Staging
    reference_docx: document-styles/work-reference.docx
    max_table_columns: 5
    state_file: ""
`

func WriteDefault(path string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("config %s already exists; use --force to replace it", path)
		}
		return fmt.Errorf("create config %s: %w", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(DefaultYAML); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
