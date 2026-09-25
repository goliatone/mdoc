package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
)

const (
	MarkerCover  = "cover"
	MarkerTOC    = "table_of_contents"
	MarkerMember = "member"
)

type Marker struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	SourceKey string `json:"source_key,omitempty"`
	Start     string `json:"start,omitempty"`
	Boundary  string `json:"boundary,omitempty"`
}

type Topology struct {
	Markers      []Marker     `json:"markers"`
	Headings     []Heading    `json:"headings,omitempty"`
	TOCHeadings  []Heading    `json:"toc_headings,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
}

type Heading struct {
	SourceKey string `json:"source_key"`
	Level     int    `json:"level"`
	ID        string `json:"id"`
	Text      string `json:"text"`
}

type Result struct {
	JSON          []byte
	Document      map[string]any
	Title         string
	Topology      Topology
	ResourcePaths []string
	Issues        []document.Issue
	Assets        []Asset
	ExternalLinks []ExternalLink
}

type AssembleOptions struct {
	AllowedRoots    []string
	PathTargets     map[string]string
	ExternalTargets map[string]string
	Fields          map[string]any
	Computed        map[string]any
}

type preparedMember struct {
	member publication.Member
	blocks []any
	refs   []headingRef
}

type rewrittenMember struct {
	preparedMember
	result RewriteResult
}

func Assemble(ctx context.Context, target *publication.Publication, runner document.Runner) (Result, error) {
	return AssembleWithOptions(ctx, target, runner, AssembleOptions{})
}

func AssembleWithOptions(ctx context.Context, target *publication.Publication, runner document.Runner, options AssembleOptions) (Result, error) {
	if target == nil || target.Kind != publication.KindBundle {
		return Result{}, errors.New("bundle assembly requires a bundle publication")
	}
	if len(target.Members) == 0 {
		return Result{}, fmt.Errorf("bundle publication %q has no resolved members", target.ID)
	}
	root := map[string]any{"pandoc-api-version": []any{float64(1), float64(23), float64(1)}, "meta": map[string]any{}, "blocks": []any{}}
	blocks := []any{}
	topology := Topology{}
	resourceSet := map[string]struct{}{}
	prepared := make([]preparedMember, 0, len(target.Members))
	candidates := make([]BookmarkCandidate, 0, len(target.Members))
	issues := []document.Issue{}
	publicationTitle := target.Title
	for index, member := range target.Members {
		ast, err := runner.PandocJSON(ctx, member.Document.Path)
		if err != nil {
			return Result{}, fmt.Errorf("parse bundle %q member %q with Pandoc: %w", target.ID, member.SourceKey, err)
		}
		parsed, err := decodeDocument(ast)
		if err != nil {
			return Result{}, fmt.Errorf("decode bundle %q member %q: %w", target.ID, member.SourceKey, err)
		}
		if index == 0 {
			if version, ok := parsed["pandoc-api-version"]; ok {
				root["pandoc-api-version"] = version
			}
		}
		memberBlocks, ok := parsed["blocks"].([]any)
		if !ok {
			return Result{}, fmt.Errorf("bundle %q member %q has no Pandoc block list", target.ID, member.SourceKey)
		}
		memberBlocks, refs, title, headingErr := transformHeadings(member.SourceKey, memberBlocks, member.Config)
		if headingErr != nil {
			return Result{}, headingErr
		}
		if publicationTitle == "" && member.Config.TitleMode == "document_title" && title != "" {
			publicationTitle = title
		}
		memberBlocks, refs, appendixErr := addAppendix(member, memberBlocks, refs, title, target.Layout.Headings.MemberTitleLevel, options.Fields, options.Computed)
		if appendixErr != nil {
			return Result{}, appendixErr
		}
		if hierarchyErr := validateHeadingHierarchy(member.SourceKey, refs); hierarchyErr != nil {
			switch target.HeadingPolicy {
			case "allow":
			case "warning":
				issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "bundle_heading_jump", Source: member.SourceKey, Message: hierarchyErr.Error()})
			default:
				return Result{}, hierarchyErr
			}
		}
		prepared = append(prepared, preparedMember{member: member, blocks: memberBlocks, refs: refs})
		candidates = append(candidates, BookmarkCandidate{Logical: memberStartLogical(member.SourceKey), Label: filepath.Base(member.SourceKey)})
		for _, ref := range refs {
			candidates = append(candidates, BookmarkCandidate{Logical: ref.logical, Label: ref.text})
		}
		resourceSet[filepath.Dir(member.Document.Path)] = struct{}{}
	}
	if err := validateTOCHeadingSelectors(target.ID, target.Layout.TableOfContents, prepared); err != nil {
		return Result{}, err
	}
	referenceRules, err := prepareReferenceRules(target.ReferenceLinks, prepared)
	if err != nil {
		return Result{}, fmt.Errorf("prepare bundle %q reference links: %w", target.ID, err)
	}
	bookmarks, err := GenerateBookmarks(candidates, nil)
	if err != nil {
		return Result{}, fmt.Errorf("generate bundle %q bookmarks: %w", target.ID, err)
	}
	if target.Layout.Cover.Enabled {
		coverTarget := *target
		coverTarget.Title = publicationTitle
		coverBlocks, coverErr := buildCover(&coverTarget, options.Fields, options.Computed)
		if coverErr != nil {
			return Result{}, coverErr
		}
		blocks = append(blocks, coverBlocks...)
		marker := Marker{Kind: MarkerCover, ID: "mdoc_cover"}
		blocks = append(blocks, markerBlock(marker, nil))
		topology.Markers = append(topology.Markers, marker)
	}
	if target.Layout.TableOfContents.Enabled {
		marker := Marker{Kind: MarkerTOC, ID: "mdoc_toc"}
		blocks = append(blocks, markerBlock(marker, nil))
		topology.Markers = append(topology.Markers, marker)
	}
	allowedRoots := options.AllowedRoots
	if len(allowedRoots) == 0 {
		allowedRoots = target.AllowedRoots
	}
	memberIndex := newMemberIndex(prepared, bookmarks)
	anchoredHeadings := map[string]bool{}
	for _, item := range prepared {
		for _, ref := range item.refs {
			if tocHeadingIncluded(target.Layout.TableOfContents, item.member.SourceKey, ref) {
				anchoredHeadings[bookmarks[ref.logical]] = true
			}
		}
	}
	rewritten := make([]rewrittenMember, 0, len(prepared))
	for _, item := range prepared {
		rewriteResult, rewriteErr := rewriteMember(item.member, item.blocks, memberIndex, RewriteOptions{
			FragmentPolicy: target.FragmentPolicy, AllowedRoots: allowedRoots,
			PathTargets: options.PathTargets, ExternalTargets: options.ExternalTargets,
		})
		if rewriteErr != nil {
			return Result{}, fmt.Errorf("rewrite bundle %q member %q dependencies: %w", target.ID, item.member.SourceKey, rewriteErr)
		}
		referenceResult, referenceErr := rewriteReferences(item.blocks, item.member.SourceKey, referenceRules, bookmarks)
		if referenceErr != nil {
			return Result{}, fmt.Errorf("rewrite bundle %q member %q references: %w", target.ID, item.member.SourceKey, referenceErr)
		}
		rewriteResult.Dependencies = collectDependencies(item.blocks, item.member.SourceKey)
		rewriteResult.Issues = append(rewriteResult.Issues, referenceResult.Issues...)
		for _, dependency := range rewriteResult.Dependencies {
			if dependency.Kind == "internal" {
				anchoredHeadings[strings.TrimPrefix(dependency.Target, "#")] = true
			}
		}
		rewritten = append(rewritten, rewrittenMember{preparedMember: item, result: rewriteResult})
	}
	dependencies := []Dependency{}
	assets := []Asset{}
	externalLinks := []ExternalLink{}
	for _, item := range rewritten {
		for _, ref := range item.refs {
			bookmark := bookmarks[ref.logical]
			headingID := ""
			if anchoredHeadings[bookmark] {
				headingID = bookmark
			}
			setHeadingID(ref.node, headingID)
			heading := Heading{SourceKey: item.member.SourceKey, Level: ref.level, ID: headingID, Text: ref.text}
			topology.Headings = append(topology.Headings, heading)
			if tocHeadingIncluded(target.Layout.TableOfContents, item.member.SourceKey, ref) {
				topology.TOCHeadings = append(topology.TOCHeadings, heading)
			}
		}
		issues = append(issues, item.result.Issues...)
		dependencies = append(dependencies, item.result.Dependencies...)
		assets = append(assets, item.result.Assets...)
		externalLinks = append(externalLinks, item.result.ExternalLinks...)
		marker := Marker{Kind: MarkerMember, ID: bookmarks[memberStartLogical(item.member.SourceKey)], SourceKey: item.member.SourceKey, Start: item.member.Config.Start, Boundary: boundarySignature(item.member.Config)}
		blocks = append(blocks, markerBlock(marker, item.blocks))
		topology.Markers = append(topology.Markers, marker)
	}
	topology.Dependencies = dependencies
	root["blocks"] = blocks
	encoded, err := json.Marshal(root)
	if err != nil {
		return Result{}, fmt.Errorf("encode bundle %q Pandoc JSON: %w", target.ID, err)
	}
	resourcePaths := make([]string, 0, len(resourceSet))
	for path := range resourceSet {
		resourcePaths = append(resourcePaths, path)
	}
	sort.Strings(resourcePaths)
	return Result{JSON: encoded, Document: root, Title: publicationTitle, Topology: topology, ResourcePaths: resourcePaths, Issues: issues, Assets: assets, ExternalLinks: externalLinks}, nil
}

func ExtractTopology(data []byte) (Topology, error) {
	documentRoot, err := decodeDocument(data)
	if err != nil {
		return Topology{}, err
	}
	result := Topology{}
	extractProtected(documentRoot["blocks"], "", &result)
	return result, nil
}

func extractProtected(value any, sourceKey string, result *Topology) {
	switch current := value.(type) {
	case map[string]any:
		if current["t"] == "Header" && sourceKey != "" {
			content, _ := current["c"].([]any)
			if len(content) >= 3 {
				result.Headings = append(result.Headings, Heading{SourceKey: sourceKey, Level: integer(content[0]), ID: attributeID(content[1]), Text: inlineText(content[2])})
			}
		}
		if (current["t"] == "Link" || current["t"] == "Image") && sourceKey != "" {
			if dependency, ok := extractDependency(current, sourceKey); ok {
				result.Dependencies = append(result.Dependencies, dependency)
			}
		}
		nextSource := sourceKey
		if current["t"] == "Div" {
			content, _ := current["c"].([]any)
			if len(content) > 0 {
				id, classes, attributes := attributes(content[0])
				if contains(classes, "mdoc-protected") {
					marker := Marker{Kind: attributes["data-mdoc-kind"], ID: id, SourceKey: attributes["data-mdoc-source"], Start: attributes["data-mdoc-start"], Boundary: attributes["data-mdoc-boundary"]}
					result.Markers = append(result.Markers, marker)
					if marker.Kind == MarkerMember {
						nextSource = marker.SourceKey
					}
				}
			}
		}
		for _, child := range current {
			extractProtected(child, nextSource, result)
		}
	case []any:
		for _, child := range current {
			extractProtected(child, sourceKey, result)
		}
	}
}

func VerifyTopology(expected Topology, filtered []byte) error {
	actual, err := ExtractTopology(filtered)
	if err != nil {
		return err
	}
	if len(actual.Markers) != len(expected.Markers) {
		return fmt.Errorf("protected marker count changed from %d to %d", len(expected.Markers), len(actual.Markers))
	}
	for index := range expected.Markers {
		if actual.Markers[index] != expected.Markers[index] {
			return fmt.Errorf("protected marker %d changed from %#v to %#v", index+1, expected.Markers[index], actual.Markers[index])
		}
	}
	if len(actual.Headings) != len(expected.Headings) {
		return fmt.Errorf("protected heading count changed from %d to %d", len(expected.Headings), len(actual.Headings))
	}
	for index := range expected.Headings {
		if actual.Headings[index] != expected.Headings[index] {
			return fmt.Errorf("protected heading %d changed from %#v to %#v", index+1, expected.Headings[index], actual.Headings[index])
		}
	}
	if len(actual.Dependencies) != len(expected.Dependencies) {
		return fmt.Errorf("protected dependency count changed from %d to %d", len(expected.Dependencies), len(actual.Dependencies))
	}
	for index := range expected.Dependencies {
		if actual.Dependencies[index] != expected.Dependencies[index] {
			return fmt.Errorf("protected dependency %d changed from %#v to %#v", index+1, expected.Dependencies[index], actual.Dependencies[index])
		}
	}
	return nil
}

func markerBlock(marker Marker, blocks []any) map[string]any {
	attributes := []any{
		[]any{"data-mdoc-kind", marker.Kind},
		[]any{"data-mdoc-source", marker.SourceKey},
		[]any{"data-mdoc-start", marker.Start},
		[]any{"data-mdoc-boundary", marker.Boundary},
	}
	if blocks == nil {
		blocks = []any{}
	}
	return map[string]any{"t": "Div", "c": []any{[]any{marker.ID, []any{"mdoc-protected", "mdoc-" + marker.Kind}, attributes}, blocks}}
}

func memberStartLogical(sourceKey string) string {
	return sourceKey + "\x00start"
}

func decodeDocument(data []byte) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func walk(value any, visit func(map[string]any)) {
	switch current := value.(type) {
	case map[string]any:
		if _, exists := current["t"]; exists {
			visit(current)
		}
		for _, child := range current {
			walk(child, visit)
		}
	case []any:
		for _, child := range current {
			walk(child, visit)
		}
	}
}

func attributes(value any) (string, []string, map[string]string) {
	parts, _ := value.([]any)
	if len(parts) < 3 {
		return "", nil, map[string]string{}
	}
	id, _ := parts[0].(string)
	classesRaw, _ := parts[1].([]any)
	classes := make([]string, 0, len(classesRaw))
	for _, item := range classesRaw {
		if text, ok := item.(string); ok {
			classes = append(classes, text)
		}
	}
	result := map[string]string{}
	attributeRaw, _ := parts[2].([]any)
	for _, item := range attributeRaw {
		pair, _ := item.([]any)
		if len(pair) != 2 {
			continue
		}
		key, _ := pair[0].(string)
		value, _ := pair[1].(string)
		result[key] = value
	}
	return id, classes, result
}

func contains(values []string, target string) bool {
	return slices.Contains(values, target)
}
