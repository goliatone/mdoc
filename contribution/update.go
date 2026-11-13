package contribution

import (
	"context"

	"google.golang.org/api/docs/v1"
)

func (s *Service) PrepareUpdate(ctx context.Context, in UpdateInput) (UpdatePlan, error) {
	if e := s.meta(in.Meta); e != nil {
		return UpdatePlan{}, e
	}
	if e := validReplacement(in.Replacement); e != nil {
		return UpdatePlan{}, e
	}
	if in.ExpectedRevision == "" {
		return UpdatePlan{}, fail(InvalidInput, "expected provider revision is required")
	}
	ref, e := canonicalSource(in.Source)
	if e != nil {
		return UpdatePlan{}, e
	}
	in.Source = ref
	if e = s.documentAccess(ctx, ref, false); e != nil {
		return UpdatePlan{}, e
	}
	snap, e := s.draft.Capture(ctx, ref)
	if e != nil {
		return UpdatePlan{}, safe(e)
	}
	if !snap.BodyUsable {
		return UpdatePlan{}, fail(UnsupportedContent, "document contains suggestions or unsupported body content")
	}
	if snap.ProviderRevision != in.ExpectedRevision {
		return UpdatePlan{}, fail(Stale, "document revision changed")
	}
	return selectSpan(snap, in)
}
func (s *Service) ApplyUpdate(ctx context.Context, r UpdateRequest) (Outcome, error) {
	p := r.Plan
	o := outcome(UpdateKind, p.Input.Meta, p.Digest, r.Attempt)
	o.DocumentID = p.Input.Source.DocumentID
	o.TabID = p.Input.Source.TabID
	if e := validReplacement(p.Input.Replacement); e != nil {
		return rejected(o, e)
	}
	if e := validateUpdate(p); e != nil {
		return rejected(o, e)
	}
	if e := s.authorize(p.Input.Meta, p.Digest, r.ExpectedDigest, r.Attempt); e != nil {
		return rejected(o, e)
	}
	fresh, e := s.PrepareUpdate(ctx, p.Input)
	if e != nil {
		return rejected(o, e)
	}
	if fresh.Digest != p.Digest {
		return rejected(o, fail(Stale, "native selection or style evidence changed"))
	}
	requests := []*docs.Request{}
	if p.Start < p.End {
		requests = append(requests, &docs.Request{DeleteContentRange: &docs.DeleteContentRangeRequest{Range: &docs.Range{StartIndex: p.Start, EndIndex: p.End, TabId: p.Input.Source.TabID}}})
	}
	if p.Input.Replacement != "" {
		requests = append(requests, &docs.Request{InsertText: &docs.InsertTextRequest{Location: &docs.Location{Index: p.Start, TabId: p.Input.Source.TabID}, Text: p.Input.Replacement}})
	}
	if ctx.Err() != nil {
		return rejected(o, ctx.Err())
	}
	ack, e := s.docs.Documents.BatchUpdate(o.DocumentID, &docs.BatchUpdateDocumentRequest{Requests: requests, WriteControl: &docs.WriteControl{RequiredRevisionId: p.Input.ExpectedRevision}}).Context(ctx).Do()
	if e != nil {
		return mutationError(o, e)
	}
	if ack == nil || ack.DocumentId != o.DocumentID {
		o.Status = Unknown
		return rejected(o, fail(ReconciliationRequired, "update acknowledgment omitted document identity"))
	}
	if ack.WriteControl != nil {
		o.ProviderRevision = ack.WriteControl.RequiredRevisionId
	}
	return s.committed(o), nil
}
