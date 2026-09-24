package reviewsync

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/goliatone/mdoc/internal/sourcekey"
)

func BuildPatch(files []FileProposal) ([]byte, error) {
	return BuildPatchContext(context.Background(), files)
}

func BuildPatchContext(ctx context.Context, files []FileProposal) ([]byte, error) {
	var output bytes.Buffer
	for _, file := range files {
		if !file.Changed || bytes.Equal(file.Current, file.Proposed) {
			continue
		}
		normalized, err := sourcekey.Normalize(file.SourceKey)
		if err != nil || normalized != file.SourceKey {
			return nil, fmt.Errorf("unsafe patch path %q", file.SourceKey)
		}
		if err := validateText(file.SourceKey, file.Current); err != nil {
			return nil, err
		}
		if err := validateText(file.SourceKey, file.Proposed); err != nil {
			return nil, err
		}
		if err := writeFilePatch(ctx, &output, file.SourceKey, file.Current, file.Proposed); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

type patchLine struct {
	prefix byte
	text   []byte
	hasLF  bool
}

func writeFilePatch(ctx context.Context, output *bytes.Buffer, sourceKey string, before, after []byte) error {
	oldPath := quotePatchPath("a/" + sourceKey)
	newPath := quotePatchPath("b/" + sourceKey)
	fmt.Fprintf(output, "--- %s\n+++ %s\n", oldPath, newPath)
	oldLines := splitPatchLines(before)
	newLines := splitPatchLines(after)
	oldStart, newStart := 1, 1
	if len(oldLines) == 0 {
		oldStart = 0
	}
	if len(newLines) == 0 {
		newStart = 0
	}
	fmt.Fprintf(output, "@@ -%d,%d +%d,%d @@\n", oldStart, len(oldLines), newStart, len(newLines))
	diff, err := lineDiff(ctx, oldLines, newLines)
	if err != nil {
		return fmt.Errorf("compare patch lines for %q: %w", sourceKey, err)
	}
	for _, line := range diff {
		output.WriteByte(line.prefix)
		output.Write(line.text)
		if !line.hasLF {
			output.WriteByte('\n')
			output.WriteString("\\ No newline at end of file\n")
		}
	}
	return nil
}

func splitPatchLines(content []byte) [][]byte {
	if len(content) == 0 {
		return nil
	}
	result := [][]byte{}
	start := 0
	for index, value := range content {
		if value == '\n' {
			result = append(result, append([]byte(nil), content[start:index+1]...))
			start = index + 1
		}
	}
	if start < len(content) {
		result = append(result, append([]byte(nil), content[start:]...))
	}
	return result
}

func lineDiff(ctx context.Context, before, after [][]byte) ([]patchLine, error) {
	beforeKeys := make([]string, len(before))
	for index := range before {
		beforeKeys[index] = string(before[index])
	}
	afterKeys := make([]string, len(after))
	for index := range after {
		afterKeys[index] = string(after[index])
	}
	matches, err := sequenceMatches(ctx, beforeKeys, afterKeys)
	if err != nil {
		return nil, err
	}
	result := make([]patchLine, 0, len(before)+len(after)-len(matches))
	left, right := 0, 0
	for _, match := range matches {
		for left < match[0] {
			result = append(result, newPatchLine('-', before[left]))
			left++
		}
		for right < match[1] {
			result = append(result, newPatchLine('+', after[right]))
			right++
		}
		result = append(result, newPatchLine(' ', before[left]))
		left++
		right++
	}
	for left < len(before) {
		result = append(result, newPatchLine('-', before[left]))
		left++
	}
	for right < len(after) {
		result = append(result, newPatchLine('+', after[right]))
		right++
	}
	return result, nil
}

func newPatchLine(prefix byte, content []byte) patchLine {
	return patchLine{prefix: prefix, text: content, hasLF: len(content) > 0 && content[len(content)-1] == '\n'}
}

func quotePatchPath(path string) string {
	quote := false
	for _, value := range []byte(path) {
		if value < 0x20 || value == 0x7f || value == '\\' || value == '"' {
			quote = true
			break
		}
	}
	if !quote {
		return path
	}
	var result strings.Builder
	result.WriteByte('"')
	for _, value := range []byte(path) {
		switch value {
		case '\n':
			result.WriteString("\\n")
		case '\r':
			result.WriteString("\\r")
		case '\t':
			result.WriteString("\\t")
		case '\\', '"':
			result.WriteByte('\\')
			result.WriteByte(value)
		default:
			if value < 0x20 || value == 0x7f {
				encoded := strconv.FormatUint(uint64(value), 8)
				result.WriteString("\\" + strings.Repeat("0", 3-len(encoded)) + encoded)
			} else {
				result.WriteByte(value)
			}
		}
	}
	result.WriteByte('"')
	return result.String()
}
