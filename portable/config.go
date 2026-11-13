package portable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version int         `yaml:"version" json:"version"`
	Project string      `yaml:"project" json:"project"`
	Index   IndexConfig `yaml:"index" json:"index"`
	Layout  Layout      `yaml:"layout" json:"layout"`
	Naming  Naming      `yaml:"naming" json:"naming"`
	Links   LinkPolicy  `yaml:"links" json:"links"`
	Limits  Limits      `yaml:"limits" json:"limits"`
}
type IndexConfig struct {
	Source           string `yaml:"source" json:"source"`
	InventoryHeading string `yaml:"inventory_heading" json:"inventory_heading"`
}
type Layout struct {
	Index      string            `yaml:"index" json:"index"`
	Sections   map[string]string `yaml:"sections" json:"sections"`
	Assets     string            `yaml:"assets" json:"assets"`
	AssetIndex string            `yaml:"asset_index" json:"asset_index"`
}
type Naming struct {
	Case            string   `yaml:"case" json:"case"`
	Separator       string   `yaml:"separator" json:"separator"`
	DocumentPattern string   `yaml:"document_pattern" json:"document_pattern"`
	AssetPattern    string   `yaml:"asset_pattern" json:"asset_pattern"`
	Preserve        []string `yaml:"preserve" json:"preserve"`
}
type LinkPolicy struct {
	UnresolvedFragments string `yaml:"unresolved_fragments" json:"unresolved_fragments"`
}
type Limits struct {
	Documents   int   `yaml:"documents" json:"documents"`
	Assets      int   `yaml:"assets" json:"assets"`
	Depth       int   `yaml:"depth" json:"depth"`
	SourceBytes int64 `yaml:"source_bytes" json:"source_bytes"`
	ImageBytes  int64 `yaml:"image_bytes" json:"image_bytes"`
	TotalBytes  int64 `yaml:"total_bytes" json:"total_bytes"`
	Attempts    int   `yaml:"attempts" json:"attempts"`
}

func DefaultConfig() Config {
	return Config{Version: 1, Index: IndexConfig{InventoryHeading: "Bundle inventory"}, Layout: Layout{Index: "00-START-HERE.md", Assets: "{document_dir}/assets/{document_stem}", AssetIndex: "navigation/{id}.md", Sections: map[string]string{}}, Naming: Naming{Case: "lower", Separator: "-", DocumentPattern: "{id}-{slug}", AssetPattern: "{slug}-{digest8}", Preserve: []string{"README.md", "00-START-HERE.md"}}, Links: LinkPolicy{"preserve"}, Limits: Limits{100, 1000, 8, 10 << 20, 20 << 20, 500 << 20, 3}}
}
func DecodeConfig(data []byte) (Config, error) {
	c := DefaultConfig()
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, fail("invalid_config", "invalid configuration: %s", err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fail("invalid_config", "expected one YAML document")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.Version != 1 || strings.TrimSpace(c.Project) == "" || strings.TrimSpace(c.Index.Source) == "" || c.Index.InventoryHeading == "" {
		return fail("invalid_config", "version 1, project, index source and inventory heading are required")
	}
	if c.Naming.Case != "lower" && c.Naming.Case != "preserve" || c.Naming.Separator != "-" && c.Naming.Separator != "_" {
		return fail("invalid_config", "invalid naming case or separator")
	}
	if c.Links.UnresolvedFragments != "preserve" && c.Links.UnresolvedFragments != "error" {
		return fail("invalid_config", "invalid fragment policy")
	}
	for _, v := range []struct {
		p      string
		tokens []string
	}{{c.Naming.DocumentPattern, []string{"id", "slug"}}, {c.Naming.AssetPattern, []string{"slug", "digest8", "digest"}}, {c.Layout.Assets, []string{"document_dir", "document_stem"}}, {c.Layout.AssetIndex, []string{"id", "slug"}}} {
		if err := validateTemplate(v.p, v.tokens); err != nil {
			return err
		}
	}
	for _, v := range append([]string{c.Layout.Index}, c.Naming.Preserve...) {
		if err := safePath(v); err != nil {
			return err
		}
	}
	if !strings.HasSuffix(c.Layout.Index, ".md") || !strings.HasSuffix(c.Layout.AssetIndex, ".md") {
		return fail("invalid_config", "index and asset_index must end in .md")
	}
	for k, v := range c.Layout.Sections {
		if k == "" {
			return fail("invalid_config", "empty section key")
		}
		if err := safePath(v); err != nil {
			return err
		}
	}
	l := c.Limits
	if l.Documents < 1 || l.Documents > 1000 || l.Assets < 1 || l.Assets > 10000 || l.Depth < 1 || l.Depth > 32 || l.SourceBytes < 1 || l.SourceBytes > 50<<20 || l.ImageBytes < 1 || l.ImageBytes > 100<<20 || l.TotalBytes < 1 || l.TotalBytes > 2<<30 || l.Attempts < 1 || l.Attempts > 3 {
		return fail("invalid_config", "limits exceed finite ceilings (1000 documents, 10000 assets, depth 32, source 50 MiB, image 100 MiB, total 2 GiB, 3 attempts)")
	}
	return nil
}

var tokenPattern = regexp.MustCompile(`\{([^{}]+)\}`)

func validateTemplate(p string, allowed []string) error {
	if p == "" {
		return fail("invalid_config", "empty path template")
	}
	for _, m := range tokenPattern.FindAllStringSubmatch(p, -1) {
		ok := false
		for _, a := range allowed {
			ok = ok || m[1] == a
		}
		if !ok {
			return fail("invalid_config", "unknown template token %s", m[1])
		}
	}
	expanded := tokenPattern.ReplaceAllString(p, "value")
	if strings.ContainsAny(expanded, "{}") {
		return fail("invalid_config", "malformed template")
	}
	return safePath(expanded)
}
func digest(b []byte) string       { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func configDigest(c Config) string { b, _ := json.Marshal(c); return digest(b) }

// safePath is conservative across Unix, Windows and common Markdown readers.
func safePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00?#%") {
		return fail("path_escape", "unsafe relative path %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return fail("path_escape", "unsafe relative path %q", p)
		}
		for _, r := range part {
			if r < 32 || strings.ContainsRune(`<>"|*`, r) {
				return fail("path_escape", "unsafe relative path %q", p)
			}
		}
		base := strings.ToUpper(strings.Split(part, ".")[0])
		if regexp.MustCompile(`^(CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])$`).MatchString(base) {
			return fail("path_escape", "reserved path %q", p)
		}
	}
	return nil
}

// LoadConfig resolves a local index relative to its configuration file.
func LoadConfig(filename string) (Config, error) {
	f, e := os.Open(filename)
	if e != nil {
		return Config{}, fail("invalid_config", "cannot read bundle configuration")
	}
	defer f.Close()
	b, e := readBounded(f, 1<<20)
	if e != nil {
		return Config{}, e
	}
	c, e := DecodeConfig(b)
	if e != nil {
		return c, e
	}
	if _, e := ParseSource(c.Index.Source); e != nil && !strings.Contains(c.Index.Source, "://") && !filepath.IsAbs(c.Index.Source) {
		c.Index.Source = filepath.Join(filepath.Dir(filename), c.Index.Source)
	}
	return c, nil
}
