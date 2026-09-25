package workingdraft

import (
	"encoding/json"

	"google.golang.org/api/docs/v1"
)

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func array(value any) []any           { result, _ := value.([]any); return result }

func resolveTextStyles(doc *docs.Document, tree map[string]any) error {
	rawTabs := array(tree["tabs"])
	for ti, tab := range doc.Tabs {
		if tab == nil || tab.DocumentTab == nil || tab.DocumentTab.Body == nil {
			continue
		}
		if ti >= len(rawTabs) {
			return unsupported("invalid tab representation")
		}
		rawTab := object(object(rawTabs[ti])["documentTab"])
		styles := map[string]map[string]any{}
		for _, value := range array(object(rawTab["namedStyles"])["styles"]) {
			named := object(value)
			name, _ := named["namedStyleType"].(string)
			if _, exists := styles[name]; exists {
				return unsupported("duplicate named style")
			}
			styles[name] = object(named["textStyle"])
		}
		rawContent := array(object(rawTab["body"])["content"])
		for pi, element := range tab.DocumentTab.Body.Content {
			if element == nil || element.Paragraph == nil {
				continue
			}
			paragraph := element.Paragraph
			name := "NORMAL_TEXT"
			if paragraph.ParagraphStyle != nil && paragraph.ParagraphStyle.NamedStyleType != "" {
				name = paragraph.ParagraphStyle.NamedStyleType
			}
			if name != "NORMAL_TEXT" {
				if _, exists := styles[name]; !exists {
					return unsupported("missing named style definition")
				}
			}
			if pi >= len(rawContent) {
				return unsupported("invalid paragraph representation")
			}
			rawElements := array(object(object(rawContent[pi])["paragraph"])["elements"])
			for ri, element := range paragraph.Elements {
				if element == nil || element.TextRun == nil {
					continue
				}
				if ri >= len(rawElements) {
					return unsupported("invalid text representation")
				}
				run := object(object(rawElements[ri])["textRun"])
				direct := object(run["textStyle"])
				effective := map[string]any{}
				for key, value := range direct {
					effective[key] = value
				}
				for _, key := range []string{"bold", "italic", "strikethrough", "underline", "smallCaps", "baselineOffset"} {
					for _, parent := range []map[string]any{styles["NORMAL_TEXT"], styles[name], direct} {
						if value, exists := parent[key]; exists && value != nil && value != "BASELINE_OFFSET_UNSPECIFIED" {
							effective[key] = value
						}
					}
				}
				encoded, err := json.Marshal(effective)
				if err != nil {
					return unsupported("invalid text style")
				}
				var resolved docs.TextStyle
				if err := json.Unmarshal(encoded, &resolved); err != nil {
					return unsupported("invalid text style")
				}
				element.TextRun.TextStyle = &resolved
			}
		}
	}
	return nil
}
