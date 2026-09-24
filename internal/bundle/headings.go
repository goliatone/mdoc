package bundle

import (
	"fmt"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
)

type headingRef struct {
	node       map[string]any
	originalID string
	text       string
	level      int
	logical    string
}

func transformHeadings(sourceKey string, blocks []any, member config.MemberConfig) ([]any, []headingRef, string, error) {
	refs := collectHeadings(blocks)
	if len(refs) == 0 {
		return blocks, nil, "", nil
	}
	titleIndex := -1
	for index := range refs {
		if refs[index].level == 1 {
			titleIndex = index
			break
		}
	}
	title := ""
	delta := member.HeadingAdjustment
	removeTitle := false
	if titleIndex >= 0 {
		title = refs[titleIndex].text
		switch member.TitleMode {
		case "document_title":
			removeTitle = true
			delta--
		case "heading_1", "heading_2", "heading_3", "heading_4", "heading_5", "heading_6":
			level := int(member.TitleMode[len(member.TitleMode)-1] - '0')
			delta += level - refs[titleIndex].level
		case "remove":
			removeTitle = true
		case "", "keep":
		default:
			return nil, nil, "", fmt.Errorf("member %q title mode %q is unsupported", sourceKey, member.TitleMode)
		}
	}
	if removeTitle && titleIndex >= 0 {
		blocks = removeNode(blocks, refs[titleIndex].node)
		refs = append(refs[:titleIndex], refs[titleIndex+1:]...)
	}
	occurrences := map[string]int{}
	for index := range refs {
		level := refs[index].level + delta
		if level < 1 || level > 6 {
			return nil, nil, "", fmt.Errorf("member %q heading %q shifts from H%d to unsupported H%d", sourceKey, refs[index].text, refs[index].level, level)
		}
		setHeadingLevel(refs[index].node, level)
		refs[index].level = level
		identity := refs[index].originalID
		occurrenceKey := "id:" + identity
		if identity == "" {
			occurrenceKey = "text:" + strings.ToLower(refs[index].text)
			identity = occurrenceKey
		}
		occurrences[occurrenceKey]++
		if occurrences[occurrenceKey] > 1 || refs[index].originalID == "" {
			identity = fmt.Sprintf("%s:%d", identity, occurrences[occurrenceKey])
		}
		refs[index].logical = sourceKey + "\x00heading\x00" + identity
	}
	return blocks, refs, title, nil
}

func collectHeadings(blocks []any) []headingRef {
	result := []headingRef{}
	walk(blocks, func(node map[string]any) {
		if node["t"] != "Header" {
			return
		}
		content, _ := node["c"].([]any)
		if len(content) < 3 {
			return
		}
		result = append(result, headingRef{node: node, level: integer(content[0]), originalID: attributeID(content[1]), text: inlineText(content[2])})
	})
	return result
}

func validateHeadingHierarchy(sourceKey string, refs []headingRef) error {
	previous := 0
	for _, ref := range refs {
		if previous > 0 && ref.level > previous+1 {
			return fmt.Errorf("member %q heading hierarchy jumps from H%d to H%d at %q", sourceKey, previous, ref.level, ref.text)
		}
		previous = ref.level
	}
	return nil
}

func setHeadingLevel(node map[string]any, level int) {
	content, _ := node["c"].([]any)
	if len(content) > 0 {
		content[0] = float64(level)
	}
}

func setHeadingID(node map[string]any, id string) {
	content, _ := node["c"].([]any)
	if len(content) < 2 {
		return
	}
	attribute, _ := content[1].([]any)
	if len(attribute) < 1 {
		return
	}
	attribute[0] = id
}

func removeNode(blocks []any, target map[string]any) []any {
	for index, block := range blocks {
		if node, ok := block.(map[string]any); ok && sameMap(node, target) {
			return append(blocks[:index:index], blocks[index+1:]...)
		}
	}
	return blocks
}

func sameMap(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	return fmt.Sprintf("%p", left) == fmt.Sprintf("%p", right)
}

func integer(value any) int {
	switch current := value.(type) {
	case float64:
		return int(current)
	case int:
		return current
	default:
		return 0
	}
}

func attributeID(value any) string {
	parts, _ := value.([]any)
	if len(parts) == 0 {
		return ""
	}
	id, _ := parts[0].(string)
	return id
}

func inlineText(value any) string {
	parts := []string{}
	walk(value, func(node map[string]any) {
		switch node["t"] {
		case "Str":
			if text, ok := node["c"].(string); ok {
				parts = append(parts, text)
			}
		case "Space", "SoftBreak", "LineBreak":
			parts = append(parts, " ")
		case "Code":
			content, _ := node["c"].([]any)
			if len(content) > 1 {
				if text, ok := content[1].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	})
	return strings.TrimSpace(strings.Join(parts, ""))
}
