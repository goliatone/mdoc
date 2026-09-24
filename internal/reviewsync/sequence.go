package reviewsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

const maxDynamicSequenceCells int64 = 1 << 20

var ErrComparisonComplexity = errors.New("comparison complexity limit exceeded")

type ComparisonComplexityError struct {
	Left  int
	Right int
}

func (e *ComparisonComplexityError) Error() string {
	return fmt.Sprintf("comparison complexity limit exceeded for ambiguous sequences of %d and %d items; reduce the review size or add unique context", e.Left, e.Right)
}

func (e *ComparisonComplexityError) Unwrap() error { return ErrComparisonComplexity }

func sequenceMatches(ctx context.Context, left, right []string) ([][2]int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := make([][2]int, 0, minInt(len(left), len(right)))
	if err := appendSequenceMatches(ctx, left, right, 0, len(left), 0, len(right), &result, 0); err != nil {
		return nil, err
	}
	return result, nil
}

func appendSequenceMatches(ctx context.Context, left, right []string, leftStart, leftEnd, rightStart, rightEnd int, result *[][2]int, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 1024 {
		return &ComparisonComplexityError{Left: leftEnd - leftStart, Right: rightEnd - rightStart}
	}
	for leftStart < leftEnd && rightStart < rightEnd && left[leftStart] == right[rightStart] {
		if leftStart&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		*result = append(*result, [2]int{leftStart, rightStart})
		leftStart++
		rightStart++
	}
	suffix := 0
	for leftStart < leftEnd && rightStart < rightEnd && left[leftEnd-1] == right[rightEnd-1] {
		if suffix&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		leftEnd--
		rightEnd--
		suffix++
	}
	if leftStart < leftEnd && rightStart < rightEnd {
		anchors, err := uniqueSequenceAnchors(ctx, left, right, leftStart, leftEnd, rightStart, rightEnd)
		if err != nil {
			return err
		}
		if len(anchors) == 0 {
			matches, err := boundedSequenceMatches(ctx, left, right, leftStart, leftEnd, rightStart, rightEnd)
			if err != nil {
				return err
			}
			*result = append(*result, matches...)
		} else {
			previousLeft, previousRight := leftStart, rightStart
			for _, anchor := range anchors {
				if err := appendSequenceMatches(ctx, left, right, previousLeft, anchor[0], previousRight, anchor[1], result, depth+1); err != nil {
					return err
				}
				*result = append(*result, anchor)
				previousLeft, previousRight = anchor[0]+1, anchor[1]+1
			}
			if err := appendSequenceMatches(ctx, left, right, previousLeft, leftEnd, previousRight, rightEnd, result, depth+1); err != nil {
				return err
			}
		}
	}
	for index := 0; index < suffix; index++ {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		*result = append(*result, [2]int{leftEnd + index, rightEnd + index})
	}
	return nil
}

type sequenceCount struct {
	count int
	index int
}

func uniqueSequenceAnchors(ctx context.Context, left, right []string, leftStart, leftEnd, rightStart, rightEnd int) ([][2]int, error) {
	leftCounts := make(map[string]sequenceCount, leftEnd-leftStart)
	for index := leftStart; index < leftEnd; index++ {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		count := leftCounts[left[index]]
		count.count++
		count.index = index
		leftCounts[left[index]] = count
	}
	rightCounts := make(map[string]sequenceCount, rightEnd-rightStart)
	for index := rightStart; index < rightEnd; index++ {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		count := rightCounts[right[index]]
		count.count++
		count.index = index
		rightCounts[right[index]] = count
	}
	candidates := make([][2]int, 0)
	for value, leftCount := range leftCounts {
		rightCount, ok := rightCounts[value]
		if leftCount.count == 1 && ok && rightCount.count == 1 {
			candidates = append(candidates, [2]int{leftCount.index, rightCount.index})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i][0] < candidates[j][0] })
	if len(candidates) < 2 {
		return candidates, nil
	}
	tails := make([]int, 0, len(candidates))
	tailIndexes := make([]int, 0, len(candidates))
	previous := make([]int, len(candidates))
	for index, candidate := range candidates {
		position := sort.Search(len(tails), func(i int) bool { return tails[i] >= candidate[1] })
		previous[index] = -1
		if position > 0 {
			previous[index] = tailIndexes[position-1]
		}
		if position == len(tails) {
			tails = append(tails, candidate[1])
			tailIndexes = append(tailIndexes, index)
		} else {
			tails[position] = candidate[1]
			tailIndexes[position] = index
		}
	}
	anchors := make([][2]int, len(tails))
	for index, candidateIndex := len(anchors)-1, tailIndexes[len(tailIndexes)-1]; index >= 0; index-- {
		anchors[index] = candidates[candidateIndex]
		candidateIndex = previous[candidateIndex]
	}
	return anchors, nil
}

func boundedSequenceMatches(ctx context.Context, left, right []string, leftStart, leftEnd, rightStart, rightEnd int) ([][2]int, error) {
	leftSize, rightSize := leftEnd-leftStart, rightEnd-rightStart
	if int64(leftSize+1) > maxDynamicSequenceCells/int64(rightSize+1) {
		return nil, &ComparisonComplexityError{Left: leftSize, Right: rightSize}
	}
	dp := make([][]int, leftSize+1)
	for index := range dp {
		dp[index] = make([]int, rightSize+1)
	}
	for leftIndex := leftSize - 1; leftIndex >= 0; leftIndex-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for rightIndex := rightSize - 1; rightIndex >= 0; rightIndex-- {
			if left[leftStart+leftIndex] == right[rightStart+rightIndex] {
				dp[leftIndex][rightIndex] = dp[leftIndex+1][rightIndex+1] + 1
			} else if dp[leftIndex+1][rightIndex] >= dp[leftIndex][rightIndex+1] {
				dp[leftIndex][rightIndex] = dp[leftIndex+1][rightIndex]
			} else {
				dp[leftIndex][rightIndex] = dp[leftIndex][rightIndex+1]
			}
		}
	}
	result := make([][2]int, 0, dp[0][0])
	for leftIndex, rightIndex := 0, 0; leftIndex < leftSize && rightIndex < rightSize; {
		if left[leftStart+leftIndex] == right[rightStart+rightIndex] {
			result = append(result, [2]int{leftStart + leftIndex, rightStart + rightIndex})
			leftIndex++
			rightIndex++
		} else if dp[leftIndex+1][rightIndex] >= dp[leftIndex][rightIndex+1] {
			leftIndex++
		} else {
			rightIndex++
		}
	}
	return result, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
