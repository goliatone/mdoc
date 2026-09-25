package workingdraft

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/api/docs/v1"
)

func inspect(ref SourceRef, d *docs.Document) Inspection {
	result := Inspection{Source: ref, Title: d.Title, Capabilities: Capabilities{Discussion: true}, Diagnostics: []Diagnostic{}}
	raw, _ := json.Marshal(d)
	var tree any
	_ = json.Unmarshal(raw, &tree)
	result.SuggestionsPresent = hasSuggestions(tree)
	if result.SuggestionsPresent {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{SuggestionsPending, "accept or reject pending Google suggestions before using the body"})
	}
	if len(d.Tabs) == 1 && d.Tabs[0] != nil && d.Tabs[0].TabProperties != nil {
		result.Source.TabID = d.Tabs[0].TabProperties.TabId
	}
	_, err := normalize(d)
	result.Supported = err == nil
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{UnsupportedContent, err.Error()})
	}
	result.Capabilities.Capture = result.Supported && !result.SuggestionsPresent
	return result
}
func hasSuggestions(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if strings.HasPrefix(key, "suggested") && populated(item) {
				return true
			}
			if hasSuggestions(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasSuggestions(item) {
				return true
			}
		}
	}
	return false
}
func populated(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	case nil:
		return false
	default:
		return true
	}
}

type nativeConverter struct{}

func (nativeConverter) Convert(ctx context.Context, d *docs.Document) (Content, []Diagnostic, error) {
	if ctx.Err() != nil {
		return Content{}, nil, ctx.Err()
	}
	c, e := normalize(d)
	return c, nil, e
}
func unsupported(message string) error { return fmt.Errorf("unsupported content: %s", message) }
func normalize(d *docs.Document) (Content, error) {
	if len(d.Tabs) != 1 || d.Tabs[0] == nil || d.Tabs[0].TabProperties == nil || d.Tabs[0].TabProperties.TabId == "" || d.Tabs[0].TabProperties.ParentTabId != "" || len(d.Tabs[0].ChildTabs) != 0 || d.Tabs[0].DocumentTab == nil {
		return Content{}, unsupported("exactly one root tab is required")
	}
	tab := d.Tabs[0].DocumentTab
	if tab.Body == nil || len(tab.Headers) > 0 || len(tab.Footers) > 0 || len(tab.Footnotes) > 0 || len(tab.InlineObjects) > 0 || len(tab.PositionedObjects) > 0 {
		return Content{}, unsupported("missing body, headers, footers, footnotes or embedded objects")
	}
	var blocks []string
	counts := map[string]map[int64]int64{}
	for index, e := range tab.Body.Content {
		if e == nil {
			return Content{}, unsupported("unknown body element")
		}
		if e.SectionBreak != nil && index == 0 && e.Paragraph == nil && e.Table == nil && e.TableOfContents == nil {
			continue
		}
		if e.Paragraph == nil || e.SectionBreak != nil || e.Table != nil || e.TableOfContents != nil {
			return Content{}, unsupported("tables, contents or section breaks")
		}
		p := e.Paragraph
		if len(p.PositionedObjectIds) > 0 {
			return Content{}, unsupported("positioned objects")
		}
		prefix := ""
		if p.ParagraphStyle != nil {
			switch style := p.ParagraphStyle.NamedStyleType; style {
			case "", "NORMAL_TEXT":
			case "TITLE":
				prefix = "# "
			case "SUBTITLE":
				prefix = "## "
			case "HEADING_1", "HEADING_2", "HEADING_3", "HEADING_4", "HEADING_5", "HEADING_6":
				n, _ := strconv.Atoi(strings.TrimPrefix(style, "HEADING_"))
				prefix = strings.Repeat("#", n) + " "
			default:
				return Content{}, unsupported("unrecognized paragraph style")
			}
		}
		if p.Bullet != nil {
			if prefix != "" {
				return Content{}, unsupported("heading list item")
			}
			b := p.Bullet
			list, ok := tab.Lists[b.ListId]
			if !ok || list.ListProperties == nil || b.NestingLevel < 0 || b.NestingLevel >= int64(len(list.ListProperties.NestingLevels)) {
				return Content{}, unsupported("unknown list definition")
			}
			level := list.ListProperties.NestingLevels[b.NestingLevel]
			if level == nil {
				return Content{}, unsupported("unknown list level")
			}
			if counts[b.ListId] == nil {
				counts[b.ListId] = map[int64]int64{}
			}
			marker := "- "
			if level.GlyphType == "DECIMAL" {
				if level.GlyphFormat != "" && level.GlyphFormat != fmt.Sprintf("%%%d.", b.NestingLevel) {
					return Content{}, unsupported("custom numbered list format")
				}
				n, ok := counts[b.ListId][b.NestingLevel]
				if !ok {
					n = level.StartNumber
					if n <= 0 {
						n = 1
					}
				}
				marker = strconv.FormatInt(n, 10) + ". "
				counts[b.ListId][b.NestingLevel] = n + 1
			} else if (level.GlyphType != "" && level.GlyphType != "GLYPH_TYPE_UNSPECIFIED") || !strings.Contains("●○■•◦▪", level.GlyphSymbol) || level.GlyphSymbol == "" {
				return Content{}, unsupported("custom list glyph")
			}
			for depth := range counts[b.ListId] {
				if depth > b.NestingLevel {
					delete(counts[b.ListId], depth)
				}
			}
			prefix = strings.Repeat("    ", int(b.NestingLevel)) + marker
		}
		var runs []*docs.TextRun
		for _, element := range p.Elements {
			if element == nil || element.TextRun == nil {
				return Content{}, unsupported("non-text paragraph element")
			}
			data, _ := json.Marshal(element)
			var keys map[string]any
			_ = json.Unmarshal(data, &keys)
			for key := range keys {
				if key != "startIndex" && key != "endIndex" && key != "textRun" {
					return Content{}, unsupported("unverified paragraph element")
				}
			}
			run := *element.TextRun
			if len(runs) > 0 && reflect.DeepEqual(runs[len(runs)-1].TextStyle, run.TextStyle) {
				runs[len(runs)-1].Content += run.Content
			} else {
				runs = append(runs, &run)
			}
		}
		var text strings.Builder
		for j, run := range runs {
			value := run.Content
			if j == len(runs)-1 {
				value = strings.TrimSuffix(value, "\n")
			}
			style := run.TextStyle
			if style != nil && ((style.BaselineOffset != "" && style.BaselineOffset != "NONE" && style.BaselineOffset != "BASELINE_OFFSET_UNSPECIFIED") || style.SmallCaps || style.Underline && style.Link == nil) {
				return Content{}, unsupported("superscript, subscript, small caps or underline")
			}
			link := ""
			if style != nil && style.Link != nil {
				l := style.Link
				if l.Url == "" || l.Bookmark != nil || l.BookmarkId != "" || l.Heading != nil || l.HeadingId != "" {
					return Content{}, unsupported("internal or unknown link")
				}
				u, err := url.Parse(l.Url)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
					return Content{}, unsupported("link scheme")
				}
				link = strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20", "\n", "%0A", "\r", "%0D").Replace(l.Url)
			}
			lines := strings.Split(value, "\n")
			for k, line := range lines {
				if k > 0 {
					text.WriteString("  \n")
					if p.Bullet != nil {
						text.WriteString(strings.Repeat("    ", int(p.Bullet.NestingLevel)+1))
					}
				}
				core := strings.TrimFunc(line, unicode.IsSpace)
				if core == "" {
					text.WriteString(line)
					continue
				}
				start := strings.Index(line, core)
				left, right := line[:start], line[start+len(core):]
				core = escapeMarkdown(core)
				if style != nil {
					if style.Bold {
						core = "**" + core + "**"
					}
					if style.Italic {
						core = "*" + core + "*"
					}
					if style.Strikethrough {
						core = "~~" + core + "~~"
					}
				}
				if link != "" {
					core = "[" + core + "](<" + link + ">)"
				}
				text.WriteString(left + core + right)
			}
		}
		blocks = append(blocks, prefix+text.String())
	}
	return Content{Title: d.Title, Body: strings.Join(blocks, "\n\n") + "\n"}, nil
}
func escapeMarkdown(value string) string {
	var b strings.Builder
	for _, r := range value {
		if strings.ContainsRune("\\`*_{}[]<>#+-.!|~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
