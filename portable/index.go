package portable

import (
	"net/url"
	"regexp"
	"strings"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func ParseSource(value string) (SourceRef, error) {
	if idPattern.MatchString(value) {
		return SourceRef{FileID: value, Kind: "drive"}, nil
	}
	u, e := url.Parse(value)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return SourceRef{}, fail("invalid_index", "expected a canonical Google source link or file ID")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	id := ""
	switch u.Host {
	case "docs.google.com":
		if len(parts) >= 3 && parts[0] == "document" && parts[1] == "d" {
			id = parts[2]
		}
	case "drive.google.com":
		if len(parts) >= 3 && parts[0] == "file" && parts[1] == "d" {
			id = parts[2]
		} else if len(parts) >= 3 && parts[0] == "drive" && parts[1] == "folders" {
			id = parts[2]
		} else if u.Path == "/open" || u.Path == "/uc" {
			id = u.Query().Get("id")
		}
	}
	if !idPattern.MatchString(id) {
		return SourceRef{}, fail("invalid_index", "unsupported Google source identity")
	}
	tab := u.Query().Get("tab")
	if tab != "" && !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`).MatchString(tab) {
		return SourceRef{}, fail("invalid_index", "invalid tab selector")
	}
	return SourceRef{FileID: id, TabID: tab, Kind: "drive"}, nil
}
func canonical(ref SourceRef) string {
	if ref.Kind == "local" {
		return ""
	}
	s := "https://drive.google.com/file/d/" + ref.FileID + "/view"
	if ref.Kind == "document" || ref.TabID != "" {
		s = "https://docs.google.com/document/d/" + ref.FileID + "/edit"
	}
	if ref.TabID != "" {
		s += "?tab=" + url.QueryEscape(ref.TabID)
	}
	return s
}
func parseInventory(model map[string]any, c Config) ([]IndexEntry, error) {
	var table map[string]any
	active := false
	level := 0
	headings := 0
	for _, v := range arr(model["blocks"]) {
		n, _ := v.(map[string]any)
		parts := arr(n["c"])
		if n["t"] == "Header" && len(parts) >= 3 {
			if textOf(parts[2]) == c.Index.InventoryHeading {
				headings++
				active = true
				level = num(parts[0])
			} else if active && num(parts[0]) <= level {
				active = false
			}
		}
		if active && n["t"] == "Table" {
			if table != nil {
				return nil, fail("invalid_index", "inventory heading must contain exactly one table")
			}
			table = n
		}
	}
	if headings != 1 || table == nil {
		return nil, fail("invalid_index", "exactly one %q heading and inventory table required", c.Index.InventoryHeading)
	}
	rows, e := tableRows(table)
	if e != nil {
		return nil, e
	}
	if len(rows) < 2 {
		return nil, fail("invalid_index", "inventory is empty")
	}
	want := []string{"ID", "Document", "Section", "Path", "Role"}
	if len(rows[0]) != 5 {
		return nil, fail("invalid_index", "inventory requires ID, Document, Section, Path, Role columns")
	}
	for i, s := range want {
		if textOf(rows[0][i]) != s {
			return nil, fail("invalid_index", "invalid inventory column %d; expected %s", i+1, s)
		}
	}
	entries := []IndexEntry{}
	ids := map[string]bool{}
	sources := map[string]bool{}
	docs := 0
	for i, row := range rows[1:] {
		if len(row) != 5 {
			return nil, fail("invalid_index", "row %d must have five cells", i+1)
		}
		e := IndexEntry{ID: textOf(row[0]), Section: textOf(row[2]), Path: textOf(row[3]), Role: textOf(row[4])}
		links := []map[string]any{}
		walk(row[1], func(n map[string]any) {
			if n["t"] == "Link" {
				links = append(links, n)
			}
		})
		if len(links) != 1 {
			return nil, fail("invalid_index", "row %d requires one document hyperlink", i+1)
		}
		e.Source = target(links[0])
		e.Label = textOf(arr(links[0]["c"])[1])
		if e.Label == "" || textOf(row[1]) != e.Label {
			return nil, fail("invalid_index", "row %d document cell must contain only its hyperlink", i+1)
		}
		if !idPattern.MatchString(e.ID) || ids[fold(e.ID)] {
			return nil, fail("duplicate_identity", "invalid or duplicate inventory ID %q", e.ID)
		}
		ids[fold(e.ID)] = true
		ref, err := ParseSource(e.Source)
		identity := ref.FileID
		if err != nil {
			u, parseErr := url.Parse(e.Source)
			if e.Role != "reference" || parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
				return nil, err
			}
			u.Fragment = ""
			identity = "external:" + u.String()
		}
		if sources[identity] {
			return nil, fail("duplicate_identity", "source for %s appears more than once", e.ID)
		}
		sources[identity] = true
		switch e.Role {
		case "document":
			docs++
		case "asset", "asset-tree":
			if e.Path == "" || e.Section != "" || ref.TabID != "" {
				return nil, fail("invalid_index", "%s needs an exact Path and no Section or tab", e.ID)
			}
		case "reference":
			if e.Path != "" || e.Section != "" {
				return nil, fail("invalid_index", "reference %s cannot define an output path", e.ID)
			}
		default:
			return nil, fail("invalid_index", "unknown role for %s", e.ID)
		}
		entries = append(entries, e)
	}
	if docs == 0 || docs > c.Limits.Documents || len(entries) > c.Limits.Documents+c.Limits.Assets {
		return nil, fail("invalid_index", "inventory must include 1..%d documents and remain within item limits", c.Limits.Documents)
	}
	return entries, nil
}
func tableRows(n map[string]any) ([][]any, error) {
	c := arr(n["c"])
	if len(c) != 6 {
		return nil, fail("unsupported_content", "unsupported table representation")
	}
	var raw []any
	h := arr(c[3])
	if len(h) != 2 {
		return nil, fail("unsupported_content", "invalid table head")
	}
	raw = append(raw, arr(h[1])...)
	for _, b := range arr(c[4]) {
		body := arr(b)
		if len(body) != 4 {
			return nil, fail("unsupported_content", "invalid table body")
		}
		raw = append(raw, arr(body[2])...)
		raw = append(raw, arr(body[3])...)
	}
	f := arr(c[5])
	if len(f) != 2 {
		return nil, fail("unsupported_content", "invalid table foot")
	}
	raw = append(raw, arr(f[1])...)
	rows := [][]any{}
	for _, v := range raw {
		r := arr(v)
		if len(r) != 2 {
			return nil, fail("unsupported_content", "invalid row")
		}
		cells := []any{}
		for _, v := range arr(r[1]) {
			cell := arr(v)
			if len(cell) != 5 || num(cell[2]) != 1 || num(cell[3]) != 1 {
				return nil, fail("unsupported_content", "merged tables are unsupported")
			}
			cells = append(cells, cell[4])
		}
		rows = append(rows, cells)
	}
	return rows, nil
}
