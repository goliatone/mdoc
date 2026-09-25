package reviewsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const SourceMapVersion = 2

type ByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type SourcePosition struct {
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
}

type BlockKind string

const (
	BlockParagraph  BlockKind = "paragraph"
	BlockHeading    BlockKind = "heading"
	BlockListItem   BlockKind = "list_item"
	BlockBlockQuote BlockKind = "block_quote"
	BlockTable      BlockKind = "table"
	BlockCode       BlockKind = "code"
	BlockRaw        BlockKind = "raw"
	BlockImage      BlockKind = "image"
	BlockOther      BlockKind = "other"
)

type Block struct {
	Kind         BlockKind        `json:"kind"`
	HeadingLevel int              `json:"heading_level,omitempty"`
	ListKind     string           `json:"list_kind,omitempty"`
	MarkupHash   string           `json:"markup_hash,omitempty"`
	References   []BlockReference `json:"references,omitempty"`
	VisibleText  string           `json:"visible_text"`
	Fingerprint  string           `json:"fingerprint"`
	Signature    string           `json:"signature"`
	Range        ByteRange        `json:"range"`
	Eligible     bool             `json:"eligible"`
	Occurrence   int              `json:"occurrence"`
}

type BlockReference struct {
	Kind       string `json:"kind"`
	TargetHash string `json:"target_hash"`
	TitleHash  string `json:"title_hash,omitempty"`
}

type MemberInput struct {
	SourceKey string
	Content   []byte
}

type SourceMapEntry struct {
	BaselineIndex   int              `json:"baseline_index"`
	Member          string           `json:"member,omitempty"`
	Kind            BlockKind        `json:"kind"`
	BaselineKind    BlockKind        `json:"baseline_kind"`
	HeadingLevel    int              `json:"heading_level,omitempty"`
	ListKind        string           `json:"list_kind,omitempty"`
	MarkupHash      string           `json:"markup_hash,omitempty"`
	References      []BlockReference `json:"references,omitempty"`
	TrackReferences bool             `json:"track_references,omitempty"`
	Fingerprint     string           `json:"fingerprint"`
	Signature       string           `json:"signature"`
	VisibleText     string           `json:"visible_text,omitempty"`
	SourceRange     ByteRange        `json:"source_range"`
	Occurrence      int              `json:"occurrence,omitempty"`
	Eligible        bool             `json:"eligible"`
	Generated       bool             `json:"generated"`
}

type SourceMap struct {
	Version int              `json:"version"`
	Entries []SourceMapEntry `json:"entries"`
}

type Parser interface {
	Parse(context.Context, string, []byte, bool) ([]byte, error)
}

type PandocParser struct {
	Binary string
}

func (p PandocParser) Parse(ctx context.Context, reader string, source []byte, sourcePositions bool) ([]byte, error) {
	if !utf8.Valid(source) {
		return nil, errors.New("Markdown source is not valid UTF-8")
	}
	binary := p.Binary
	if binary == "" {
		binary = "pandoc"
	}
	reader = strings.TrimSpace(reader)
	if reader == "" {
		reader = "gfm"
	}
	if sourcePositions && !readerHasExtension(reader, "sourcepos") {
		reader += "+sourcepos"
	}
	command := exec.CommandContext(ctx, binary, "--from="+reader, "--to=json")
	command.Stdin = bytes.NewReader(source)
	output, err := command.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("pandoc source map parse failed: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("run pandoc source map parse: %w", err)
	}
	return output, nil
}

func readerHasExtension(reader, extension string) bool {
	parts := strings.FieldsFunc(reader, func(r rune) bool { return r == '+' || r == '-' })
	return slices.Contains(parts[1:], extension)
}

func ParseMember(ctx context.Context, parser Parser, reader string, member MemberInput) ([]Block, error) {
	if strings.TrimSpace(member.SourceKey) == "" {
		return nil, errors.New("member source key is required")
	}
	if !utf8.Valid(member.Content) {
		return nil, fmt.Errorf("member %q is not valid UTF-8", member.SourceKey)
	}
	ast, err := parser.Parse(ctx, reader, member.Content, true)
	if err != nil {
		return nil, fmt.Errorf("parse member %q: %w", member.SourceKey, err)
	}
	root, err := decodePandoc(ast)
	if err != nil {
		return nil, fmt.Errorf("decode member %q: %w", member.SourceKey, err)
	}
	blocks, _ := root["blocks"].([]any)
	result := make([]Block, 0, len(blocks))
	for _, value := range blocks {
		node, _ := value.(map[string]any)
		parsed, parseErr := sourceBlocks(node, member.Content)
		if parseErr != nil {
			return nil, fmt.Errorf("map member %q: %w", member.SourceKey, parseErr)
		}
		result = append(result, parsed...)
	}
	assignOccurrences(result)
	return result, nil
}

func ParseBaseline(ctx context.Context, parser Parser, reader string, content []byte) ([]Block, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("baseline export is not valid UTF-8")
	}
	ast, err := parser.Parse(ctx, reader, content, false)
	if err != nil {
		return nil, fmt.Errorf("parse baseline export: %w", err)
	}
	root, err := decodePandoc(ast)
	if err != nil {
		return nil, fmt.Errorf("decode baseline export: %w", err)
	}
	values, _ := root["blocks"].([]any)
	result := make([]Block, 0, len(values))
	for _, value := range values {
		node, _ := value.(map[string]any)
		result = append(result, semanticBlocks(node)...)
	}
	assignOccurrences(result)
	return result, nil
}

func BuildSourceMap(ctx context.Context, parser Parser, reader string, members []MemberInput, baseline []byte) (SourceMap, error) {
	if parser == nil {
		parser = PandocParser{}
	}
	if len(members) == 0 {
		return SourceMap{}, errors.New("source map requires at least one member")
	}
	source := make([]alignedMemberBlock, 0)
	seenMembers := map[string]bool{}
	for _, member := range members {
		if seenMembers[member.SourceKey] {
			return SourceMap{}, fmt.Errorf("duplicate source member %q", member.SourceKey)
		}
		seenMembers[member.SourceKey] = true
		blocks, err := ParseMember(ctx, parser, reader, member)
		if err != nil {
			return SourceMap{}, err
		}
		for _, block := range blocks {
			source = append(source, alignedMemberBlock{member: member.SourceKey, block: block})
		}
	}
	remote, err := ParseBaseline(ctx, parser, reader, baseline)
	if err != nil {
		return SourceMap{}, err
	}
	if len(remote) == 0 {
		return SourceMap{}, errors.New("baseline export has no semantic blocks")
	}
	matches, err := alignMemberBlocks(ctx, source, remote)
	if err != nil {
		return SourceMap{}, err
	}
	byBaseline := make(map[int]int, len(matches))
	for sourceIndex, baselineIndex := range matches {
		byBaseline[baselineIndex] = sourceIndex
	}
	result := SourceMap{Version: SourceMapVersion, Entries: make([]SourceMapEntry, 0, len(remote))}
	for baselineIndex, block := range remote {
		entry := SourceMapEntry{BaselineIndex: baselineIndex, Kind: block.Kind, BaselineKind: block.Kind, HeadingLevel: block.HeadingLevel, ListKind: block.ListKind, MarkupHash: block.MarkupHash, Fingerprint: block.Fingerprint, VisibleText: block.VisibleText}
		if sourceIndex, ok := byBaseline[baselineIndex]; ok {
			mapped := source[sourceIndex]
			entry.Member = mapped.member
			entry.Kind = mapped.block.Kind
			entry.SourceRange = mapped.block.Range
			entry.Occurrence = mapped.block.Occurrence
			entry.Eligible = mapped.block.Eligible
			entry.TrackReferences = len(mapped.block.References) > 0
		} else {
			entry.Generated = true
		}
		if entry.TrackReferences {
			entry.References = append([]BlockReference(nil), block.References...)
		}
		entry.Signature = block.Signature
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

type alignedMemberBlock struct {
	member string
	block  Block
}

func alignMemberBlocks(ctx context.Context, source []alignedMemberBlock, remote []Block) (map[int]int, error) {
	matches := map[int]int{}
	cursor := 0
	comparisons := 0
	for sourceIndex, current := range source {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidates := make([]int, 0)
		for index := cursor; index < len(remote); index++ {
			comparisons++
			if comparisons > 10_000_000 {
				return nil, &ComparisonComplexityError{Left: len(source), Right: len(remote)}
			}
			if compatibleFingerprint(current.block, remote[index]) {
				candidates = append(candidates, index)
			}
		}
		if len(candidates) == 0 {
			if current.block.Eligible {
				return nil, fmt.Errorf("required source block %d in member %q has no baseline match", sourceIndex, current.member)
			}
			continue
		}
		selected := candidates[0]
		if len(candidates) > 1 {
			selected = disambiguateCandidate(source, sourceIndex, remote, candidates, cursor)
			if selected < 0 && current.block.Eligible {
				return nil, fmt.Errorf("source block %d in member %q has ambiguous duplicate baseline matches", sourceIndex, current.member)
			}
			if selected < 0 {
				selected = candidates[0]
			}
		}
		matches[sourceIndex] = selected
		cursor = selected + 1
	}
	return matches, nil
}

func disambiguateCandidate(source []alignedMemberBlock, sourceIndex int, remote []Block, candidates []int, cursor int) int {
	if sourceIndex > 0 {
		previous := source[sourceIndex-1].block
		for _, candidate := range candidates {
			if candidate == cursor && candidate > 0 && compatibleFingerprint(previous, remote[candidate-1]) {
				return candidate
			}
		}
	}
	if sourceIndex+1 < len(source) {
		next := source[sourceIndex+1].block
		found := -1
		for _, candidate := range candidates {
			if candidate+1 < len(remote) && compatibleFingerprint(next, remote[candidate+1]) {
				if found >= 0 {
					return -1
				}
				found = candidate
			}
		}
		return found
	}
	return -1
}

func compatibleFingerprint(source, remote Block) bool {
	if source.Fingerprint == "" || remote.Fingerprint == "" || normalizeText(source.VisibleText) != normalizeText(remote.VisibleText) {
		return false
	}
	if source.Kind == remote.Kind {
		return true
	}
	return remote.Kind == BlockParagraph && (source.Kind == BlockBlockQuote || source.Kind == BlockCode || source.Kind == BlockHeading)
}

func decodePandoc(ast []byte) (map[string]any, error) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(ast))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	return root, nil
}

func assignOccurrences(blocks []Block) {
	counts := map[string]int{}
	for index := range blocks {
		key := string(blocks[index].Kind) + "\x00" + blocks[index].Fingerprint
		counts[key]++
		blocks[index].Occurrence = counts[key]
	}
}

func sourceBlocks(node map[string]any, source []byte) ([]Block, error) {
	if node == nil {
		return nil, nil
	}
	typeName, _ := node["t"].(string)
	switch typeName {
	case "Header", "CodeBlock", "RawBlock", "Table", "Figure", "HorizontalRule":
		kind := nodeKind(node)
		position, ok := nodePosition(node)
		if !ok {
			if kind == BlockTable {
				return []Block{{Kind: kind, VisibleText: normalizedVisible(node), Fingerprint: fingerprint(node), Eligible: false}}, nil
			}
			return nil, fmt.Errorf("%s block has no source position", typeName)
		}
		byteRange, err := PositionToByteRange(source, position)
		if err != nil {
			return nil, err
		}
		return []Block{newBlock(kind, node, byteRange)}, nil
	case "Div":
		position, wrapped := nodePosition(node)
		children := divChildren(node)
		if len(children) == 0 {
			return nil, nil
		}
		childType, _ := children[0]["t"].(string)
		switch childType {
		case "Para", "Plain":
			if !wrapped {
				return nil, fmt.Errorf("paragraph wrapper has no source position")
			}
			byteRange, err := PositionToByteRange(source, position)
			if err != nil {
				return nil, err
			}
			kind := nodeKind(children[0])
			return []Block{newBlock(kind, children[0], byteRange)}, nil
		case "BulletList", "OrderedList":
			return sourceListItems(children[0], source)
		case "BlockQuote":
			return sourceBlockQuote(children[0], source)
		default:
			if wrapped {
				byteRange, err := PositionToByteRange(source, position)
				if err != nil {
					return nil, err
				}
				if len(children) == 1 {
					return []Block{newBlock(nodeKind(children[0]), children[0], byteRange)}, nil
				}
				return []Block{newBlock(BlockOther, node, byteRange)}, nil
			}
			result := []Block{}
			for _, child := range children {
				blocks, err := sourceBlocks(child, source)
				if err != nil {
					return nil, err
				}
				result = append(result, blocks...)
			}
			return result, nil
		}
	default:
		position, ok := nodePosition(node)
		if !ok {
			return nil, nil
		}
		byteRange, err := PositionToByteRange(source, position)
		if err != nil {
			return nil, err
		}
		return []Block{newBlock(nodeKind(node), node, byteRange)}, nil
	}
}

func sourceListItems(node map[string]any, source []byte) ([]Block, error) {
	listKind := listKindForNode(node)
	content, _ := node["c"].([]any)
	items := content
	if node["t"] == "OrderedList" && len(content) > 1 {
		items, _ = content[1].([]any)
	}
	result := []Block{}
	for _, itemValue := range items {
		item, _ := itemValue.([]any)
		for _, blockValue := range item {
			wrapper, _ := blockValue.(map[string]any)
			position, ok := nodePosition(wrapper)
			children := divChildren(wrapper)
			if !ok || len(children) == 0 {
				return nil, errors.New("list item block has no source position")
			}
			byteRange, err := PositionToByteRange(source, position)
			if err != nil {
				return nil, err
			}
			byteRange.Start, err = positionOffset(source, indexLines(source), position.StartLine, 1)
			if err != nil {
				return nil, err
			}
			block := newBlock(BlockListItem, wrapper, byteRange)
			block.ListKind = listKind
			block.Signature = blockSignature(block, true)
			result = append(result, block)
		}
	}
	return result, nil
}

func sourceBlockQuote(node map[string]any, source []byte) ([]Block, error) {
	content, _ := node["c"].([]any)
	result := make([]Block, 0, len(content))
	for _, blockValue := range content {
		wrapper, _ := blockValue.(map[string]any)
		position, ok := nodePosition(wrapper)
		children := divChildren(wrapper)
		if !ok || len(children) == 0 {
			return nil, errors.New("block quote child has no source position")
		}
		byteRange, err := PositionToByteRange(source, position)
		if err != nil {
			return nil, err
		}
		byteRange.Start, err = positionOffset(source, indexLines(source), position.StartLine, 1)
		if err != nil {
			return nil, err
		}
		result = append(result, newBlock(BlockBlockQuote, wrapper, byteRange))
	}
	return result, nil
}

func semanticBlocks(node map[string]any) []Block {
	if node == nil {
		return nil
	}
	typeName, _ := node["t"].(string)
	switch typeName {
	case "Header", "Para", "Plain", "CodeBlock", "RawBlock", "Table", "Figure", "HorizontalRule":
		return []Block{newBlock(nodeKind(node), node, ByteRange{})}
	case "BulletList", "OrderedList":
		listKind := listKindForNode(node)
		content, _ := node["c"].([]any)
		items := content
		if typeName == "OrderedList" && len(content) > 1 {
			items, _ = content[1].([]any)
		}
		result := []Block{}
		for _, itemValue := range items {
			item, _ := itemValue.([]any)
			for _, blockValue := range item {
				if child, ok := blockValue.(map[string]any); ok {
					block := newBlock(BlockListItem, child, ByteRange{})
					block.ListKind = listKind
					block.Signature = blockSignature(block, true)
					result = append(result, block)
				}
			}
		}
		return result
	case "BlockQuote", "Div":
		content := make([]any, 0)
		if typeName == "BlockQuote" {
			content, _ = node["c"].([]any)
		} else {
			for _, child := range divChildren(node) {
				content = append(content, child)
			}
		}
		result := []Block{}
		for _, value := range content {
			child, _ := value.(map[string]any)
			blocks := semanticBlocks(child)
			if typeName == "BlockQuote" {
				for index := range blocks {
					blocks[index].Kind = BlockBlockQuote
					blocks[index].Fingerprint = normalizedFingerprint(BlockBlockQuote, blocks[index].VisibleText)
					blocks[index].Signature = blockSignature(blocks[index], true)
				}
			}
			result = append(result, blocks...)
		}
		return result
	default:
		if typeName == "" {
			return nil
		}
		return []Block{newBlock(BlockOther, node, ByteRange{})}
	}
}

func newBlock(kind BlockKind, node map[string]any, byteRange ByteRange) Block {
	visible := normalizedVisible(node)
	block := Block{Kind: kind, VisibleText: visible, Fingerprint: normalizedFingerprint(kind, visible), Range: byteRange, Eligible: eligibleKind(kind), References: blockReferences(node), MarkupHash: markupHash(node)}
	if kind == BlockHeading {
		content, _ := node["c"].([]any)
		if len(content) > 0 {
			switch level := content[0].(type) {
			case json.Number:
				block.HeadingLevel, _ = strconv.Atoi(level.String())
			case float64:
				block.HeadingLevel = int(level)
			}
		}
	}
	block.Signature = blockSignature(block, true)
	return block
}

func listKindForNode(node map[string]any) string {
	if node["t"] == "OrderedList" {
		return "ordered"
	}
	return "bullet"
}

func blockSignature(block Block, trackReferences bool) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00heading=%d\x00list=%s\x00markup=%s", block.Fingerprint, block.HeadingLevel, block.ListKind, block.MarkupHash)
	if trackReferences {
		for _, reference := range block.References {
			fmt.Fprintf(hash, "\x00%s:%s:%s", reference.Kind, reference.TargetHash, reference.TitleHash)
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func markupHash(node map[string]any) string {
	var structure strings.Builder
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			typeName, _ := typed["t"].(string)
			switch typeName {
			case "Emph", "Strong", "Strikeout", "Superscript", "Subscript", "SmallCaps", "Quoted", "Code":
				fmt.Fprintf(&structure, "%s:%s\x00", typeName, normalizedVisible(typed))
			}
			walk(typed["c"])
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(node)
	if structure.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(structure.String()))
	return fmt.Sprintf("%x", sum[:])
}

func blockReferences(node map[string]any) []BlockReference {
	result := []BlockReference{}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			typeName, _ := typed["t"].(string)
			if typeName == "Link" || typeName == "Image" {
				values, _ := typed["c"].([]any)
				if len(values) > 0 {
					target, _ := values[len(values)-1].([]any)
					if len(target) > 0 {
						url, _ := target[0].(string)
						title := ""
						if len(target) > 1 {
							title, _ = target[1].(string)
						}
						result = append(result, BlockReference{Kind: strings.ToLower(typeName), TargetHash: referenceHash(url), TitleHash: referenceHash(title)})
					}
					if len(values) > 1 {
						walk(values[len(values)-2])
					}
				}
				return
			}
			walk(typed["c"])
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(node)
	return result
}

func referenceHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func eligibleKind(kind BlockKind) bool {
	return kind == BlockParagraph || kind == BlockHeading || kind == BlockListItem || kind == BlockBlockQuote
}

func nodeKind(node map[string]any) BlockKind {
	typeName, _ := node["t"].(string)
	switch typeName {
	case "Header":
		return BlockHeading
	case "Para", "Plain":
		if containsNodeType(node, "Image") {
			return BlockImage
		}
		return BlockParagraph
	case "CodeBlock":
		return BlockCode
	case "RawBlock":
		return BlockRaw
	case "Table":
		return BlockTable
	case "Figure":
		return BlockImage
	default:
		return BlockOther
	}
}

func normalizedVisible(node map[string]any) string {
	return normalizeText(visibleText(node))
}

func normalizedFingerprint(kind BlockKind, visible string) string {
	// Block quote and code may be normalized to paragraphs by Google export.
	fingerprintKind := kind
	if kind == BlockBlockQuote || kind == BlockCode {
		fingerprintKind = BlockParagraph
	}
	return string(fingerprintKind) + ":" + normalizeText(visible)
}

func fingerprint(node map[string]any) string {
	return normalizedFingerprint(nodeKind(node), normalizedVisible(node))
}

func normalizeText(value string) string {
	value = norm.NFC.String(value)
	var normalized strings.Builder
	normalized.Grow(len(value))
	punctuation := `!"#$%&'()*+,-./:;<=>?@[\]^_` + "`" + `{|}~`
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) && strings.ContainsRune(punctuation, rune(value[index+1])) {
			if value[index+1] == '\\' {
				normalized.WriteByte('\\')
				index++
			}
			continue
		}
		normalized.WriteByte(value[index])
	}
	return strings.Join(strings.Fields(normalized.String()), " ")
}

func visibleText(value any) string {
	parts := []string{}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			typeName, _ := typed["t"].(string)
			switch typeName {
			case "Str":
				if text, ok := typed["c"].(string); ok {
					parts = append(parts, text)
				}
				return
			case "Space", "SoftBreak", "LineBreak":
				parts = append(parts, " ")
				return
			case "Code":
				if values, ok := typed["c"].([]any); ok && len(values) > 1 {
					if text, ok := values[1].(string); ok {
						parts = append(parts, text)
					}
				}
				return
			case "CodeBlock", "RawBlock":
				if values, ok := typed["c"].([]any); ok && len(values) > 1 {
					if text, ok := values[len(values)-1].(string); ok {
						parts = append(parts, text)
					}
				}
				return
			case "Link", "Image":
				if values, ok := typed["c"].([]any); ok && len(values) > 1 {
					walk(values[1])
				}
				return
			}
			walk(typed["c"])
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return strings.Join(parts, "")
}

func containsNodeType(value any, expected string) bool {
	found := false
	var walk func(any)
	walk = func(current any) {
		if found {
			return
		}
		switch typed := current.(type) {
		case map[string]any:
			if typed["t"] == expected {
				found = true
				return
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return found
}

func divChildren(node map[string]any) []map[string]any {
	content, _ := node["c"].([]any)
	if len(content) < 2 {
		return nil
	}
	values, _ := content[1].([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if child, ok := value.(map[string]any); ok {
			result = append(result, child)
		}
	}
	return result
}

var dataPositionPattern = regexp.MustCompile(`^([0-9]+):([0-9]+)-([0-9]+):([0-9]+)$`)

func nodePosition(node map[string]any) (SourcePosition, bool) {
	attributes := nodeAttributes(node)
	value := attributes["data-pos"]
	if value == "" || strings.Contains(value, ";") {
		return SourcePosition{}, false
	}
	match := dataPositionPattern.FindStringSubmatch(value)
	if len(match) != 5 {
		return SourcePosition{}, false
	}
	values := make([]int, 4)
	for index := range values {
		_, _ = fmt.Sscanf(match[index+1], "%d", &values[index])
	}
	return SourcePosition{StartLine: values[0], StartColumn: values[1], EndLine: values[2], EndColumn: values[3]}, true
}

func nodeAttributes(node map[string]any) map[string]string {
	content, _ := node["c"].([]any)
	var attr []any
	switch node["t"] {
	case "Header":
		if len(content) > 1 {
			attr, _ = content[1].([]any)
		}
	case "CodeBlock", "Table", "Figure", "Div":
		if len(content) > 0 {
			attr, _ = content[0].([]any)
		}
	}
	if len(attr) < 3 {
		return nil
	}
	pairs, _ := attr[2].([]any)
	result := map[string]string{}
	for _, pairValue := range pairs {
		pair, _ := pairValue.([]any)
		if len(pair) == 2 {
			key, _ := pair[0].(string)
			value, _ := pair[1].(string)
			result[key] = value
		}
	}
	return result
}

type sourceLine struct {
	start      int
	contentEnd int
	end        int
}

func PositionToByteRange(source []byte, position SourcePosition) (ByteRange, error) {
	if !utf8.Valid(source) {
		return ByteRange{}, errors.New("source is not valid UTF-8")
	}
	lines := indexLines(source)
	start, err := positionOffset(source, lines, position.StartLine, position.StartColumn)
	if err != nil {
		return ByteRange{}, fmt.Errorf("invalid start position: %w", err)
	}
	end, err := positionOffset(source, lines, position.EndLine, position.EndColumn)
	if err != nil {
		return ByteRange{}, fmt.Errorf("invalid end position: %w", err)
	}
	if end < start {
		return ByteRange{}, errors.New("source position end precedes start")
	}
	return ByteRange{Start: start, End: end}, nil
}

func indexLines(source []byte) []sourceLine {
	lines := []sourceLine{}
	start := 0
	for index := range source {
		if source[index] != '\n' {
			continue
		}
		contentEnd := index
		if contentEnd > start && source[contentEnd-1] == '\r' {
			contentEnd--
		}
		lines = append(lines, sourceLine{start: start, contentEnd: contentEnd, end: index + 1})
		start = index + 1
	}
	if start < len(source) || len(source) == 0 || (len(source) > 0 && source[len(source)-1] == '\n') {
		lines = append(lines, sourceLine{start: start, contentEnd: len(source), end: len(source)})
	}
	return lines
}

func positionOffset(source []byte, lines []sourceLine, line, column int) (int, error) {
	if line < 1 || line > len(lines) {
		return 0, fmt.Errorf("line %d is outside 1..%d", line, len(lines))
	}
	if column < 1 {
		return 0, fmt.Errorf("column %d must be positive", column)
	}
	current := lines[line-1]
	content := source[current.start:current.contentEnd]
	runeCount := utf8.RuneCount(content)
	if column > runeCount+1 {
		return 0, fmt.Errorf("column %d is outside 1..%d", column, runeCount+1)
	}
	if column == runeCount+1 {
		return current.contentEnd, nil
	}
	offset := current.start
	for currentColumn := 1; currentColumn < column; currentColumn++ {
		_, size := utf8.DecodeRune(source[offset:current.contentEnd])
		offset += size
	}
	return offset, nil
}

func InsertionNewline(source []byte, offset int) (string, error) {
	if offset < 0 || offset > len(source) {
		return "", errors.New("insertion offset is outside the source")
	}
	if !utf8.Valid(source) {
		return "", errors.New("source is not valid UTF-8")
	}
	styles := map[string]bool{}
	for index := range source {
		if source[index] != '\n' {
			continue
		}
		style := "\n"
		start := index
		if index > 0 && source[index-1] == '\r' {
			style = "\r\n"
			start--
		}
		if start == offset || index+1 == offset {
			return style, nil
		}
		styles[style] = true
	}
	if len(styles) == 1 {
		for style := range styles {
			return style, nil
		}
	}
	if len(styles) == 0 {
		return "\n", nil
	}
	ordered := make([]string, 0, len(styles))
	for style := range styles {
		ordered = append(ordered, fmt.Sprintf("%q", style))
	}
	sort.Strings(ordered)
	return "", fmt.Errorf("mixed newline source has no unambiguous insertion convention at offset %d: %s", offset, strings.Join(ordered, ", "))
}
