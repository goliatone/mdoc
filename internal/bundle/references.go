package bundle

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
)

type referenceDefinition struct {
	Key       string
	SourceKey string
	Logical   string
	NodeKey   string
}

type preparedReferenceRule struct {
	Rule        publication.ReferenceLink
	Pattern     *regexp.Regexp
	Definitions map[string]referenceDefinition
	NodeKeys    map[string]string
}

type referenceRewriteResult struct {
	Dependencies []Dependency
	Issues       []document.Issue
}

type referenceRewriteState struct {
	rule           preparedReferenceRule
	bookmarks      map[string]string
	sourceKey      string
	skipKey        string
	skipRemaining  int
	warned         map[string]bool
	dependencies   []Dependency
	issues         []document.Issue
	rewriteFailure error
}

type referenceReplacement struct {
	start  int
	end    int
	target string
}

type inlineProjection struct {
	text     string
	eligible []bool
}

func prepareReferenceRules(rules []publication.ReferenceLink, members []preparedMember) ([]preparedReferenceRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	bySource := map[string]preparedMember{}
	for _, member := range members {
		bySource[member.member.SourceKey] = member
	}
	result := make([]preparedReferenceRule, 0, len(rules))
	for _, rule := range rules {
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("reference rule %q pattern: %w", rule.ID, err)
		}
		member, exists := bySource[rule.DefinitionSourceKey]
		if !exists {
			return nil, fmt.Errorf("reference rule %q definition source %q is not a bundle member", rule.ID, rule.DefinitionSourceKey)
		}
		prepared := preparedReferenceRule{Rule: rule, Pattern: pattern, Definitions: map[string]referenceDefinition{}, NodeKeys: map[string]string{}}
		for _, heading := range member.refs {
			text := strings.TrimSpace(heading.text)
			match := pattern.FindStringIndex(text)
			if match == nil || match[0] != 0 {
				continue
			}
			key := text[match[0]:match[1]]
			if previous, duplicate := prepared.Definitions[key]; duplicate {
				return nil, fmt.Errorf("reference rule %q key %q has duplicate definitions in %q", rule.ID, key, previous.SourceKey)
			}
			definition := referenceDefinition{Key: key, SourceKey: member.member.SourceKey, Logical: heading.logical, NodeKey: referenceNodeKey(heading.node)}
			prepared.Definitions[key] = definition
			prepared.NodeKeys[definition.NodeKey] = key
		}
		if len(prepared.Definitions) == 0 {
			return nil, fmt.Errorf("reference rule %q found no definition headings in %q", rule.ID, rule.DefinitionSourceKey)
		}
		result = append(result, prepared)
	}
	return result, nil
}

func rewriteReferences(blocks []any, sourceKey string, rules []preparedReferenceRule, bookmarks map[string]string) (referenceRewriteResult, error) {
	result := referenceRewriteResult{}
	for _, rule := range rules {
		state := &referenceRewriteState{rule: rule, bookmarks: bookmarks, sourceKey: sourceKey, warned: map[string]bool{}}
		rewriteReferenceValue(blocks, state)
		if state.rewriteFailure != nil {
			return result, state.rewriteFailure
		}
		result.Dependencies = append(result.Dependencies, state.dependencies...)
		result.Issues = append(result.Issues, state.issues...)
	}
	return result, nil
}

func rewriteReferenceValue(value any, state *referenceRewriteState) any {
	if state.rewriteFailure != nil {
		return value
	}
	switch current := value.(type) {
	case []any:
		if isInlineList(current) {
			return rewriteReferenceInlineList(current, state)
		}
		result := make([]any, 0, len(current))
		for _, item := range current {
			result = append(result, rewriteReferenceValue(item, state))
		}
		return result
	case map[string]any:
		typeName, _ := current["t"].(string)
		switch typeName {
		case "Link", "Image", "Code", "CodeBlock", "RawInline", "RawBlock":
			return current
		}
		previousSkipKey, previousSkipRemaining := state.skipKey, state.skipRemaining
		if typeName == "Header" {
			if key := state.rule.NodeKeys[referenceNodeKey(current)]; key != "" {
				state.skipKey = key
				state.skipRemaining = 1
			}
		}
		if content, exists := current["c"]; exists {
			current["c"] = rewriteReferenceValue(content, state)
		}
		state.skipKey, state.skipRemaining = previousSkipKey, previousSkipRemaining
		return current
	default:
		return value
	}
}

func rewriteReferenceInlineList(inlines []any, state *referenceRewriteState) []any {
	prepareNestedInlineContent(inlines, state)
	projection := projectInlineList(inlines)
	matches := state.rule.Pattern.FindAllStringIndex(projection.text, -1)
	if len(matches) == 0 {
		return inlines
	}
	replacements := make([]referenceReplacement, 0, len(matches))
	for _, match := range matches {
		if !inlineRangeEligible(projection.eligible, match[0], match[1]) {
			continue
		}
		key := projection.text[match[0]:match[1]]
		if state.skipRemaining > 0 && key == state.skipKey {
			state.skipRemaining--
			continue
		}
		definition, exists := state.rule.Definitions[key]
		if !exists {
			if state.rule.Rule.Unresolved == "error" {
				state.rewriteFailure = fmt.Errorf("reference rule %q key %q in %q has no definition", state.rule.Rule.ID, key, state.sourceKey)
				return inlines
			}
			if state.rule.Rule.Unresolved == "warning" {
				warningKey := state.sourceKey + "\x00" + key
				if !state.warned[warningKey] {
					state.warned[warningKey] = true
					state.issues = append(state.issues, document.Issue{Severity: document.SeverityWarning, Code: "bundle_reference_unresolved", Source: state.sourceKey, Message: fmt.Sprintf("reference rule %q key %q has no definition", state.rule.Rule.ID, key)})
				}
			}
			continue
		}
		bookmark := state.bookmarks[definition.Logical]
		if bookmark == "" {
			state.rewriteFailure = fmt.Errorf("reference rule %q key %q has no generated bookmark", state.rule.Rule.ID, key)
			return inlines
		}
		target := "#" + bookmark
		replacements = append(replacements, referenceReplacement{start: match[0], end: match[1], target: target})
		state.dependencies = append(state.dependencies, Dependency{SourceKey: state.sourceKey, Kind: "internal", Target: target})
	}
	if len(replacements) == 0 {
		return inlines
	}
	result := make([]any, 0, len(inlines)+len(replacements)*2)
	position := 0
	for _, replacement := range replacements {
		prefix, err := sliceInlineList(inlines, position, replacement.start)
		if err != nil {
			state.rewriteFailure = fmt.Errorf("reference rule %q could not preserve inline formatting: %w", state.rule.Rule.ID, err)
			return inlines
		}
		content, err := sliceInlineList(inlines, replacement.start, replacement.end)
		if err != nil {
			state.rewriteFailure = fmt.Errorf("reference rule %q could not preserve inline formatting: %w", state.rule.Rule.ID, err)
			return inlines
		}
		result = append(result, prefix...)
		link := referenceLinkWithContent(content, replacement.target)
		protectDependency(link, "internal")
		result = append(result, link)
		position = replacement.end
	}
	suffix, err := sliceInlineList(inlines, position, len(projection.text))
	if err != nil {
		state.rewriteFailure = fmt.Errorf("reference rule %q could not preserve inline formatting: %w", state.rule.Rule.ID, err)
		return inlines
	}
	result = append(result, suffix...)
	return result
}

func prepareNestedInlineContent(inlines []any, state *referenceRewriteState) {
	for _, item := range inlines {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := node["t"].(string)
		switch typeName {
		case "Link", "Image", "Code", "Math", "RawInline":
			continue
		case "Quoted", "Cite", "Note":
			if content, exists := node["c"]; exists {
				node["c"] = rewriteReferenceValue(content, state)
			}
		default:
			if children, ok := transparentInlineChildren(node); ok {
				prepareNestedInlineContent(children, state)
			}
		}
	}
}

func isInlineList(values []any) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		node, ok := value.(map[string]any)
		if !ok {
			return false
		}
		typeName, _ := node["t"].(string)
		switch typeName {
		case "Plain", "Para", "LineBlock", "CodeBlock", "RawBlock", "BlockQuote", "OrderedList", "BulletList", "DefinitionList", "Header", "HorizontalRule", "Table", "Div", "Null", "Figure":
			return false
		}
	}
	return true
}

func projectInlineList(inlines []any) inlineProjection {
	result := inlineProjection{}
	for _, item := range inlines {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		projection := projectInlineNode(node)
		result.text += projection.text
		result.eligible = append(result.eligible, projection.eligible...)
	}
	return result
}

func projectInlineNode(node map[string]any) inlineProjection {
	typeName, _ := node["t"].(string)
	switch typeName {
	case "Str":
		text, _ := node["c"].(string)
		return projectedText(text, true)
	case "Space", "SoftBreak", "LineBreak":
		return projectedText(" ", true)
	case "Code", "Math":
		content, _ := node["c"].([]any)
		if len(content) > 1 {
			text, _ := content[1].(string)
			return projectedText(text, false)
		}
	case "Link", "Image":
		content, _ := node["c"].([]any)
		if len(content) > 1 {
			projection := projectInlineList(asInlineList(content[1]))
			return projectedText(projection.text, false)
		}
	case "Quoted", "Cite":
		if children, ok := protectedInlineChildren(node); ok {
			projection := projectInlineList(children)
			return projectedText(projection.text, false)
		}
	default:
		if children, ok := transparentInlineChildren(node); ok {
			return projectInlineList(children)
		}
	}
	return inlineProjection{}
}

func projectedText(text string, eligible bool) inlineProjection {
	flags := make([]bool, len(text))
	if eligible {
		for index := range flags {
			flags[index] = true
		}
	}
	return inlineProjection{text: text, eligible: flags}
}

func inlineRangeEligible(values []bool, start, end int) bool {
	if start < 0 || end <= start || end > len(values) {
		return false
	}
	for _, eligible := range values[start:end] {
		if !eligible {
			return false
		}
	}
	return true
}

func sliceInlineList(inlines []any, start, end int) ([]any, error) {
	if start == end {
		return nil, nil
	}
	result := []any{}
	position := 0
	totalLength := len(projectInlineList(inlines).text)
	for _, item := range inlines {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		length := len(projectInlineNode(node).text)
		nodeStart, nodeEnd := position, position+length
		position = nodeEnd
		if length == 0 {
			if (start <= nodeStart && nodeStart < end) || (nodeStart == end && end == totalLength) {
				result = append(result, node)
			}
			continue
		}
		if end <= nodeStart || start >= nodeEnd {
			continue
		}
		localStart := maxInt(start-nodeStart, 0)
		localEnd := minInt(end-nodeStart, length)
		fragment, err := sliceInlineNode(node, localStart, localEnd)
		if err != nil {
			return nil, err
		}
		if fragment != nil {
			result = append(result, fragment)
		}
	}
	return result, nil
}

func sliceInlineNode(node map[string]any, start, end int) (map[string]any, error) {
	projection := projectInlineNode(node)
	if start == 0 && end == len(projection.text) {
		return node, nil
	}
	if start < 0 || end <= start || end > len(projection.text) {
		return nil, fmt.Errorf("invalid inline slice %d:%d for %q", start, end, projection.text)
	}
	typeName, _ := node["t"].(string)
	switch typeName {
	case "Str":
		return referenceString(projection.text[start:end]), nil
	case "Space", "SoftBreak", "LineBreak":
		return nil, fmt.Errorf("cannot split %s node", typeName)
	}
	children, ok := transparentInlineChildren(node)
	if !ok {
		return nil, fmt.Errorf("cannot split protected %s node", typeName)
	}
	fragments, err := sliceInlineList(children, start, end)
	if err != nil {
		return nil, err
	}
	clone := cloneInlineContainer(node, fragments)
	if typeName == "Span" && start > 0 {
		clearSpanID(clone)
	}
	return clone, nil
}

func transparentInlineChildren(node map[string]any) ([]any, bool) {
	typeName, _ := node["t"].(string)
	switch typeName {
	case "Emph", "Underline", "Strong", "Strikeout", "Superscript", "Subscript", "SmallCaps":
		return asInlineList(node["c"]), true
	case "Span":
		content, _ := node["c"].([]any)
		if len(content) > 1 {
			return asInlineList(content[1]), true
		}
	}
	if content, ok := node["c"].([]any); ok && isInlineList(content) {
		return content, true
	}
	return nil, false
}

func protectedInlineChildren(node map[string]any) ([]any, bool) {
	content, _ := node["c"].([]any)
	if len(content) < 2 {
		return nil, false
	}
	return asInlineList(content[1]), true
}

func asInlineList(value any) []any {
	values, _ := value.([]any)
	return values
}

func cloneInlineContainer(node map[string]any, children []any) map[string]any {
	clone := make(map[string]any, len(node))
	maps.Copy(clone, node)
	if node["t"] == "Span" {
		content := append([]any(nil), asInlineList(node["c"])...)
		content[1] = children
		clone["c"] = content
	} else {
		clone["c"] = children
	}
	return clone
}

func clearSpanID(node map[string]any) {
	content, _ := node["c"].([]any)
	if len(content) < 1 {
		return
	}
	attribute, _ := content[0].([]any)
	if len(attribute) < 1 {
		return
	}
	attribute = append([]any(nil), attribute...)
	attribute[0] = ""
	content[0] = attribute
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func referenceString(value string) map[string]any {
	return map[string]any{"t": "Str", "c": value}
}

func referenceLink(key, target string) map[string]any {
	return referenceLinkWithContent([]any{referenceString(key)}, target)
}

func referenceLinkWithContent(content []any, target string) map[string]any {
	return map[string]any{"t": "Link", "c": []any{[]any{"", []any{}, []any{}}, content, []any{target, ""}}}
}

func referenceNodeKey(node map[string]any) string {
	return fmt.Sprintf("%p", node)
}
