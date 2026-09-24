package reviewsync

import (
	"context"
	"strings"
	"testing"
)

func TestClassifyReviewEditInsertDeleteAndGenerated(t *testing.T) {
	requirePandoc(t)
	source := []byte("# Title\n\nFirst.\n\nSecond.\n\nThird.\n")
	baseline := []byte("Generated cover.\n\n# Title\n\nFirst.\n\nSecond.\n\nThird.\n\nGenerated footer.\n")
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		review string
		class  ChangeClass
		op     ChangeOperation
	}{
		{name: "edit", review: "Generated cover.\n\n# Title\n\nFirst changed.\n\nSecond.\n\nThird.\n\nGenerated footer.\n", class: ChangeSafe, op: OperationEdit},
		{name: "insert", review: "Generated cover.\n\n# Title\n\nFirst.\n\nInserted.\n\nSecond.\n\nThird.\n\nGenerated footer.\n", class: ChangeSafe, op: OperationInsert},
		{name: "delete", review: "Generated cover.\n\n# Title\n\nFirst.\n\nThird.\n\nGenerated footer.\n", class: ChangeSafe, op: OperationDelete},
		{name: "generated", review: "Changed cover.\n\n# Title\n\nFirst.\n\nSecond.\n\nThird.\n\nGenerated footer.\n", class: ChangeGenerated, op: OperationEdit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", []byte(test.review))
			if err != nil {
				t.Fatal(err)
			}
			classified, err := ClassifyReview(sourceMap, blocks)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, change := range classified.Changes {
				if change.Class == test.class && change.Operation == test.op {
					found = true
				}
			}
			if !found {
				t.Fatalf("changes = %#v, want %s %s", classified.Changes, test.class, test.op)
			}
		})
	}
}

func TestClassifyReviewUnsupportedMoveAndAmbiguousInsertion(t *testing.T) {
	requirePandoc(t)
	source := []byte("# Title\n\nOne.\n\nTwo.\n\n```\ncode\n```\n")
	baseline := source
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		review string
		class  ChangeClass
	}{
		{name: "unsupported code", review: "# Title\n\nOne.\n\nTwo.\n\n```\nchanged\n```\n", class: ChangeUnsupported},
		{name: "move", review: "# Title\n\nTwo.\n\nOne.\n\n```\ncode\n```\n", class: ChangeConflict},
		{name: "unanchored insertion", review: "Inserted.\n\n# Title\n\nOne.\n\nTwo.\n\n```\ncode\n```\n", class: ChangeConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", []byte(test.review))
			if err != nil {
				t.Fatal(err)
			}
			classified, err := ClassifyReview(sourceMap, blocks)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, change := range classified.Changes {
				if change.Class == test.class {
					found = true
				}
			}
			if !found {
				t.Fatalf("changes = %#v, want %s", classified.Changes, test.class)
			}
		})
	}
}

func TestClassifyReviewCrossMemberInsertionConflicts(t *testing.T) {
	requirePandoc(t)
	members := []MemberInput{{SourceKey: "one.md", Content: []byte("# One\n\nEnd one.\n")}, {SourceKey: "two.md", Content: []byte("# Two\n\nStart two.\n")}}
	baseline := []byte("# One\n\nEnd one.\n\n# Two\n\nStart two.\n")
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", members, baseline)
	if err != nil {
		t.Fatal(err)
	}
	review, _ := ParseBaseline(context.Background(), PandocParser{}, "gfm", []byte("# One\n\nEnd one.\n\nBetween.\n\n# Two\n\nStart two.\n"))
	classified, err := ClassifyReview(sourceMap, review)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range classified.Changes {
		if change.Operation == OperationInsert {
			if change.Class != ChangeConflict {
				t.Fatalf("cross-member insertion = %#v", change)
			}
			return
		}
	}
	t.Fatal("missing insertion classification")
}

func TestClassifyReviewRejectsEditedDuplicateWithoutUniqueContext(t *testing.T) {
	requirePandoc(t)
	source := []byte("Same.\n\nSame.\n")
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, source)
	if err != nil {
		t.Fatal(err)
	}
	review, _ := ParseBaseline(context.Background(), PandocParser{}, "gfm", []byte("Changed.\n\nSame.\n"))
	classified, err := ClassifyReview(sourceMap, review)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range classified.Changes {
		if change.Operation == OperationEdit {
			if change.Class != ChangeConflict {
				t.Fatalf("duplicate edit = %#v", change)
			}
			return
		}
	}
	t.Fatal("missing duplicate edit")
}

func TestClassifyReviewDetectsMarkdownStructuralChanges(t *testing.T) {
	requirePandoc(t)
	tests := []struct {
		name     string
		source   string
		review   string
		contains string
	}{
		{name: "heading level", source: "# Before\n\nAfter.\n", review: "## Before\n\nAfter.\n", contains: "structure changed"},
		{name: "list kind", source: "Before.\n\n1. Item\n\nAfter.\n", review: "Before.\n\n- Item\n\nAfter.\n", contains: "structure changed"},
		{name: "link destination", source: "Before.\n\n[Label](https://one.test)\n\nAfter.\n", review: "Before.\n\n[Label](https://two.test)\n\nAfter.\n", contains: "structure changed"},
		{name: "image destination", source: "Before.\n\n![Alt](one.png)\n\nAfter.\n", review: "Before.\n\n![Alt](two.png)\n\nAfter.\n", contains: "not supported"},
		{name: "inline style", source: "Before.\n\nPlain text.\n\nAfter.\n", review: "Before.\n\n*Plain text.*\n\nAfter.\n", contains: "structure changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, source)
			if err != nil {
				t.Fatal(err)
			}
			review, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", []byte(test.review))
			if err != nil {
				t.Fatal(err)
			}
			classified, err := ClassifyReview(sourceMap, review)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, change := range classified.Changes {
				if change.Class == ChangeUnsupported && strings.Contains(change.Reason, test.contains) {
					found = true
				}
			}
			if !found {
				t.Fatalf("changes = %#v, want unsupported %q", classified.Changes, test.contains)
			}
		})
	}
}

func TestClassifyPortableReviewIgnoresPresentationAndSplitsAdjacentTextEdits(t *testing.T) {
	requirePandoc(t)
	source := []byte("# Title\n\nFirst paragraph.\n\nSecond paragraph.\n\n[Label](https://one.test)\n\nEnd.\n")
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "one.md", Content: source}}, source)
	if err != nil {
		t.Fatal(err)
	}
	review := []byte("## Title\n\n*First changed.*\n\nSecond changed.\n\n[Label](https://two.test)\n\nEnd.\n")
	blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	if err != nil {
		t.Fatal(err)
	}
	classified, err := ClassifyPortableReviewContext(context.Background(), sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	safe := 0
	linkChangeApplied := false
	for _, change := range classified.Changes {
		if change.Class == ChangeSafe && change.Operation == OperationEdit {
			safe++
		}
		if change.Member == "one.md" && len(change.Baseline) == 1 && change.Baseline[0].VisibleText == "Label" && change.Class != ChangeUnchanged {
			linkChangeApplied = true
		}
	}
	if safe != 2 || linkChangeApplied {
		t.Fatalf("portable changes = %#v, want two safe text edits and a preserved local link target", classified.Changes)
	}
}
