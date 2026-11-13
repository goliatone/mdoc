package contribution

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/goliatone/mdoc/workingdraft"
)

func validReplacement(s string) error {
	if len(s) > MaxText || !utf8.ValidString(s) {
		return fail(UnsupportedText, "invalid or oversized replacement")
	}
	for _, r := range s {
		if r == '\r' || r == '\n' || r == '\u2028' || r == '\u2029' || r >= 0xe000 && r <= 0xf8ff || unicode.IsControl(r) && r != '\t' {
			return fail(UnsupportedText, "replacement contains unsupported characters")
		}
	}
	return nil
}
func units(s string) int64                             { return int64(len(utf16.Encode([]rune(s)))) }
func object(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
func array(m map[string]any, k string) []any           { v, _ := m[k].([]any); return v }
func number(m map[string]any, k string) int64          { v, _ := m[k].(float64); return int64(v) }
func text(m map[string]any, k string) string           { v, _ := m[k].(string); return v }
func hasLink(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["link"]; ok {
			return true
		}
		for _, v := range x {
			if hasLink(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasLink(v) {
				return true
			}
		}
	}
	return false
}
func selectSpan(snap workingdraft.Snapshot, in UpdateInput) (UpdatePlan, error) {
	var root map[string]any
	if json.Unmarshal(snap.RawContent, &root) != nil {
		return UpdatePlan{}, fail(UnsupportedContent, "invalid native document")
	}
	tabs := array(root, "tabs")
	if len(tabs) != 1 {
		return UpdatePlan{}, fail(UnsupportedContent, "one root tab is required")
	}
	tab, _ := tabs[0].(map[string]any)
	dt := object(tab, "documentTab")
	var para map[string]any
	matches := 0
	for _, v := range array(object(dt, "body"), "content") {
		el, _ := v.(map[string]any)
		if number(el, "startIndex") == in.Selection.ParagraphStart && object(el, "paragraph") != nil {
			para = object(el, "paragraph")
			matches++
		}
	}
	if matches != 1 || in.Selection.ParagraphStart < 1 {
		return UpdatePlan{}, fail(UnsupportedContent, "paragraph identity unavailable")
	}
	type run struct {
		start, end int64
		content    string
		style      any
	}
	var runs []run
	all := ""
	position := in.Selection.ParagraphStart
	for _, v := range array(para, "elements") {
		el, _ := v.(map[string]any)
		tr := object(el, "textRun")
		c := text(tr, "content")
		a, b := number(el, "startIndex"), number(el, "endIndex")
		if tr == nil || c == "" || a != position || b != a+units(c) {
			return UpdatePlan{}, fail(UnsupportedContent, "native text indices are inconsistent")
		}
		runs = append(runs, run{a, b, c, tr["textStyle"]})
		all += c
		position = b
	}
	if !strings.HasSuffix(all, "\n") || strings.Contains(strings.TrimSuffix(all, "\n"), "\n") {
		return UpdatePlan{}, fail(UnsupportedContent, "invalid paragraph boundary")
	}
	before := strings.TrimSuffix(all, "\n")
	runes := []rune(before)
	sel := in.Selection
	if sel.Start < 0 || sel.End < sel.Start || sel.End > len(runes) {
		return UpdatePlan{}, fail(UnsupportedContent, "selection outside paragraph")
	}
	if sel.ParagraphDigest != ParagraphDigest(before) || sel.ExpectedText != string(runes[sel.Start:sel.End]) {
		return UpdatePlan{}, fail(Stale, "selected paragraph or text changed")
	}
	if in.Replacement == sel.ExpectedText {
		return UpdatePlan{}, fail(InvalidInput, "update is a no-op")
	}
	a := sel.ParagraphStart + units(string(runes[:sel.Start]))
	b := sel.ParagraphStart + units(string(runes[:sel.End]))
	var evidence RunEvidence
	found := false
	for _, r := range runs {
		end := r.end
		if strings.HasSuffix(r.content, "\n") {
			end--
		}
		if r.start < a && a <= b && b < end {
			if hasLink(r.style) || hasLink(dt["namedStyles"]) {
				return UpdatePlan{}, fail(UnsupportedContent, "linked runs cannot be changed")
			}
			evidence = RunEvidence{Start: r.start, End: end, StyleDigest: digest([]any{r.style, para["paragraphStyle"], dt["namedStyles"], dt["documentStyle"]}), SelectionPolicy: SelectionPolicy}
			found = true
			break
		}
	}
	if !found {
		return UpdatePlan{}, fail(UnsupportedContent, "selection must remain strictly inside one original native text run")
	}
	in.Source = snap.Source
	p := UpdatePlan{Kind: UpdateKind, Input: in, Before: before, After: string(runes[:sel.Start]) + in.Replacement + string(runes[sel.End:]), SnapshotDigest: digest([]any{snap.Source, snap.ProviderRevision, snap.ProviderVersion, root}), ContentDigest: snap.ContentDigest, Start: a, End: b, Run: evidence}
	p.Digest = updateDigest(p)
	return p, validateUpdate(p)
}
