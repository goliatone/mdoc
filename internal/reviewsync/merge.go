package reviewsync

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

type SourceEdit struct {
	Range   ByteRange `json:"range"`
	NewText []byte    `json:"new_text,omitempty"`
}

type FileProposal struct {
	SourceKey string `json:"source_key"`
	Base      []byte `json:"-"`
	Current   []byte `json:"-"`
	Proposed  []byte `json:"-"`
	Changed   bool   `json:"changed"`
}

type ProposalConflict struct {
	SourceKey string `json:"source_key,omitempty"`
	Reason    string `json:"reason"`
}

type ProposalResult struct {
	Files     []FileProposal     `json:"files"`
	Conflicts []ProposalConflict `json:"conflicts,omitempty"`
}

func BuildProposals(snapshot LoadedSnapshot, classification Classification, current map[string][]byte) (ProposalResult, error) {
	if snapshot.Manifest.Status != SnapshotSealed {
		return ProposalResult{}, ErrSnapshotUnsealed
	}
	remoteEdits := map[string][]SourceEdit{}
	result := ProposalResult{}
	for _, change := range classification.Changes {
		switch change.Class {
		case ChangeUnchanged, ChangeGenerated:
			continue
		case ChangeUnsupported, ChangeConflict:
			result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: change.Member, Reason: change.Reason})
			continue
		case ChangeSafe:
			base, ok := snapshot.Sources[change.Member]
			if !ok {
				result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: change.Member, Reason: "mapped source snapshot is missing"})
				continue
			}
			edit, err := remoteSourceEdit(base, change)
			if err != nil {
				result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: change.Member, Reason: err.Error()})
				continue
			}
			remoteEdits[change.Member] = append(remoteEdits[change.Member], edit)
		default:
			return ProposalResult{}, fmt.Errorf("unknown change class %q", change.Class)
		}
	}
	for _, member := range snapshot.Manifest.Members {
		base := snapshot.Sources[member.SourceKey]
		local, ok := current[member.SourceKey]
		if !ok {
			result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: member.SourceKey, Reason: "current source is missing"})
			continue
		}
		if err := validateText(member.SourceKey, base); err != nil {
			return ProposalResult{}, err
		}
		if err := validateText(member.SourceKey, local); err != nil {
			return ProposalResult{}, err
		}
		remote := remoteEdits[member.SourceKey]
		if err := validateNonOverlapping(remote, len(base)); err != nil {
			result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: member.SourceKey, Reason: err.Error()})
			continue
		}
		combined := append([]SourceEdit(nil), remote...)
		if !bytes.Equal(base, local) {
			localEdits, err := textEdits(member.SourceKey, base, local)
			if err != nil {
				return ProposalResult{}, err
			}
			if editsOverlap(localEdits, remote) {
				result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: member.SourceKey, Reason: "local and remote changes overlap"})
				continue
			}
			combined = append(combined, localEdits...)
		}
		proposed, err := applySourceEdits(base, combined)
		if err != nil {
			result.Conflicts = append(result.Conflicts, ProposalConflict{SourceKey: member.SourceKey, Reason: err.Error()})
			continue
		}
		result.Files = append(result.Files, FileProposal{SourceKey: member.SourceKey, Base: append([]byte(nil), base...), Current: append([]byte(nil), local...), Proposed: proposed, Changed: !bytes.Equal(local, proposed)})
	}
	return result, nil
}

func remoteSourceEdit(base []byte, change ClassifiedChange) (SourceEdit, error) {
	if change.SourceRange.Start < 0 || change.SourceRange.End < change.SourceRange.Start || change.SourceRange.End > len(base) {
		return SourceEdit{}, errors.New("remote source range is outside the snapshot")
	}
	edit := SourceEdit{Range: change.SourceRange}
	switch change.Operation {
	case OperationDelete:
		edit.Range.End = extendDeletionThroughBlankLines(base, edit.Range.End)
		return edit, nil
	case OperationEdit:
		if len(change.Review) != 1 {
			return SourceEdit{}, errors.New("safe edit does not contain exactly one remote block")
		}
		original := base[edit.Range.Start:edit.Range.End]
		edit.NewText = renderReplacement(original, change.Baseline[0].Kind, change.Review[0].VisibleText)
		if change.PreserveMarkup {
			edit.NewText = restoreInlineMarkup(original, edit.NewText)
		}
	case OperationInsert:
		newline, err := InsertionNewline(base, edit.Range.Start)
		if err != nil {
			return SourceEdit{}, err
		}
		var builder strings.Builder
		for _, block := range change.Review {
			builder.WriteString(renderInsertedBlock(block))
			builder.WriteString(newline)
			builder.WriteString(newline)
		}
		edit.NewText = []byte(builder.String())
	default:
		return SourceEdit{}, fmt.Errorf("safe change has unsupported operation %q", change.Operation)
	}
	return edit, nil
}

var portableInlineMarkup = []*regexp.Regexp{
	regexp.MustCompile(`!?\[([^]\n]+)\]\([^)\n]+\)`),
	regexp.MustCompile("`([^`\\n]+)`"),
	regexp.MustCompile(`\*\*([^*\n]+)\*\*`),
	regexp.MustCompile(`__([^_\n]+)__`),
	regexp.MustCompile(`\*([^*\n]+)\*`),
}

type inlineMarkupCandidate struct {
	visible string
	markup  string
}

func restoreInlineMarkup(original, replacement []byte) []byte {
	candidates := []inlineMarkupCandidate{}
	text := string(original)
	for _, pattern := range portableInlineMarkup {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) == 2 && match[1] != "" && match[0] != match[1] {
				candidates = append(candidates, inlineMarkupCandidate{visible: match[1], markup: match[0]})
			}
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool { return len(candidates[left].visible) > len(candidates[right].visible) })
	result := string(replacement)
	for _, candidate := range candidates {
		if strings.Contains(result, candidate.markup) {
			continue
		}
		if index := strings.Index(result, candidate.visible); index >= 0 {
			result = result[:index] + candidate.markup + result[index+len(candidate.visible):]
		}
	}
	return []byte(result)
}

func extendDeletionThroughBlankLines(base []byte, offset int) int {
	for offset < len(base) {
		lineStart := offset
		for offset < len(base) && (base[offset] == ' ' || base[offset] == '\t') {
			offset++
		}
		switch {
		case offset == len(base):
			return offset
		case base[offset] == '\n':
			offset++
		case base[offset] == '\r' && offset+1 < len(base) && base[offset+1] == '\n':
			offset += 2
		default:
			return lineStart
		}
	}
	return offset
}

func renderReplacement(original []byte, kind BlockKind, visible string) []byte {
	text := string(original)
	suffixStart := len(text)
	for suffixStart > 0 && (text[suffixStart-1] == '\n' || text[suffixStart-1] == '\r') {
		suffixStart--
	}
	prefix := ""
	firstLine := text[:suffixStart]
	if lineEnd := strings.IndexAny(firstLine, "\r\n"); lineEnd >= 0 {
		firstLine = firstLine[:lineEnd]
	}
	switch kind {
	case BlockHeading:
		trimmed := strings.TrimLeft(firstLine, " \t")
		count := 0
		for count < len(trimmed) && trimmed[count] == '#' {
			count++
		}
		if count == 0 {
			count = 1
		}
		prefix = strings.Repeat("#", count) + " "
	case BlockListItem:
		prefix = structuralPrefix(firstLine, "- ")
	case BlockBlockQuote:
		prefix = structuralPrefix(firstLine, "> ")
	}
	return []byte(prefix + visible + text[suffixStart:])
}

func structuralPrefix(line, fallback string) string {
	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]
	if strings.HasPrefix(trimmed, ">") {
		marker := ">"
		if len(trimmed) > 1 && trimmed[1] == ' ' {
			marker += " "
		}
		return indent + marker
	}
	for _, marker := range []string{"- ", "+ ", "* "} {
		if strings.HasPrefix(trimmed, marker) {
			return indent + marker
		}
	}
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(trimmed) && (trimmed[digits] == '.' || trimmed[digits] == ')') && trimmed[digits+1] == ' ' {
		return indent + trimmed[:digits+2]
	}
	return indent + fallback
}

func renderInsertedBlock(block Block) string {
	switch block.Kind {
	case BlockHeading:
		level := block.HeadingLevel
		if level < 1 || level > 6 {
			level = 1
		}
		return strings.Repeat("#", level) + " " + block.VisibleText
	case BlockListItem:
		if block.ListKind == "ordered" {
			return "1. " + block.VisibleText
		}
		return "- " + block.VisibleText
	case BlockBlockQuote:
		return "> " + block.VisibleText
	default:
		return block.VisibleText
	}
}

func textEdits(sourceKey string, base, local []byte) ([]SourceEdit, error) {
	edits := myers.ComputeEdits(span.URI(sourceKey), string(base), string(local))
	result := make([]SourceEdit, 0, len(edits))
	for _, edit := range edits {
		start, err := lineOffset(base, edit.Span.Start().Line())
		if err != nil {
			return nil, err
		}
		end, err := lineOffset(base, edit.Span.End().Line())
		if err != nil {
			return nil, err
		}
		result = append(result, SourceEdit{Range: ByteRange{Start: start, End: end}, NewText: []byte(edit.NewText)})
	}
	return coalesceTextEdits(result), nil
}

func coalesceTextEdits(edits []SourceEdit) []SourceEdit {
	result := make([]SourceEdit, 0, len(edits))
	for _, edit := range edits {
		if len(result) > 0 {
			previous := &result[len(result)-1]
			if previous.Range.Start < previous.Range.End && edit.Range.Start == edit.Range.End && (edit.Range.Start == previous.Range.Start || edit.Range.Start == previous.Range.End) {
				previous.NewText = append(previous.NewText, edit.NewText...)
				continue
			}
			if previous.Range.Start == previous.Range.End && edit.Range.Start < edit.Range.End && (previous.Range.Start == edit.Range.Start || previous.Range.Start == edit.Range.End) {
				edit.NewText = append(previous.NewText, edit.NewText...)
				result[len(result)-1] = edit
				continue
			}
		}
		result = append(result, edit)
	}
	return result
}

func lineOffset(content []byte, line int) (int, error) {
	if line < 1 {
		return 0, fmt.Errorf("invalid diff line %d", line)
	}
	if line == 1 {
		return 0, nil
	}
	current := 1
	for index, value := range content {
		if value == '\n' {
			current++
			if current == line {
				return index + 1, nil
			}
		}
	}
	if current+1 == line {
		return len(content), nil
	}
	return 0, fmt.Errorf("diff line %d is outside the source", line)
}

func validateNonOverlapping(edits []SourceEdit, size int) error {
	sorted := append([]SourceEdit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Range.Start < sorted[j].Range.Start })
	for index, edit := range sorted {
		if edit.Range.Start < 0 || edit.Range.End < edit.Range.Start || edit.Range.End > size {
			return errors.New("source edit is outside the snapshot")
		}
		if index > 0 && rangesOverlap(sorted[index-1].Range, edit.Range) {
			return errors.New("source edits overlap")
		}
	}
	return nil
}

func editsOverlap(left, right []SourceEdit) bool {
	for _, first := range left {
		for _, second := range right {
			if rangesOverlap(first.Range, second.Range) {
				return true
			}
		}
	}
	return false
}

func rangesOverlap(left, right ByteRange) bool {
	if left.Start == left.End && right.Start == right.End {
		return left.Start == right.Start
	}
	if left.Start == left.End {
		return left.Start >= right.Start && left.Start <= right.End
	}
	if right.Start == right.End {
		return right.Start >= left.Start && right.Start <= left.End
	}
	return left.Start < right.End && right.Start < left.End
}

func applySourceEdits(base []byte, edits []SourceEdit) ([]byte, error) {
	if err := validateNonOverlapping(edits, len(base)); err != nil {
		return nil, err
	}
	sorted := append([]SourceEdit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Range.Start > sorted[j].Range.Start })
	result := append([]byte(nil), base...)
	for _, edit := range sorted {
		next := make([]byte, 0, len(result)-(edit.Range.End-edit.Range.Start)+len(edit.NewText))
		next = append(next, result[:edit.Range.Start]...)
		next = append(next, edit.NewText...)
		next = append(next, result[edit.Range.End:]...)
		result = next
	}
	return result, nil
}

func validateText(sourceKey string, content []byte) error {
	if !utf8.Valid(content) {
		return fmt.Errorf("source %q is not valid UTF-8", sourceKey)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return fmt.Errorf("source %q contains NUL bytes", sourceKey)
	}
	return nil
}
