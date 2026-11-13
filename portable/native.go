package portable

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func nonempty(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case string:
		return x != ""
	case float64:
		return x != 0
	case bool:
		return x
	}
	return true
}
func nativeSanitize(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if strings.HasPrefix(k, "suggested") && nonempty(v) {
				return fail("unsupported_content", "unresolved suggestions are unsupported")
			}
			if k == "contentUri" || k == "sourceUri" {
				delete(x, k)
				continue
			}
			if e := nativeSanitize(v); e != nil {
				return e
			}
		}
	case []any:
		for _, v := range x {
			if e := nativeSanitize(v); e != nil {
				return e
			}
		}
	}
	return nil
}
func known(m map[string]any, keys ...string) error {
	for k := range m {
		ok := false
		for _, a := range keys {
			ok = ok || a == k
		}
		if !ok {
			return fail("unsupported_content", "unsupported native field %s", k)
		}
	}
	return nil
}

type nativeBuilder struct {
	objects, lists    map[string]any
	handles, headings map[string]string
	source            SourceRef
}

func (s *Service) native(ctx context.Context, raw []byte, ref SourceRef) (map[string]any, map[string]string, map[string]string, string, string, SourceRef, error) {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil, nil, nil, "", "", ref, fail("unsupported_content", "invalid Docs JSON")
	}
	if str(root["documentId"]) != ref.FileID {
		return nil, nil, nil, "", "", ref, fail("invalid_source", "document identity mismatch")
	}
	if e := known(root, "documentId", "title", "revisionId", "suggestionsViewMode", "body", "tabs", "documentStyle", "namedStyles", "lists", "inlineObjects", "positionedObjects", "headers", "footers", "footnotes", "namedRanges", "suggestedDocumentStyleChanges", "suggestedNamedStylesChanges"); e != nil {
		return nil, nil, nil, "", "", ref, e
	}
	content := root
	tabs := arr(root["tabs"])
	if len(tabs) > 0 {
		if len(tabs) != 1 || len(arr(obj(tabs[0])["childTabs"])) != 0 {
			return nil, nil, nil, "", "", ref, fail("unsupported_content", "multiple or nested tabs are unsupported")
		}
		tab := obj(tabs[0])
		tabID := str(obj(tab["tabProperties"])["tabId"])
		if ref.TabID != "" && ref.TabID != tabID {
			return nil, nil, nil, "", "", ref, fail("invalid_source", "requested tab is not the root tab")
		}
		ref.TabID = tabID
		content = obj(tab["documentTab"])
	} else if ref.TabID != "" {
		return nil, nil, nil, "", "", ref, fail("invalid_source", "tab identity unavailable")
	}
	ref.Kind = "document"
	for _, k := range []string{"headers", "footers", "footnotes", "positionedObjects"} {
		if nonempty(content[k]) {
			return nil, nil, nil, "", "", ref, fail("unsupported_content", "native %s are unsupported", k)
		}
	}
	if content["body"] == nil {
		return nil, nil, nil, "", "", ref, fail("unsupported_content", "missing document body")
	}
	b := nativeBuilder{objects: obj(content["inlineObjects"]), lists: obj(content["lists"]), handles: map[string]string{}, headings: map[string]string{}, source: ref}
	blocks, e := b.blocks(arr(obj(content["body"])["content"]), false)
	if e != nil {
		return nil, nil, nil, "", "", ref, e
	}
	model, e := s.converter.Parse(ctx, nil)
	if e != nil {
		return nil, nil, nil, "", "", ref, e
	}
	model["blocks"] = blocks
	revision := str(root["revisionId"])
	if e = nativeSanitize(root); e != nil {
		return nil, nil, nil, "", "", ref, e
	}
	sanitized, _ := json.Marshal(root)
	return model, b.handles, b.headings, digest(sanitized), revision, ref, nil
}
func (b *nativeBuilder) blocks(elements []any, inTable bool) ([]any, error) {
	out := []any{}
	var previousList string
	var listStack []map[string]any
	for _, v := range elements {
		el := obj(v)
		if e := known(el, "startIndex", "endIndex", "paragraph", "table", "sectionBreak"); e != nil {
			return nil, e
		}
		if el["sectionBreak"] != nil {
			continue
		}
		if el["paragraph"] != nil {
			p := obj(el["paragraph"])
			if e := known(p, "elements", "paragraphStyle", "bullet", "positionedObjectIds", "suggestedBulletChanges", "suggestedParagraphStyleChanges"); e != nil {
				return nil, e
			}
			if nonempty(p["positionedObjectIds"]) {
				return nil, fail("unsupported_content", "positioned figures are unsupported")
			}
			inline, e := b.inlines(arr(p["elements"]))
			if e != nil {
				return nil, e
			}
			style := obj(p["paragraphStyle"])
			kind := str(style["namedStyleType"])
			block := astNode("Para", inline)
			if strings.HasPrefix(kind, "HEADING_") {
				level := 0
				fmt.Sscanf(kind, "HEADING_%d", &level)
				if level < 1 || level > 6 {
					return nil, fail("unsupported_content", "invalid heading level")
				}
				id := str(style["headingId"])
				block = astNode("Header", []any{level, []any{id, []any{}, []any{}}, inline})
				b.headings[id] = textOf(inline)
			} else if kind == "TITLE" {
				block = astNode("Header", []any{1, attr(), inline})
			}
			bullet := obj(p["bullet"])
			if len(bullet) > 0 {
				id := str(bullet["listId"])
				depth := num(bullet["nestingLevel"])
				if previousList != id {
					listStack = nil
				}
				if depth < 0 || depth > 8 || depth > len(listStack) {
					return nil, fail("unsupported_content", "invalid native list nesting")
				}
				levels := arr(obj(obj(b.lists[id])["listProperties"])["nestingLevels"])
				if depth >= len(levels) {
					return nil, fail("unsupported_content", "missing native list properties")
				}
				if depth < len(listStack) {
					listStack = listStack[:depth+1]
				} else {
					list, err := nativeList(obj(levels[depth]))
					if err != nil {
						return nil, err
					}
					if depth == 0 {
						out = append(out, list)
					} else {
						parent := listStack[depth-1]
						items := listItems(parent)
						if len(items) == 0 {
							return nil, fail("unsupported_content", "list has no parent item")
						}
						items[len(items)-1] = append(arr(items[len(items)-1]), list)
						setListItems(parent, items)
					}
					listStack = append(listStack, list)
				}
				list := listStack[depth]
				setListItems(list, append(listItems(list), []any{block}))
				previousList = id
				continue
			}
			previousList = ""
			listStack = nil
			out = append(out, block)
		} else if el["table"] != nil {
			previousList = ""
			listStack = nil
			if inTable {
				return nil, fail("unsupported_content", "nested tables unsupported")
			}
			t := obj(el["table"])
			cols := num(t["columns"])
			if cols < 1 || cols > 50 {
				return nil, fail("unsupported_content", "invalid table width")
			}
			rows := []any{}
			for _, rv := range arr(t["tableRows"]) {
				cells := []any{}
				for _, cv := range arr(obj(rv)["tableCells"]) {
					cell := obj(cv)
					style := obj(cell["tableCellStyle"])
					if num(style["rowSpan"]) > 1 || num(style["columnSpan"]) > 1 {
						return nil, fail("unsupported_content", "merged tables unsupported")
					}
					blocks, e := b.blocks(arr(cell["content"]), true)
					if e != nil {
						return nil, e
					}
					cells = append(cells, []any{attr(), astNode("AlignDefault", nil), 1, 1, blocks})
				}
				if len(cells) != cols {
					return nil, fail("unsupported_content", "nonrectangular table")
				}
				rows = append(rows, []any{attr(), cells})
			}
			if len(rows) == 0 {
				return nil, fail("unsupported_content", "empty native table")
			}
			spec := []any{}
			for range cols {
				spec = append(spec, []any{astNode("AlignDefault", nil), astNode("ColWidthDefault", nil)})
			}
			out = append(out, astNode("Table", []any{attr(), []any{nil, []any{}}, spec, []any{attr(), []any{rows[0]}}, []any{[]any{attr(), 0, []any{}, rows[1:]}}, []any{attr(), []any{}}}))
		} else {
			return nil, fail("unsupported_content", "unrecognized native structural element")
		}
	}
	return out, nil
}
func (b *nativeBuilder) inlines(elements []any) ([]any, error) {
	out := []any{}
	for _, v := range elements {
		el := obj(v)
		if e := known(el, "startIndex", "endIndex", "textRun", "inlineObjectElement"); e != nil {
			return nil, e
		}
		if tr := obj(el["textRun"]); tr != nil {
			txt := str(tr["content"])
			txt = strings.TrimSuffix(txt, "\n")
			runs := []any{}
			for i, line := range strings.Split(txt, "\n") {
				if i > 0 {
					runs = append(runs, astNode("LineBreak", nil))
				}
				for _, r := range line {
					if r == ' ' || r == '\t' {
						runs = append(runs, astNode("Space", nil))
					} else {
						runs = append(runs, astNode("Str", string(r)))
					}
				}
			}
			style := obj(tr["textStyle"])
			for _, pair := range []struct{ k, t string }{{"bold", "Strong"}, {"italic", "Emph"}, {"strikethrough", "Strikeout"}} {
				if style[pair.k] == true {
					runs = []any{astNode(pair.t, runs)}
				}
			}
			if link := obj(style["link"]); len(link) > 0 {
				u := str(link["url"])
				if u == "" {
					id := str(link["headingId"])
					if id == "" {
						id = str(obj(link["heading"])["id"])
					}
					if id != "" {
						u = canonical(b.source) + "#heading=" + id
					} else {
						id = str(link["bookmarkId"])
						if id == "" {
							id = str(obj(link["bookmark"])["id"])
						}
						if id == "" {
							return nil, fail("unsupported_content", "unknown native link")
						}
						u = canonical(b.source) + "#bookmark=" + id
					}
				}
				runs = []any{astNode("Link", []any{attr(), runs, []any{u, ""}})}
			}
			out = append(out, runs...)
		} else if im := obj(el["inlineObjectElement"]); im != nil {
			id := str(im["inlineObjectId"])
			o := obj(obj(obj(b.objects[id])["inlineObjectProperties"])["embeddedObject"])
			ip := obj(o["imageProperties"])
			if ip == nil || nonempty(o["embeddedDrawingProperties"]) || nonempty(o["linkedContentReference"]) {
				return nil, fail("unsupported_image", "inline object is not a plain image")
			}
			for _, k := range []string{"angle", "brightness", "contrast", "transparency"} {
				if nonempty(ip[k]) {
					return nil, fail("unsupported_image", "transformed images are unsupported")
				}
			}
			for _, v := range obj(ip["cropProperties"]) {
				if nonempty(v) {
					return nil, fail("unsupported_image", "cropped images are unsupported")
				}
			}
			u := str(ip["contentUri"])
			if u == "" {
				return nil, fail("missing_asset", "inline image has no retrieval capability")
			}
			b.handles[id] = u
			alt := str(o["description"])
			title := str(o["title"])
			out = append(out, astNode("Image", []any{attr(), plain(alt), []any{"mdoc-object:" + id, title}}))
		} else {
			return nil, fail("unsupported_content", "unsupported paragraph element")
		}
	}
	return out, nil
}

func listItems(n map[string]any) []any {
	if n["t"] == "OrderedList" {
		return arr(arr(n["c"])[1])
	}
	return arr(n["c"])
}
func setListItems(n map[string]any, items []any) {
	if n["t"] == "OrderedList" {
		arr(n["c"])[1] = items
	} else {
		n["c"] = items
	}
}
func nativeList(level map[string]any) (map[string]any, error) {
	glyph := str(level["glyphType"])
	if glyph == "" || glyph == "GLYPH_TYPE_UNSPECIFIED" {
		return astNode("BulletList", []any{}), nil
	}
	style := map[string]string{"DECIMAL": "Decimal", "ZERO_DECIMAL": "Decimal", "UPPER_ALPHA": "UpperAlpha", "ALPHA": "LowerAlpha", "UPPER_ROMAN": "UpperRoman", "ROMAN": "LowerRoman"}[glyph]
	if style == "" {
		return nil, fail("unsupported_content", "unsupported native list numbering")
	}
	start := num(level["startNumber"])
	if start < 1 {
		start = 1
	}
	return astNode("OrderedList", []any{[]any{start, astNode(style, nil), astNode("Period", nil)}, []any{}}), nil
}
