package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

func NeutralProfile() RuntimeProfile {
	return RuntimeProfile{
		Sources: SourceConfig{Include: []string{"README.md"}, Exclude: []string{}, ExternalRoots: []string{}},
		Entry:   "README.md",
		Naming:  NamingConfig{Source: "file", Mapping: map[string]string{}},
		Validation: ValidationConfig{
			Title: "optional", HeadingJumps: "warning", UnpublishedMarkdownLinks: "warning",
			MaxTableColumns: 5, ImageTypes: []string{"png", "jpg", "jpeg", "svg"},
		},
		Style: StyleConfig{HeadingFilter: "none", Filters: []string{}, Roots: []string{}, Reader: "gfm"},
		Destination: DestinationConfig{
			Mode: "immutable_review", StagingFolderName: "Documents Staging", ReviewFolderName: "Documents Review",
		},
	}
}

func WriteNeutralProject(path, profileName string, force bool) error {
	if !validID(profileName) {
		return fmt.Errorf("profile %q must contain lowercase letters, digits, and single hyphens", profileName)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if !force {
		if _, err := os.Stat(abs); err == nil {
			return fmt.Errorf("config %s already exists; use --force to replace it", abs)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect config %s: %w", abs, err)
		}
	}
	projectID, err := generatedProjectID()
	if err != nil {
		return err
	}
	project := ProjectConfigV2{
		Version:        ProjectVersion2,
		ProjectID:      projectID,
		DefaultProfile: profileName,
		Root:           ".",
		PandocBinary:   "pandoc",
		Output:         OutputConfig{Format: "human"},
		Profiles:       map[string]RuntimeProfile{profileName: NeutralProfile()},
	}
	return writeProjectAtomic(abs, project, force)
}

func AddProfile(path, name string, replace bool) error {
	if !validID(name) {
		return fmt.Errorf("profile %q must contain lowercase letters, digits, and single hyphens", name)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read project config %s: %w", abs, err)
	}
	var header struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return fmt.Errorf("decode project config %s: %w", abs, err)
	}
	if header.Version != ProjectVersion2 {
		return fmt.Errorf("profile add requires a version 2 project config; %s is version %d", abs, header.Version)
	}
	var project ProjectConfigV2
	if err := decodeStrictYAML(abs, data, &project); err != nil {
		return err
	}
	if project.Profiles == nil {
		project.Profiles = map[string]RuntimeProfile{}
	}
	if _, exists := project.Profiles[name]; exists && !replace {
		return fmt.Errorf("profile %q already exists; use --force to replace it", name)
	}
	project.Profiles[name] = NeutralProfile()
	return writeProjectAtomic(abs, project, true)
}

func ListProfiles(projectPath, workingDir string) (string, []string, error) {
	path, err := absoluteOptionalPath(projectPath, workingDir)
	if err != nil {
		return "", nil, err
	}
	if path == "" {
		path, err = DiscoverProject(workingDir)
		if err != nil {
			return "", nil, fmt.Errorf("project config is required: %w", err)
		}
	}
	project, err := readProject(path)
	if err != nil {
		return "", nil, err
	}
	names := profileNames(project.profiles)
	sort.Strings(names)
	return path, names, nil
}

func writeProjectAtomic(path string, project ProjectConfigV2, replace bool) error {
	data, err := yaml.Marshal(project)
	if err != nil {
		return fmt.Errorf("encode project config: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".mdoc-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if !replace {
		if err := os.Link(temporaryPath, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("config %s already exists; use --force to replace it", path)
			}
			return fmt.Errorf("create config %s: %w", path, err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return fmt.Errorf("remove temporary config: %w", err)
		}
		removeTemporary = false
		return nil
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	removeTemporary = false
	return nil
}

func generatedProjectID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate project identity: %w", err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16]), nil
}
