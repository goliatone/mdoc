package reviewsync

import (
	"context"
	"errors"
	"fmt"
)

type ChangeClass string

const (
	ChangeUnchanged   ChangeClass = "unchanged"
	ChangeSafe        ChangeClass = "safe"
	ChangeGenerated   ChangeClass = "generated"
	ChangeConflict    ChangeClass = "conflict"
	ChangeUnsupported ChangeClass = "unsupported"
)

type ChangeOperation string

const (
	OperationNone   ChangeOperation = "none"
	OperationEdit   ChangeOperation = "edit"
	OperationInsert ChangeOperation = "insert"
	OperationDelete ChangeOperation = "delete"
)

type ClassifiedChange struct {
	Index          int              `json:"index"`
	Class          ChangeClass      `json:"class"`
	Operation      ChangeOperation  `json:"operation"`
	Member         string           `json:"member,omitempty"`
	SourceRange    ByteRange        `json:"source_range,omitempty"`
	BaselineStart  int              `json:"baseline_start"`
	BaselineEnd    int              `json:"baseline_end"`
	ReviewStart    int              `json:"review_start"`
	ReviewEnd      int              `json:"review_end"`
	Baseline       []SourceMapEntry `json:"baseline,omitempty"`
	Review         []Block          `json:"review,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	PreserveMarkup bool             `json:"preserve_markup,omitempty"`
}

type Classification struct {
	Changes []ClassifiedChange `json:"changes"`
}

func ClassifyReview(sourceMap SourceMap, review []Block) (Classification, error) {
	return ClassifyReviewContext(context.Background(), sourceMap, review)
}

func ClassifyReviewContext(ctx context.Context, sourceMap SourceMap, review []Block) (Classification, error) {
	return classifyReviewContext(ctx, sourceMap, review, false)
}

// ClassifyPortableReviewContext aligns an exported copy by visible block content.
// It is intended for Google Docs that were created outside mdoc, where import and
// export can change Markdown presentation without changing the visible document.
func ClassifyPortableReviewContext(ctx context.Context, sourceMap SourceMap, review []Block) (Classification, error) {
	return classifyReviewContext(ctx, sourceMap, review, true)
}

func classifyReviewContext(ctx context.Context, sourceMap SourceMap, review []Block, portable bool) (Classification, error) {
	if sourceMap.Version != SourceMapVersion || len(sourceMap.Entries) == 0 {
		return Classification{}, errors.New("a valid baseline source map is required")
	}
	for index, entry := range sourceMap.Entries {
		if entry.BaselineIndex != index || entry.Fingerprint == "" || entry.Signature == "" {
			return Classification{}, fmt.Errorf("baseline source map entry %d is invalid", index)
		}
	}
	baselineKeys := make([]string, len(sourceMap.Entries))
	for index := range sourceMap.Entries {
		baselineKeys[index] = sourceMap.Entries[index].Signature
		if portable {
			baselineKeys[index] = "visible:" + sourceMap.Entries[index].VisibleText
		}
	}
	reviewKeys := make([]string, len(review))
	for index := range review {
		if review[index].Signature == "" {
			return Classification{}, fmt.Errorf("review block %d is invalid", index)
		}
		reviewKeys[index] = review[index].Signature
		if portable {
			reviewKeys[index] = "visible:" + review[index].VisibleText
		}
	}
	matches, err := sequenceMatches(ctx, baselineKeys, reviewKeys)
	if err != nil {
		return Classification{}, fmt.Errorf("align review blocks: %w", err)
	}
	type point struct{ baseline, review int }
	points := make([]point, 0, len(matches)+2)
	points = append(points, point{-1, -1})
	for _, match := range matches {
		points = append(points, point{match[0], match[1]})
	}
	points = append(points, point{len(sourceMap.Entries), len(review)})
	result := Classification{}
	for index := 0; index+1 < len(points); index++ {
		left, right := points[index], points[index+1]
		baselineStart, baselineEnd := left.baseline+1, right.baseline
		reviewStart, reviewEnd := left.review+1, right.review
		if baselineStart < baselineEnd || reviewStart < reviewEnd {
			if portable && baselineEnd-baselineStart == reviewEnd-reviewStart && baselineEnd-baselineStart > 1 {
				for offset := 0; offset < baselineEnd-baselineStart; offset++ {
					change := classifyPortableGap(sourceMap.Entries, review, baselineStart+offset, baselineStart+offset+1, reviewStart+offset, reviewStart+offset+1, baselineStart+offset-1, baselineStart+offset+1)
					change.Index = len(result.Changes)
					result.Changes = append(result.Changes, change)
				}
			} else {
				classify := classifyGap
				if portable {
					classify = classifyPortableGap
				}
				change := classify(sourceMap.Entries, review, baselineStart, baselineEnd, reviewStart, reviewEnd, left.baseline, right.baseline)
				change.Index = len(result.Changes)
				result.Changes = append(result.Changes, change)
			}
		}
		if right.baseline < len(sourceMap.Entries) {
			entry := sourceMap.Entries[right.baseline]
			result.Changes = append(result.Changes, ClassifiedChange{
				Index: len(result.Changes), Class: ChangeUnchanged, Operation: OperationNone, Member: entry.Member, SourceRange: entry.SourceRange,
				BaselineStart: right.baseline, BaselineEnd: right.baseline + 1, ReviewStart: right.review, ReviewEnd: right.review + 1,
				Baseline: []SourceMapEntry{entry}, Review: []Block{review[right.review]},
			})
		}
	}
	markAmbiguousDuplicates(sourceMap.Entries, &result)
	markMoves(&result)
	return result, nil
}

func markAmbiguousDuplicates(baseline []SourceMapEntry, result *Classification) {
	counts := map[string]int{}
	for _, entry := range baseline {
		counts[entry.Fingerprint]++
	}
	for index := range result.Changes {
		change := &result.Changes[index]
		if change.Class != ChangeSafe {
			continue
		}
		switch change.Operation {
		case OperationEdit:
			if len(change.Baseline) != 1 || counts[change.Baseline[0].Fingerprint] < 2 {
				continue
			}
			uniqueBefore := change.BaselineStart > 0 && counts[baseline[change.BaselineStart-1].Fingerprint] == 1
			uniqueAfter := change.BaselineEnd < len(baseline) && counts[baseline[change.BaselineEnd].Fingerprint] == 1
			if !uniqueBefore && !uniqueAfter {
				change.Class, change.Reason = ChangeConflict, "edited duplicate block has no unique neighboring context"
			}
		case OperationInsert:
			if change.BaselineStart <= 0 || change.BaselineStart >= len(baseline) || counts[baseline[change.BaselineStart-1].Fingerprint] != 1 || counts[baseline[change.BaselineStart].Fingerprint] != 1 {
				change.Class, change.Reason = ChangeConflict, "insertion anchors are duplicated or incomplete"
			}
		}
	}
}

func ParseAndClassifyReview(ctx context.Context, parser Parser, reader string, sourceMap SourceMap, reviewContent []byte) (Classification, error) {
	blocks, err := ParseBaseline(ctx, parser, reader, reviewContent)
	if err != nil {
		return Classification{}, err
	}
	return ClassifyReviewContext(ctx, sourceMap, blocks)
}

func ParseAndClassifyPortableReview(ctx context.Context, parser Parser, reader string, sourceMap SourceMap, reviewContent []byte) (Classification, error) {
	blocks, err := ParseBaseline(ctx, parser, reader, reviewContent)
	if err != nil {
		return Classification{}, err
	}
	return ClassifyPortableReviewContext(ctx, sourceMap, blocks)
}

func classifyPortableGap(baseline []SourceMapEntry, review []Block, baselineStart, baselineEnd, reviewStart, reviewEnd, previous, next int) ClassifiedChange {
	change := classifyGap(baseline, review, baselineStart, baselineEnd, reviewStart, reviewEnd, previous, next)
	if baselineEnd-baselineStart != 1 || reviewEnd-reviewStart != 1 {
		return change
	}
	entry, remote := baseline[baselineStart], review[reviewStart]
	change.Member, change.SourceRange = entry.Member, entry.SourceRange
	switch {
	case entry.Generated:
		change.Class, change.Reason = ChangeGenerated, "generated publication content changed"
	case !entry.Eligible || !eligibleKind(remote.Kind):
		change.Class, change.Reason = ChangeUnsupported, "changed block type is not supported for source patches"
	case !compatibleKinds(entry.Kind, remote.Kind):
		change.Class, change.Reason = ChangeConflict, "changed block type moved across semantic kinds"
	default:
		change.Class, change.Reason, change.PreserveMarkup = ChangeSafe, "", true
	}
	return change
}

func classifyGap(baseline []SourceMapEntry, review []Block, baselineStart, baselineEnd, reviewStart, reviewEnd, previous, next int) ClassifiedChange {
	change := ClassifiedChange{
		BaselineStart: baselineStart, BaselineEnd: baselineEnd, ReviewStart: reviewStart, ReviewEnd: reviewEnd,
		Baseline: append([]SourceMapEntry(nil), baseline[baselineStart:baselineEnd]...), Review: append([]Block(nil), review[reviewStart:reviewEnd]...),
	}
	baselineCount, reviewCount := baselineEnd-baselineStart, reviewEnd-reviewStart
	switch {
	case baselineCount == 1 && reviewCount == 1:
		change.Operation = OperationEdit
		entry, remote := baseline[baselineStart], review[reviewStart]
		change.Member, change.SourceRange = entry.Member, entry.SourceRange
		if ignoredReferenceOnlyChange(entry, remote) {
			change.Class, change.Operation, change.Reason = ChangeUnchanged, OperationNone, "generated link presentation changed"
		} else if entry.Generated {
			change.Class, change.Reason = ChangeGenerated, "generated publication content changed"
		} else if !entry.Eligible || !eligibleKind(remote.Kind) {
			change.Class, change.Reason = ChangeUnsupported, "changed block type is not supported for source patches"
		} else if !compatibleKinds(entry.Kind, remote.Kind) {
			change.Class, change.Reason = ChangeConflict, "changed block type moved across semantic kinds"
		} else if !compatibleStructure(entry, remote) {
			change.Class, change.Reason = ChangeUnsupported, "Markdown structure changed and cannot be patched safely"
		} else if entry.TrackReferences {
			change.Class, change.Reason = ChangeUnsupported, "text edits inside links or images are not supported"
		} else {
			change.Class = ChangeSafe
		}
	case baselineCount > 0 && reviewCount == 0:
		change.Operation = OperationDelete
		if allGenerated(change.Baseline) {
			change.Class, change.Reason = ChangeGenerated, "generated publication content was deleted"
		} else if member, sourceRange, ok := ownedEligibleRange(change.Baseline); ok && deletionAnchored(baseline, previous, next, member) {
			change.Class, change.Member, change.SourceRange = ChangeSafe, member, sourceRange
		} else if containsUnsupported(change.Baseline) {
			change.Class, change.Reason = ChangeUnsupported, "deleted range contains an unsupported block"
		} else {
			change.Class, change.Reason = ChangeConflict, "deleted range crosses ownership or generated boundaries"
		}
	case baselineCount == 0 && reviewCount > 0:
		change.Operation = OperationInsert
		if !allEligibleReview(change.Review) {
			change.Class, change.Reason = ChangeUnsupported, "inserted block type is not supported for source patches"
			break
		}
		if previous >= 0 && next < len(baseline) {
			before, after := baseline[previous], baseline[next]
			if before.Generated && after.Generated {
				change.Class, change.Reason = ChangeGenerated, "content was inserted inside a generated region"
			} else if before.Member != "" && before.Member == after.Member && usableInsertionAnchor(before) && usableInsertionAnchor(after) {
				change.Class, change.Member = ChangeSafe, before.Member
				change.SourceRange = ByteRange{Start: after.SourceRange.Start, End: after.SourceRange.Start}
			} else {
				change.Class, change.Reason = ChangeConflict, "inserted blocks do not have two unchanged anchors in one member"
			}
		} else {
			change.Class, change.Reason = ChangeConflict, "inserted blocks do not have two unchanged anchors"
		}
	default:
		change.Operation = OperationEdit
		if allGenerated(change.Baseline) {
			change.Class, change.Reason = ChangeGenerated, "generated publication region changed"
		} else if containsUnsupported(change.Baseline) || !allEligibleReview(change.Review) {
			change.Class, change.Reason = ChangeUnsupported, "structural change includes an unsupported block"
		} else {
			change.Class, change.Reason = ChangeConflict, "multiple blocks changed without a unique one-to-one mapping"
		}
	}
	return change
}

func usableInsertionAnchor(entry SourceMapEntry) bool {
	return !entry.Generated && entry.Member != "" && entry.SourceRange.Start >= 0 && entry.SourceRange.End > entry.SourceRange.Start
}

func compatibleStructure(baseline SourceMapEntry, review Block) bool {
	if baseline.BaselineKind != review.Kind || baseline.HeadingLevel != review.HeadingLevel || baseline.ListKind != review.ListKind || baseline.MarkupHash != review.MarkupHash {
		return false
	}
	if !baseline.TrackReferences {
		return true
	}
	return baseline.Signature == blockSignature(review, true) || sameReferences(baseline.References, review.References)
}

func ignoredReferenceOnlyChange(baseline SourceMapEntry, review Block) bool {
	return !baseline.Generated && !baseline.TrackReferences && baseline.Fingerprint == review.Fingerprint && baseline.BaselineKind == review.Kind && baseline.HeadingLevel == review.HeadingLevel && baseline.ListKind == review.ListKind && baseline.MarkupHash == review.MarkupHash
}

func sameReferences(left, right []BlockReference) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func compatibleKinds(source, remote BlockKind) bool {
	return source == remote || remote == BlockParagraph && (source == BlockHeading || source == BlockBlockQuote)
}

func allGenerated(entries []SourceMapEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !entry.Generated {
			return false
		}
	}
	return true
}

func containsUnsupported(entries []SourceMapEntry) bool {
	for _, entry := range entries {
		if !entry.Generated && !entry.Eligible {
			return true
		}
	}
	return false
}

func allEligibleReview(blocks []Block) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if !eligibleKind(block.Kind) || len(block.References) > 0 || block.MarkupHash != "" {
			return false
		}
	}
	return true
}

func ownedEligibleRange(entries []SourceMapEntry) (string, ByteRange, bool) {
	if len(entries) == 0 {
		return "", ByteRange{}, false
	}
	member := entries[0].Member
	result := entries[0].SourceRange
	for _, entry := range entries {
		if entry.Generated || !entry.Eligible || entry.Member == "" || entry.Member != member || entry.SourceRange.Start < result.Start || entry.SourceRange.End < result.End {
			return "", ByteRange{}, false
		}
		result.End = entry.SourceRange.End
	}
	return member, result, true
}

func deletionAnchored(baseline []SourceMapEntry, previous, next int, member string) bool {
	return previous >= 0 && next < len(baseline) && baseline[previous].Member == member && baseline[next].Member == member && !baseline[previous].Generated && !baseline[next].Generated
}

func markMoves(result *Classification) {
	deleted := map[string][]int{}
	inserted := map[string][]int{}
	for index, change := range result.Changes {
		if change.Class == ChangeUnchanged || change.Class == ChangeGenerated {
			continue
		}
		if change.Operation == OperationDelete {
			for _, entry := range change.Baseline {
				deleted[entry.Fingerprint] = append(deleted[entry.Fingerprint], index)
			}
		}
		if change.Operation == OperationInsert {
			for _, block := range change.Review {
				inserted[block.Fingerprint] = append(inserted[block.Fingerprint], index)
			}
		}
	}
	for fingerprint, deletions := range deleted {
		insertions := inserted[fingerprint]
		if len(deletions) == 0 || len(insertions) == 0 {
			continue
		}
		for _, index := range append(deletions, insertions...) {
			result.Changes[index].Class = ChangeConflict
			result.Changes[index].Reason = "block move or reorder detected"
		}
	}
}
