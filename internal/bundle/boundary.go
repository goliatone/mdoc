package bundle

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/fields"
	"github.com/goliatone/mdoc/internal/publication"
)

func addAppendix(member publication.Member, blocks []any, refs []headingRef, sourceTitle string, level int, values, computed map[string]any) ([]any, []headingRef, error) {
	if !member.Config.Appendix {
		return blocks, refs, nil
	}
	title := sourceTitle
	if member.Config.AppendixTitle != "" {
		resolved, err := evaluateText("appendix title", member.Config.AppendixTitle, fields.TemplateContext{PublicationID: member.SourceKey, Fields: values, Computed: computed})
		if err != nil {
			return nil, nil, fmt.Errorf("member %q appendix title: %w", member.SourceKey, err)
		}
		title = resolved
	}
	prefix := strings.TrimSpace(member.Config.AppendixPrefix)
	if prefix == "" {
		prefix = "Appendix"
	}
	text := strings.TrimSpace(prefix + " " + strings.TrimSpace(title))
	if level <= 0 {
		return append([]any{styledParagraph("Appendix Label", text)}, blocks...), refs, nil
	}
	node := headingNode(level, "mdoc-appendix", text)
	ref := headingRef{node: node, originalID: "mdoc-appendix", text: text, level: level, logical: member.SourceKey + "\x00appendix"}
	return append([]any{node}, blocks...), append([]headingRef{ref}, refs...), nil
}

func headingNode(level int, id, text string) map[string]any {
	return map[string]any{"t": "Header", "c": []any{level, pandocAttribute(id, nil, nil), textInlines(text)}}
}

func boundarySignature(member config.MemberConfig) string {
	data, _ := json.Marshal(struct {
		Section  config.SectionConfig `json:"section"`
		Appendix bool                 `json:"appendix"`
	}{member.Section, member.Appendix})
	return string(data)
}
