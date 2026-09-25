package bundle

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/fields"
	"github.com/goliatone/mdoc/internal/publication"
)

func buildCover(target *publication.Publication, values, computed map[string]any) ([]any, error) {
	cover := target.Layout.Cover
	context := fields.TemplateContext{PublicationID: target.ID, Fields: values, Computed: computed}
	titleSource := cover.Title
	if titleSource == "" {
		titleSource = target.Title
	}
	blocks := []any{}
	for _, item := range []struct{ source, role string }{{titleSource, "Cover Title"}, {cover.Subtitle, "Cover Subtitle"}} {
		if item.source == "" {
			continue
		}
		text, err := evaluateText(item.role, item.source, context)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, styledParagraph(item.role, text))
		}
	}
	if cover.Prose != "" {
		prose, err := evaluateText("cover prose", cover.Prose, context)
		if err != nil {
			return nil, err
		}
		for paragraph := range strings.SplitSeq(prose, "\n\n") {
			if strings.TrimSpace(paragraph) != "" {
				blocks = append(blocks, styledParagraph("Cover Prose", paragraph))
			}
		}
	}
	rows := []any{}
	for index, field := range cover.Fields {
		if field.Visible != nil && !*field.Visible {
			continue
		}
		value, err := evaluateCoverField(field, context)
		if err != nil {
			return nil, fmt.Errorf("publication %q cover field %d %q: %w", target.ID, index+1, field.Key, err)
		}
		if strings.TrimSpace(value) == "" {
			if field.Optional {
				continue
			}
			return nil, fmt.Errorf("publication %q cover field %q is required but empty", target.ID, field.Key)
		}
		role := field.StyleRole
		if role == "" {
			role = "Cover Field Value"
		}
		rows = append(rows, tableRow(styledInline("Cover Field Label", field.Label), styledInline(role, value)))
	}
	if len(rows) > 0 {
		blocks = append(blocks, fieldTable(rows))
	}
	return blocks, nil
}

func evaluateCoverField(field config.CoverFieldConfig, context fields.TemplateContext) (string, error) {
	function := "field"
	if field.Optional {
		function = "optional"
	}
	expression := fmt.Sprintf(`{{ %s %q }}`, function, field.Key)
	switch {
	case field.Format == "":
	case field.Format == "date", field.Format == "number":
		if field.Optional {
			expression = fmt.Sprintf(`{{ with optional %q }}{{ %s . }}{{ end }}`, field.Key, field.Format)
		} else {
			expression = fmt.Sprintf(`{{ %s %q | %s }}`, function, field.Key, field.Format)
		}
	case strings.HasPrefix(field.Format, "date:"):
		layout := strings.TrimPrefix(field.Format, "date:")
		if field.Optional {
			expression = fmt.Sprintf(`{{ with optional %q }}{{ date %q . }}{{ end }}`, field.Key, layout)
		} else {
			expression = fmt.Sprintf(`{{ date %q (%s %q) }}`, layout, function, field.Key)
		}
	case strings.HasPrefix(field.Format, "number:"):
		decimals, err := strconv.Atoi(strings.TrimPrefix(field.Format, "number:"))
		if err != nil {
			return "", fmt.Errorf("number format requires an integer decimal count")
		}
		if field.Optional {
			expression = fmt.Sprintf(`{{ with optional %q }}{{ number %d . }}{{ end }}`, field.Key, decimals)
		} else {
			expression = fmt.Sprintf(`{{ number %d (%s %q) }}`, decimals, function, field.Key)
		}
	default:
		return "", fmt.Errorf("format %q is unsupported", field.Format)
	}
	return evaluateText("cover field "+field.Key, expression, context)
}

func evaluateText(name, source string, context fields.TemplateContext) (string, error) {
	template, err := fields.ParseTemplate(name, source)
	if err != nil {
		return "", err
	}
	segments, err := template.Evaluate(context)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, segment := range segments {
		if segment.Kind != fields.SegmentText {
			return "", fmt.Errorf("template %q emitted an unsupported page field", name)
		}
		builder.WriteString(segment.Text)
	}
	return builder.String(), nil
}

func styledParagraph(role, text string) map[string]any {
	return map[string]any{"t": "Div", "c": []any{pandocAttribute("", nil, map[string]string{"custom-style": role}), []any{map[string]any{"t": "Para", "c": textInlines(text)}}}}
}

func styledInline(role, text string) map[string]any {
	return map[string]any{"t": "Span", "c": []any{pandocAttribute("", nil, map[string]string{"custom-style": role}), textInlines(text)}}
}

func textInlines(text string) []any {
	result := []any{}
	lines := strings.Split(text, "\n")
	for lineIndex, line := range lines {
		if lineIndex > 0 {
			result = append(result, map[string]any{"t": "LineBreak"})
		}
		words := strings.Fields(line)
		for index, word := range words {
			if index > 0 {
				result = append(result, map[string]any{"t": "Space"})
			}
			result = append(result, map[string]any{"t": "Str", "c": word})
		}
	}
	return result
}

func fieldTable(rows []any) map[string]any {
	emptyAttribute := pandocAttribute("", nil, nil)
	columns := []any{[]any{map[string]any{"t": "AlignDefault"}, map[string]any{"t": "ColWidthDefault"}}, []any{map[string]any{"t": "AlignDefault"}, map[string]any{"t": "ColWidthDefault"}}}
	body := []any{emptyAttribute, float64(0), []any{}, rows}
	return map[string]any{"t": "Table", "c": []any{pandocAttribute("", nil, map[string]string{"custom-style": "Cover Field Grid"}), []any{nil, []any{}}, columns, []any{emptyAttribute, []any{}}, []any{body}, []any{emptyAttribute, []any{}}}}
}

func tableRow(label, value any) []any {
	return []any{pandocAttribute("", nil, nil), []any{tableCell(label), tableCell(value)}}
}

func tableCell(value any) []any {
	return []any{pandocAttribute("", nil, nil), map[string]any{"t": "AlignDefault"}, float64(1), float64(1), []any{map[string]any{"t": "Plain", "c": []any{value}}}}
}

func pandocAttribute(id string, classes []string, values map[string]string) []any {
	classValues := make([]any, len(classes))
	for index, class := range classes {
		classValues[index] = class
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attributes := make([]any, 0, len(keys))
	for _, key := range keys {
		attributes = append(attributes, []any{key, values[key]})
	}
	return []any{id, classValues, attributes}
}
