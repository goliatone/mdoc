package document

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func ParsePandocJSON(sourceKey, path string, source, ast []byte, maxTableColumns int) (*Document, []Issue, error) {
	return ParsePandocJSONWithPolicy(sourceKey, path, source, ast, Policy{
		Title: "required", HeadingJumps: "error", UnpublishedMarkdownLinks: "error", NamingSource: "h1",
		ImageTypes: map[string]bool{"png": true, "jpg": true, "jpeg": true, "svg": true}, MaxTableColumns: maxTableColumns,
	})
}

func ParsePandocJSONWithPolicy(sourceKey, path string, source, ast []byte, policy Policy) (*Document, []Issue, error) {
	var root map[string]any
	if err := json.Unmarshal(ast, &root); err != nil {
		return nil, nil, fmt.Errorf("decode Pandoc JSON for %s: %w", sourceKey, err)
	}
	document := &Document{SourceKey: sourceKey, Path: path, SourceHash: SourceHash(source)}
	issues := []Issue{}
	titleCount := 0
	previousHeading := 0
	walkAST(root["blocks"], func(node map[string]any) {
		typeName, _ := node["t"].(string)
		content := node["c"]
		switch typeName {
		case "Header":
			parts, _ := content.([]any)
			if len(parts) < 3 {
				return
			}
			level := intValue(parts[0])
			text := inlineText(parts[2])
			id := attributeID(parts[1])
			document.Headings = append(document.Headings, Heading{Level: level, ID: id, Text: text})
			if level == 1 {
				titleCount++
				if document.Title == "" {
					document.Title = text
				}
			}
			if previousHeading > 0 && level > previousHeading+1 && policy.HeadingJumps != "allow" {
				severity := SeverityError
				if policy.HeadingJumps == "warning" {
					severity = SeverityWarning
				}
				issues = append(issues, Issue{Severity: severity, Code: "heading_jump", Source: sourceKey, Message: fmt.Sprintf("heading level jumps from H%d to H%d", previousHeading, level)})
			}
			previousHeading = level
		case "Link", "Image":
			parts, _ := content.([]any)
			if len(parts) < 3 {
				return
			}
			target, _ := parts[2].([]any)
			if len(target) > 0 {
				value, _ := target[0].(string)
				document.Links = append(document.Links, Link{Target: value, Image: typeName == "Image"})
			}
		case "Table":
			parts, _ := content.([]any)
			if len(parts) > 2 {
				columns, _ := parts[2].([]any)
				document.TableColumns = append(document.TableColumns, len(columns))
				if policy.MaxTableColumns > 0 && len(columns) > policy.MaxTableColumns {
					issues = append(issues, Issue{Severity: SeverityWarning, Code: "wide_table", Source: sourceKey, Message: fmt.Sprintf("table has %d columns; configured maximum is %d", len(columns), policy.MaxTableColumns)})
				}
			}
		case "RawBlock", "RawInline":
			issues = append(issues, Issue{Severity: SeverityWarning, Code: "raw_node", Source: sourceKey, Message: "Pandoc found raw HTML or another unsupported raw node"})
		}
	})
	if policy.Title == "required" && titleCount != 1 {
		issues = append(issues, Issue{Severity: SeverityError, Code: "title_count", Source: sourceKey, Message: fmt.Sprintf("document must contain one H1 title; found %d", titleCount)})
	}
	title, titleErr := documentName(policy, sourceKey, path, source, document.Title)
	if titleErr != nil {
		issues = append(issues, Issue{Severity: SeverityError, Code: "document_name", Source: sourceKey, Message: titleErr.Error()})
	} else {
		document.Title = title
	}
	return document, issues, nil
}

func documentName(policy Policy, sourceKey, path string, source []byte, h1 string) (string, error) {
	switch policy.NamingSource {
	case "", "h1":
		if strings.TrimSpace(h1) == "" {
			return "", fmt.Errorf("document name requires an H1 title")
		}
		return strings.TrimSpace(h1), nil
	case "file":
		name := filepath.Base(path)
		return strings.TrimSuffix(name, filepath.Ext(name)), nil
	case "front_matter":
		title, err := frontMatterTitle(source)
		if err != nil {
			return "", err
		}
		if title == "" {
			return "", fmt.Errorf("document name requires front matter title")
		}
		return title, nil
	case "mapping":
		title := strings.TrimSpace(policy.NameMapping[sourceKey])
		if title == "" {
			return "", fmt.Errorf("document name mapping is missing for %s", sourceKey)
		}
		return title, nil
	default:
		return "", fmt.Errorf("unsupported naming source %q", policy.NamingSource)
	}
}

func frontMatterTitle(source []byte) (string, error) {
	lines := bytes.Split(source, []byte("\n"))
	if len(lines) < 3 || strings.TrimSpace(string(lines[0])) != "---" {
		return "", nil
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(string(lines[index])) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("front matter is not closed")
	}
	var values struct {
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal(bytes.Join(lines[1:end], []byte("\n")), &values); err != nil {
		return "", fmt.Errorf("decode front matter: %w", err)
	}
	return strings.TrimSpace(values.Title), nil
}

func walkAST(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["t"]; ok {
			visit(typed)
		}
		for _, child := range typed {
			walkAST(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkAST(child, visit)
		}
	}
}

func inlineText(value any) string {
	parts := []string{}
	walkAST(value, func(node map[string]any) {
		typeName, _ := node["t"].(string)
		switch typeName {
		case "Str":
			if text, ok := node["c"].(string); ok {
				parts = append(parts, text)
			}
		case "Space", "SoftBreak", "LineBreak":
			parts = append(parts, " ")
		case "Code":
			if values, ok := node["c"].([]any); ok && len(values) > 1 {
				if text, ok := values[1].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	})
	return strings.TrimSpace(strings.Join(parts, ""))
}

func attributeID(value any) string {
	parts, _ := value.([]any)
	if len(parts) == 0 {
		return ""
	}
	id, _ := parts[0].(string)
	return id
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}
