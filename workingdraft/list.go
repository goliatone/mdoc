package workingdraft

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/api/docs/v1"
)

type listFrame struct {
	id            string
	contentIndent int
}
type listLayout struct {
	counts map[string]map[int64]int64
	stack  []listFrame
}

func (layout *listLayout) item(b *docs.Bullet, definitions map[string]docs.List) (string, int, string, error) {
	list, ok := definitions[b.ListId]
	if !ok || b.ListId == "" || list.ListProperties == nil || b.NestingLevel < 0 || b.NestingLevel >= int64(len(list.ListProperties.NestingLevels)) || b.NestingLevel > int64(len(layout.stack)) {
		return "", 0, "", unsupported("unknown list definition or missing parent item")
	}
	level := list.ListProperties.NestingLevels[b.NestingLevel]
	if level == nil {
		return "", 0, "", unsupported("unknown list level")
	}
	if layout.counts[b.ListId] == nil {
		layout.counts[b.ListId] = map[int64]int64{}
	}
	marker := "- "
	if level.GlyphType == "DECIMAL" {
		if level.GlyphFormat != "" && level.GlyphFormat != fmt.Sprintf("%%%d.", b.NestingLevel) {
			return "", 0, "", unsupported("custom numbered list format")
		}
		n, ok := layout.counts[b.ListId][b.NestingLevel]
		if !ok {
			n = level.StartNumber
		}
		if n < 0 || n > 999999999 {
			return "", 0, "", unsupported("numbered list marker exceeds Markdown limits")
		}
		marker = strconv.FormatInt(n, 10) + ". "
		layout.counts[b.ListId][b.NestingLevel] = n + 1
	} else if (level.GlyphType != "" && level.GlyphType != "GLYPH_TYPE_UNSPECIFIED") || !ordinaryBullet(level.GlyphSymbol) {
		return "", 0, "", unsupported("custom list glyph")
	}
	for depth := range layout.counts[b.ListId] {
		if depth > b.NestingLevel {
			delete(layout.counts[b.ListId], depth)
		}
	}
	depth := int(b.NestingLevel)
	indent := 0
	if depth > 0 {
		indent = layout.stack[depth-1].contentIndent
	}
	boundary := ""
	if depth < len(layout.stack) && layout.stack[depth].id != b.ListId {
		boundary = strings.Repeat(" ", indent) + "<!-- -->"
	}
	contentIndent := indent + len(marker)
	layout.stack = append(layout.stack[:depth], listFrame{id: b.ListId, contentIndent: contentIndent})
	return strings.Repeat(" ", indent) + marker, contentIndent, boundary, nil
}
