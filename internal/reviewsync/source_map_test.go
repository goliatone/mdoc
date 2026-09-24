package reviewsync

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestPositionToByteRangeUsesUnicodeCodePoints(t *testing.T) {
	source := []byte("AéZ\nnext\r\nlast")
	tests := []struct {
		name     string
		position SourcePosition
		want     string
	}{
		{name: "ascii", position: SourcePosition{1, 1, 1, 2}, want: "A"},
		{name: "unicode", position: SourcePosition{1, 2, 1, 3}, want: "é"},
		{name: "LF terminator", position: SourcePosition{1, 1, 2, 1}, want: "AéZ\n"},
		{name: "CRLF terminator", position: SourcePosition{2, 1, 3, 1}, want: "next\r\n"},
		{name: "final line", position: SourcePosition{3, 1, 3, 5}, want: "last"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := PositionToByteRange(source, test.position)
			if err != nil {
				t.Fatal(err)
			}
			if text := string(source[got.Start:got.End]); text != test.want {
				t.Fatalf("range = %q, want %q (%#v)", text, test.want, got)
			}
		})
	}
}

func TestPositionToByteRangeRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		source   []byte
		position SourcePosition
	}{
		{name: "invalid UTF-8", source: []byte{0xff}, position: SourcePosition{1, 1, 1, 1}},
		{name: "zero line", source: []byte("a"), position: SourcePosition{0, 1, 1, 1}},
		{name: "line too large", source: []byte("a"), position: SourcePosition{1, 1, 2, 1}},
		{name: "zero column", source: []byte("a"), position: SourcePosition{1, 0, 1, 1}},
		{name: "column too large", source: []byte("a"), position: SourcePosition{1, 1, 1, 3}},
		{name: "reversed", source: []byte("ab"), position: SourcePosition{1, 2, 1, 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PositionToByteRange(test.source, test.position); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestInsertionNewline(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		offset  int
		want    string
		wantErr bool
	}{
		{name: "empty defaults LF", source: "", want: "\n"},
		{name: "LF", source: "a\nb\n", offset: 1, want: "\n"},
		{name: "CRLF", source: "a\r\nb\r\n", offset: 1, want: "\r\n"},
		{name: "mixed exact LF", source: "a\nb\r\n", offset: 1, want: "\n"},
		{name: "mixed exact CRLF", source: "a\nb\r\n", offset: 3, want: "\r\n"},
		{name: "mixed ambiguous", source: "a\nb\r\n", offset: 0, wantErr: true},
		{name: "outside", source: "a", offset: 2, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := InsertionNewline([]byte(test.source), test.offset)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("newline = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeTextTreatsMarkdownEscapesAsVisiblePunctuation(t *testing.T) {
	plain := "[Vercel limit](https://example.test/docs#request body size)"
	escaped := `[Vercel limit](https://example.test/docs\#request body size)`
	if normalizeText(plain) != normalizeText(escaped) {
		t.Fatalf("Markdown escape changed visible text: %q != %q", normalizeText(plain), normalizeText(escaped))
	}
	if got := normalizeText(`path\\#literal`); got != `path\#literal` {
		t.Fatalf("escaped backslash = %q", got)
	}
}

func TestParseMemberRecordsExactRangesAndEligibility(t *testing.T) {
	requirePandoc(t)
	source := []byte("# Head\r\n\r\nPara café [label](old).\r\n\r\n- Alpha\r\n- Beta\r\n\r\n> Quote\r\n\r\n```go\r\ncode\r\n```\r\n\r\n| A | B |\r\n|---|---|\r\n| 1 | 2 |\r\n\r\n![Alt text](image.png)\r\n")
	blocks, err := ParseMember(context.Background(), PandocParser{}, "gfm", MemberInput{SourceKey: "member.md", Content: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 8 {
		t.Fatalf("block count = %d, want 8: %#v", len(blocks), blocks)
	}
	wants := []struct {
		kind     BlockKind
		text     string
		contains string
		eligible bool
	}{
		{BlockHeading, "Head", "# Head\r\n", true},
		{BlockParagraph, "Para café label.", "Para café [label](old).\r\n", true},
		{BlockListItem, "Alpha", "Alpha\r\n", true},
		{BlockListItem, "Beta", "Beta\r\n", true},
		{BlockBlockQuote, "Quote", "Quote\r\n", true},
		{BlockCode, "code", "```go\r\ncode\r\n```\r\n", false},
		{BlockTable, "AB12", "", false},
		{BlockImage, "Alt text", "![Alt text](image.png)\r\n", false},
	}
	for index, want := range wants {
		got := blocks[index]
		if got.Kind != want.kind || got.VisibleText != want.text || got.Eligible != want.eligible {
			t.Fatalf("block %d = %#v, want kind=%s text=%q eligible=%t", index, got, want.kind, want.text, want.eligible)
		}
		if text := string(source[got.Range.Start:got.Range.End]); want.contains != "" && !strings.Contains(text, want.contains) {
			t.Fatalf("block %d range = %q, want it to contain %q", index, text, want.contains)
		}
	}
}

func TestParseMemberMapsRawAndUnknownBlocksAsProtectedRanges(t *testing.T) {
	requirePandoc(t)
	source := []byte("First.\n\n---\n\n<!-- comment -->\n\n<div>raw</div>\n\nSecond.\n")
	blocks, err := ParseMember(context.Background(), PandocParser{}, "gfm", MemberInput{SourceKey: "member.md", Content: source})
	if err != nil {
		t.Fatal(err)
	}
	wants := []BlockKind{BlockParagraph, BlockOther, BlockRaw, BlockRaw, BlockParagraph}
	if len(blocks) != len(wants) {
		t.Fatalf("block count = %d, want %d: %#v", len(blocks), len(wants), blocks)
	}
	for index, want := range wants {
		if blocks[index].Kind != want || blocks[index].Range.End <= blocks[index].Range.Start {
			t.Fatalf("block %d = %#v, want protected %s range", index, blocks[index], want)
		}
		if index > 0 && blocks[index].Range.Start < blocks[index-1].Range.End {
			t.Fatalf("block ranges overlap: %#v", blocks)
		}
	}
}

func TestParseMemberRejectsMalformedUnclosedFence(t *testing.T) {
	requirePandoc(t)
	if _, err := ParseMember(context.Background(), PandocParser{}, "gfm", MemberInput{SourceKey: "member.md", Content: []byte("```go\nunterminated\n")}); err == nil {
		t.Fatal("malformed fenced block was accepted")
	}
}

func TestBuildSourceMapUsesOrderNeighborsAndVisibleLinkText(t *testing.T) {
	requirePandoc(t)
	members := []MemberInput{
		{SourceKey: "one.md", Content: []byte("# Alpha\n\nShared text.\n\n- First\n- Second\n")},
		{SourceKey: "two.md", Content: []byte("# Beta\n\nShared text with [Docs](old-target).\n\n> Quote words.\n")},
	}
	baseline := []byte("# Generated cover\n\nComputed field\n\n# Alpha\n\nShared text.\n\n- First\n- Second\n\n# Beta\n\nShared text with [Docs](new-target).\n\nQuote words.\n\nGenerated footer\n")
	got, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", members, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != SourceMapVersion || len(got.Entries) != 10 {
		t.Fatalf("source map = %#v", got)
	}
	generated := 0
	mappedByMember := map[string]int{}
	for _, entry := range got.Entries {
		if entry.Generated {
			generated++
			continue
		}
		if entry.Member == "" || entry.SourceRange.End <= entry.SourceRange.Start {
			t.Fatalf("mapped entry lacks a member range: %#v", entry)
		}
		mappedByMember[entry.Member]++
	}
	if generated != 3 || mappedByMember["one.md"] != 4 || mappedByMember["two.md"] != 3 {
		t.Fatalf("generated=%d mapped=%v", generated, mappedByMember)
	}
	last := got.Entries[8]
	if last.Member != "two.md" || last.Kind != BlockBlockQuote || !last.Eligible {
		t.Fatalf("Google-normalized block quote was not mapped: %#v", last)
	}
	linked := got.Entries[7]
	if !linked.TrackReferences || len(linked.References) != 1 || linked.Signature == "" {
		t.Fatalf("author link structure was not captured: %#v", linked)
	}
}

func TestBuildSourceMapIgnoresOnlyGeneratedLinkDestinations(t *testing.T) {
	requirePandoc(t)
	source := []byte("# Title\n\nPlain label\n")
	baseline := []byte("# Title\n\n[Plain label](https://generated.test/one)\n")
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if sourceMap.Entries[1].TrackReferences {
		t.Fatalf("generated link was marked author owned: %#v", sourceMap.Entries[1])
	}
	review, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", []byte("# Title\n\n[Plain label](https://generated.test/two)\n"))
	if err != nil {
		t.Fatal(err)
	}
	classified, err := ClassifyReview(sourceMap, review)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range classified.Changes {
		if change.Class != ChangeUnchanged {
			t.Fatalf("generated destination change = %#v", classified.Changes)
		}
	}
}

func TestBuildSourceMapKeepsRepeatedUnsupportedBlocksOwned(t *testing.T) {
	requirePandoc(t)
	source := []byte("Before.\n\n---\n\nMiddle.\n\n---\n\nAfter.\n")
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, source)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := 0
	for _, entry := range sourceMap.Entries {
		if entry.Kind == BlockOther {
			unsupported++
			if entry.Generated || entry.Member != "one.md" || entry.Eligible {
				t.Fatalf("unsupported entry lost ownership: %#v", entry)
			}
		}
	}
	if unsupported != 2 {
		t.Fatalf("unsupported count = %d, map = %#v", unsupported, sourceMap.Entries)
	}
}

func TestBuildSourceMapRejectsAmbiguousAndIncompleteMappings(t *testing.T) {
	requirePandoc(t)
	tests := []struct {
		name     string
		members  []MemberInput
		baseline string
		contains string
	}{
		{
			name:     "ambiguous duplicate",
			members:  []MemberInput{{SourceKey: "one.md", Content: []byte("Same.\n")}},
			baseline: "Same.\n\nSame.\n",
			contains: "ambiguous",
		},
		{
			name:     "missing eligible source block",
			members:  []MemberInput{{SourceKey: "one.md", Content: []byte("Required.\n")}},
			baseline: "Different.\n",
			contains: "no baseline match",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", test.members, []byte(test.baseline))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestBuildSourceMapHandlesMultiParagraphListItemsAndQuotes(t *testing.T) {
	requirePandoc(t)
	source := []byte("- First paragraph.\n\n  Second paragraph.\n\n> First quote.\n>\n> Second quote.\n")
	got, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, source)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []BlockKind{BlockListItem, BlockListItem, BlockBlockQuote, BlockBlockQuote}
	if len(got.Entries) != len(wantKinds) {
		t.Fatalf("entry count = %d, want %d: %#v", len(got.Entries), len(wantKinds), got.Entries)
	}
	for index, kind := range wantKinds {
		entry := got.Entries[index]
		if entry.Kind != kind || !entry.Eligible || entry.SourceRange.End <= entry.SourceRange.Start {
			t.Fatalf("entry %d = %#v, want eligible %s", index, entry, kind)
		}
	}
}

func TestBuildSourceMapAcceptsTitleStyleHeadingAsParagraph(t *testing.T) {
	requirePandoc(t)
	source := []byte("# Styled title\n\nBody.\n")
	baseline := []byte("Styled title\n\nBody.\n")
	got, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 || got.Entries[0].Kind != BlockHeading || got.Entries[0].Member != "one.md" {
		t.Fatalf("source map = %#v", got)
	}
}

func TestBuildSourceMapMapsThirteenOrderedMembers(t *testing.T) {
	requirePandoc(t)
	members := make([]MemberInput, 13)
	var baseline bytes.Buffer
	baseline.WriteString("# Generated cover\n\nGenerated table of contents\n\n")
	for index := range members {
		name := "docs/engagement/member-" + string(rune('a'+index)) + ".md"
		content := "# Section " + string(rune('A'+index)) + "\n\nShared body " + string(rune('A'+index)) + ".\n"
		members[index] = MemberInput{SourceKey: name, Content: []byte(content)}
		baseline.WriteString(content)
		baseline.WriteByte('\n')
	}
	got, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", members, baseline.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range got.Entries {
		if entry.Member != "" {
			seen[entry.Member] = true
		}
	}
	if len(seen) != 13 {
		t.Fatalf("mapped members = %d, want 13", len(seen))
	}
}

func requirePandoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("pandoc is not installed")
	}
}
