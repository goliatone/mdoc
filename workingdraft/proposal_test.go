package workingdraft_test

import (
	"context"
	wd "github.com/goliatone/mdoc/workingdraft"
	"testing"
)

func TestProposalRejectsCorruptionAndReportsDivergence(t *testing.T) {
	ctx := context.Background()
	p := &provider{doc: document("before")}
	s, _ := wd.New(wd.Options{Provider: p})
	base, err := s.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	p.doc = document("after")
	incoming, err := s.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	input := wd.CompareInput{Baseline: base, Incoming: incoming, Current: wd.Content{Title: "Draft", Body: "local"}}
	first, err := wd.Compare(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := wd.Compare(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || !first.LocalDivergence || len(first.Changes) != 1 || first.ProposedContent.Body != incoming.NormalizedContent.Body {
		t.Fatal("incorrect proposal")
	}
	input.Incoming.RawContent = []byte(`{"changed":true}`)
	if _, err := wd.Compare(ctx, input); err == nil {
		t.Fatal("accepted corrupt snapshot")
	}
}
