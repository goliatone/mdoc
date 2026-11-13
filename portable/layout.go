package portable

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"path"
	"strings"
	"unicode"
)

func fold(s string) string { return cases.Fold().String(norm.NFC.String(s)) }
func name(c Config, s string) string {
	for _, p := range c.Naming.Preserve {
		if p == s {
			return s
		}
	}
	if c.Naming.Case == "lower" {
		s = strings.ToLower(s)
	}
	var b strings.Builder
	sep := false
	for _, r := range norm.NFC.String(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if sep && b.Len() > 0 {
				b.WriteString(c.Naming.Separator)
			}
			b.WriteRune(r)
			sep = false
		} else {
			sep = true
		}
	}
	return b.String()
}
func expand(p string, values map[string]string) string {
	for k, v := range values {
		p = strings.ReplaceAll(p, "{"+k+"}", v)
	}
	return p
}
func documentPath(c Config, e IndexEntry) (string, error) {
	if e.Section != "" {
		if _, ok := c.Layout.Sections[e.Section]; !ok {
			return "", fail("invalid_config", "unknown section %q", e.Section)
		}
	}
	if e.Path != "" {
		return e.Path, safePath(e.Path)
	}
	dir := ""
	if e.Section != "" {
		var ok bool
		dir, ok = c.Layout.Sections[e.Section]
		if !ok {
			return "", fail("invalid_config", "unknown section %q", e.Section)
		}
	}
	base := expand(c.Naming.DocumentPattern, map[string]string{"id": name(c, e.ID), "slug": name(c, e.Label)}) + ".md"
	for _, p := range c.Naming.Preserve {
		if e.Label == p {
			base = p
		}
	}
	p := path.Join(dir, base)
	return p, safePath(p)
}
func listingPath(c Config, e IndexEntry) string {
	return expand(c.Layout.AssetIndex, map[string]string{"id": name(c, e.ID), "slug": name(c, e.Label)})
}
func reserve(all *[]Reservation, r Reservation) error {
	if err := safePath(r.Path); err != nil {
		return err
	}
	for i, p := range *all {
		a, b := fold(p.Path), fold(r.Path)
		if a == b {
			if p.Path == r.Path && p.Kind == "asset" && r.Kind == "asset" {
				(*all)[i].PendingEquality = true
				r.PendingEquality = true
				continue
			}
			return fail("path_conflict", "%s conflicts with %s", r.Path, p.Path)
		}
		if strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
			return fail("path_conflict", "file/directory conflict between %s and %s", r.Path, p.Path)
		}
	}
	*all = append(*all, r)
	return nil
}
func resolveLayout(c Config, entries []IndexEntry) (Plan, error) {
	p := Plan{SchemaVersion: 1, Project: c.Project, ConfigDigest: configDigest(c), Entries: append([]IndexEntry(nil), entries...)}
	for _, r := range []Reservation{{Path: c.Layout.Index, ID: "index", Kind: "document"}, {Path: "manifest.json", ID: "manifest", Kind: "generated"}} {
		if e := reserve(&p.Destinations, r); e != nil {
			return p, e
		}
	}
	for i, e := range p.Entries {
		switch e.Role {
		case "document":
			dest, err := documentPath(c, e)
			if err != nil {
				return p, err
			}
			p.Entries[i].Path = dest
			if err = reserve(&p.Destinations, Reservation{Path: dest, ID: e.ID, Kind: "document"}); err != nil {
				return p, err
			}
		case "asset":
			if err := reserve(&p.Destinations, Reservation{Path: e.Path, ID: e.ID, Kind: "asset"}); err != nil {
				return p, err
			}
		case "asset-tree":
			if err := safePath(e.Path); err != nil {
				return p, err
			}
			listing := listingPath(c, e)
			for _, tree := range entries {
				if tree.Role == "asset-tree" && (fold(listing) == fold(tree.Path) || strings.HasPrefix(fold(listing), fold(tree.Path)+"/")) {
					return p, fail("path_conflict", "listing %s must be outside asset tree %s", listing, tree.Path)
				}
			}
			if err := reserve(&p.Destinations, Reservation{Path: listing, ID: e.ID, Kind: "generated"}); err != nil {
				return p, err
			}
		}
	}
	p.Diagnostics = append(p.Diagnostics, Diagnostic{Code: "unresolved_image_paths", Severity: "info", Message: "Image destinations and exact asset equality require export bytes; plan does not establish completeness."})
	return p, nil
}
