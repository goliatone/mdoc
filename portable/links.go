package portable

import (
	"context"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

type localDocument struct {
	id, path string
	capture  finalizedCapture
	anchors  map[string]bool
}

func (s *Service) prepareAnchors(ctx context.Context, d *localDocument) error {
	before := []string{}
	walk(d.capture.model, func(n map[string]any) {
		if n["t"] == "Header" {
			c := arr(n["c"])
			before = append(before, str(arr(c[1])[0]))
		}
	})
	b, e := s.converter.Render(ctx, d.capture.model)
	if e != nil {
		return e
	}
	m, e := s.converter.Parse(ctx, b)
	if e != nil {
		return e
	}
	d.anchors = map[string]bool{}
	heads := map[string]string{}
	i := 0
	walk(m, func(n map[string]any) {
		if n["t"] == "Header" {
			c := arr(n["c"])
			id := str(arr(c[1])[0])
			d.anchors[id] = true
			if i < len(before) && before[i] != "" {
				heads[before[i]] = id
			}
			i++
		}
	})
	if i != len(before) {
		return fail("unsupported_content", "heading conversion changed structure")
	}
	d.capture.headings = heads
	return nil
}
func relativeURI(from, to, fragment string) string {
	r, _ := filepath.Rel(filepath.FromSlash(path.Dir(from)), filepath.FromSlash(to))
	u := url.URL{Path: filepath.ToSlash(r), Fragment: fragment}
	return u.String()
}
func (s *Service) rewriteLink(ctx context.Context, original string, from *localDocument, docs map[string]*localDocument, paths map[string]string, cat *catalog, policy string) (string, bool, string, error) {
	original = publicSourceLink(original)
	u, e := url.Parse(original)
	if e != nil {
		return "", false, "", fail("ambiguous_target", "invalid document link")
	}
	id := ""
	requestedTab := ""
	fragment := u.Fragment
	if original == "" {
		return "", false, "", fail("ambiguous_target", "empty link")
	}
	if u.Path == "" && u.Scheme == "" && u.Host == "" && u.RawQuery == "" {
		id = from.capture.evidence.Source.FileID
	} else if ref, err := ParseSource(original); err == nil {
		id = ref.FileID
		requestedTab = ref.TabID
	} else if u.IsAbs() || u.Host != "" {
		return original, true, "online_reference", nil
	} else {
		clean := *u
		clean.Fragment = ""
		id, e = s.resolveRelative(ctx, clean.String(), from.capture.evidence, cat)
		if e != nil {
			return "", false, "", e
		}
	}
	dest, ok := paths[id]
	if !ok {
		if u.IsAbs() {
			return original, true, "online_reference", nil
		}
		f := cat.files[id]
		if f == nil {
			return "", false, "", fail("missing_asset", "relative link has no canonical fallback")
		}
		return canonical(SourceRef{FileID: id, Kind: "drive"}) + fragmentSuffix(fragment), true, "online_reference", nil
	}
	target := docs[id]
	unresolved := false
	anchor := fragment
	if requestedTab != "" && (target == nil || target.capture.evidence.Source.TabID != requestedTab) {
		unresolved = true
	}
	if fragment != "" {
		if target == nil {
			unresolved = true
		} else if mapped, ok := target.capture.headings[strings.TrimPrefix(fragment, "heading=")]; ok {
			anchor = mapped
		} else if !target.anchors[fragment] {
			unresolved = true
		}
	}
	if unresolved {
		if policy == "error" {
			return "", false, "", fail("unresolved_fragment", "cannot verify section or tab selector")
		}
		if !u.IsAbs() {
			if target == nil || target.capture.evidence.CanonicalURL == "" {
				return "", false, "", fail("unresolved_fragment", "local section has no canonical fallback")
			}
			original = target.capture.evidence.CanonicalURL + fragmentSuffix(fragment)
		}
		return original, true, "unresolved_fragment", nil
	}
	return relativeURI(from.path, dest, anchor), false, "", nil
}
func fragmentSuffix(s string) string {
	if s == "" {
		return ""
	}
	u := url.URL{Fragment: s}
	return u.String()
}
func (s *Service) rewriteDocument(ctx context.Context, d *localDocument, c Config, docs map[string]*localDocument, paths map[string]string, cat *catalog) ([]byte, []LinkRecord, []Diagnostic, error) {
	links := []LinkRecord{}
	diagnostics := []Diagnostic{}
	var failure error
	position := 0
	assets := map[string]capturedAsset{}
	for _, a := range d.capture.assets {
		assets[a.objectID] = a
	}
	walk(d.capture.model, func(n map[string]any) {
		if failure != nil {
			return
		}
		switch n["t"] {
		case "Link":
			old := target(n)
			rewritten, online, code, e := s.rewriteLink(ctx, old, d, docs, paths, cat, c.Links.UnresolvedFragments)
			if e != nil {
				failure = e
				return
			}
			setTarget(n, rewritten)
			links = append(links, LinkRecord{d.path, rewritten, online})
			if code != "" {
				diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: "info", DocumentID: d.id, Reference: rewritten, Message: "Navigation retains an online dependency."})
			}
		case "Image":
			a, ok := assets[target(n)]
			if !ok || a.path == "" || position >= len(d.capture.occurrences) {
				failure = fail("capture_integrity", "image has no finalized local mapping")
				return
			}
			local := relativeURI(d.path, a.path, "")
			setTarget(n, local)
			d.capture.occurrences[position].AssetPath = a.path
			d.capture.occurrences[position].DocumentID = d.id
			position++
			links = append(links, LinkRecord{d.path, local, false})
		}
	})
	if failure != nil {
		return nil, nil, nil, failure
	}
	b, e := s.converter.Render(ctx, d.capture.model)
	return b, links, diagnostics, e
}
func fillInventoryPaths(m map[string]any, p Plan, heading string) {
	active := false
	level := 0
	byID := map[string]string{}
	for _, e := range p.Entries {
		byID[e.ID] = e.Path
	}
	for _, v := range arr(m["blocks"]) {
		n := obj(v)
		c := arr(n["c"])
		if n["t"] == "Header" {
			if textOf(c[2]) == heading {
				active = true
				level = num(c[0])
			} else if num(c[0]) <= level {
				active = false
			}
		}
		if !active || n["t"] != "Table" {
			continue
		}
		for _, body := range arr(c[4]) {
			parts := arr(body)
			for _, row := range arr(parts[3]) {
				cells := arr(arr(row)[1])
				if len(cells) != 5 {
					continue
				}
				id := textOf(arr(cells[0])[4])
				if p, ok := byID[id]; ok {
					arr(cells[3])[4] = []any{astNode("Plain", plain(p))}
				}
			}
		}
	}
}
func (s *Service) treeListing(ctx context.Context, entry IndexEntry, c Config, cat *catalog) ([]byte, error) {
	m, e := s.converter.Parse(ctx, nil)
	if e != nil {
		return nil, e
	}
	dest := listingPath(c, entry)
	blocks := []any{astNode("Header", []any{1, attr(), plain(entry.Label)})}
	items := []any{}
	for _, asset := range cat.treeAssets[entry.ID] {
		file := cat.files[asset.FileID]
		link := astNode("Link", []any{attr(), plain(file.Name), []any{relativeURI(dest, asset.Path, ""), ""}})
		items = append(items, []any{astNode("Plain", []any{link})})
	}
	blocks = append(blocks, astNode("BulletList", items))
	m["blocks"] = blocks
	return s.converter.Render(ctx, m)
}
