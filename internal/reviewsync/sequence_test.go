package reviewsync

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSequenceMatchesHandlesLargeUniqueInputs(t *testing.T) {
	const size = 20000
	left := make([]string, size)
	right := make([]string, size)
	for index := range size {
		left[index] = fmt.Sprintf("item-%d", index)
		right[index] = left[index]
	}
	right[size/2] = "changed"
	matches, err := sequenceMatches(context.Background(), left, right)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != size-1 || matches[0] != [2]int{0, 0} || matches[len(matches)-1] != [2]int{size - 1, size - 1} {
		t.Fatalf("matches = %d with edges %#v and %#v", len(matches), matches[0], matches[len(matches)-1])
	}
}

func TestSequenceMatchesBoundsAmbiguousWorkAndHonorsCancellation(t *testing.T) {
	left := make([]string, 2000)
	right := make([]string, 2000)
	for index := range left {
		left[index] = "left"
		right[index] = "right"
	}
	if _, err := sequenceMatches(context.Background(), left, right); !errors.Is(err, ErrComparisonComplexity) {
		t.Fatalf("complexity error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sequenceMatches(ctx, []string{"one"}, []string{"two"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}
