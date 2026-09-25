package workingdraft

import "context"

func (s *Service) Compare(ctx context.Context, input CompareInput) (Proposal, error) {
	return Compare(ctx, input)
}
func Compare(ctx context.Context, input CompareInput) (Proposal, error) {
	if ctx.Err() != nil {
		return Proposal{}, safeError(ctx.Err())
	}
	for _, snapshot := range []Snapshot{input.Baseline, input.Incoming} {
		if err := VerifySnapshot(snapshot); err != nil {
			return Proposal{}, err
		}
		if !snapshot.BodyUsable {
			for _, d := range snapshot.Diagnostics {
				if d.Code == SuggestionsPending {
					return Proposal{}, fail(SuggestionsPending, "pending suggestions block proposals")
				}
			}
			return Proposal{}, fail(UnsupportedContent, "unusable snapshot body blocks proposals")
		}
	}
	if input.Baseline.ConversionVersion != input.Incoming.ConversionVersion {
		return Proposal{}, fail(UnsupportedContent, "baseline and incoming conversion versions differ")
	}
	if input.Baseline.Source != input.Incoming.Source {
		return Proposal{}, fail(InvalidSource, "baseline and incoming source identities differ")
	}
	baseline, incoming := input.Baseline.NormalizedContent, input.Incoming.NormalizedContent
	p := Proposal{BaseSnapshotDigest: input.Baseline.SnapshotDigest, IncomingSnapshotDigest: input.Incoming.SnapshotDigest, CurrentContentDigest: ContentDigest(input.Current), ProposedContent: incoming, Changes: []Change{}, Diagnostics: []Diagnostic{}, LocalDivergence: input.Current != baseline}
	if baseline.Title != incoming.Title {
		p.Changes = append(p.Changes, Change{Field: "title", Before: baseline.Title, After: incoming.Title})
	}
	if baseline.Body != incoming.Body {
		p.Changes = append(p.Changes, Change{Field: "body", Before: baseline.Body, After: incoming.Body})
	}
	value, err := digest(p)
	if err != nil {
		return Proposal{}, err
	}
	p.Digest = value
	return p, nil
}
