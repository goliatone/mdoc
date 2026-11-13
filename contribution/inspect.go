package contribution

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/goliatone/mdoc/workingdraft"
)

func (s *Service) InspectOutcome(ctx context.Context, in InspectInput) (Inspection, error) {
	result := Inspection{Outcome: in.Previous, ObservedAt: s.options.Now().UTC()}
	count := 0
	var kind Kind
	var meta OperationMeta
	var dg string
	var source workingdraft.SourceRef
	var comment string
	var err error
	if p := in.Create; p != nil {
		count++
		kind = CreateKind
		meta = p.Input.Meta
		dg = p.Digest
		err = validateCreate(*p)
	}
	if p := in.Update; p != nil {
		count++
		kind = UpdateKind
		meta = p.Input.Meta
		dg = p.Digest
		source = p.Input.Source
		err = validateUpdate(*p)
	}
	if p := in.Reply; p != nil {
		count++
		kind = ReplyKind
		meta = p.Input.Meta
		dg = p.Digest
		source = p.Input.Source
		comment = p.Input.CommentID
		err = validateReply(*p)
	}
	if count != 1 || err != nil {
		return result, fail(InvalidPlan, "inspection requires one valid retained plan")
	}
	if e := s.meta(meta); e != nil {
		return result, e
	}
	o := in.Previous
	if o.Status == "" {
		o = outcome(kind, meta, dg, Attempt{})
		o.Status = Unknown
		o.DocumentID = source.DocumentID
		o.TabID = source.TabID
		o.CommentID = comment
	} else {
		if o.Kind != kind || o.RequestDigest != dg || o.Meta.Namespace != meta.Namespace || o.Meta.OperationID != meta.OperationID || o.Meta.AccountBinding != meta.AccountBinding || o.Meta.SchemaVersion != meta.SchemaVersion || source.DocumentID != "" && (o.DocumentID != source.DocumentID || o.TabID != source.TabID) || comment != "" && o.CommentID != comment {
			return result, fail(InvalidPlan, "receipt does not belong to retained plan")
		}
		switch o.Status {
		case Committed, Unknown, NotSent, Conflict:
		default:
			return result, fail(InvalidPlan, "invalid receipt status")
		}
	}
	result.Outcome = o
	observeError := func(e error) (Inspection, error) {
		e = safe(e)
		result.Evidence = Incomplete
		if e.(*Error).Code == AccessDenied || e.(*Error).Code == AccountUnavailable {
			result.Evidence = Inaccessible
		}
		if e.(*Error).Code == Stale {
			result.Evidence = Ambiguous
		}
		result.Diagnostics = append(result.Diagnostics, e.Error())
		return result, e
	}
	if p := in.Create; p != nil {
		if o.DocumentID != "" {
			if !identifier.MatchString(o.DocumentID) {
				return result, fail(InvalidPlan, "invalid retained document identity")
			}
			known, e := s.file(ctx, o.DocumentID)
			if e != nil {
				return observeError(e)
			}
			observation := &CreationObservation{
				DocumentID: known.Id, Trashed: known.Trashed, MetadataMatches: true,
				DestinationMatches: len(known.Parents) == 1 && known.Parents[0] == p.Input.ParentID,
				TopologyMatches:    known.MimeType == nativeDoc && known.DriveId == "",
			}
			for k, v := range properties(*p) {
				if known.AppProperties[k] != v {
					observation.MetadataMatches = false
				}
			}
			result.Creation = observation
			result.Evidence = Observed
			if known.Trashed {
				result.Diagnostics = append(result.Diagnostics, "retained document is now trashed")
			}
			if !observation.MetadataMatches || !observation.DestinationMatches || !observation.TopologyMatches {
				result.Diagnostics = append(result.Diagnostics, "retained document metadata, destination or topology has drifted")
			}
			if o.Status == Committed {
				return result, nil
			}
			result.Diagnostics = append(result.Diagnostics, "retained identity observation alone does not establish this operation's commitment")
		}
		f, e := s.findCreate(ctx, *p)
		if e != nil {
			return observeError(e)
		}
		if f == nil {
			if result.Creation == nil {
				result.Evidence = AbsentAtRead
			}
			result.Diagnostics = append(result.Diagnostics, "absence from discovery does not establish that an earlier creation was not sent")
			return result, nil
		}
		if o.DocumentID != "" && o.DocumentID != f.Id {
			return observeError(fail(Stale, "discovered identity differs from retained receipt"))
		}
		result.Evidence = Observed
		if result.Outcome.Status != Committed {
			result.Outcome.DocumentID = f.Id
			result.Outcome = s.committed(result.Outcome)
			result.Outcome.AcknowledgedAt = time.Time{}
			result.Diagnostics = append(result.Diagnostics, "creation recovered from exact metadata; no new acknowledgment")
		}
		if f.Trashed {
			result.Diagnostics = append(result.Diagnostics, "document is now trashed")
		}
		return result, nil
	}
	f, e := s.file(ctx, source.DocumentID)
	if e != nil {
		return observeError(e)
	}
	if f.Trashed {
		result.Evidence = AbsentAtRead
		result.Diagnostics = []string{"document is now trashed; retained outcome is unchanged"}
		return result, nil
	}
	if f.MimeType != nativeDoc || f.DriveId != "" {
		return observeError(fail(UnsupportedContent, "document topology changed"))
	}
	if p := in.Update; p != nil {
		snap, e := s.draft.Capture(ctx, source)
		if e != nil {
			return observeError(e)
		}
		if !snap.BodyUsable {
			return observeError(fail(UnsupportedContent, "current body cannot be compared"))
		}
		var root map[string]any
		if json.Unmarshal(snap.RawContent, &root) != nil {
			return observeError(fail(UnsupportedContent, "invalid current body"))
		}
		tabs := array(root, "tabs")
		if len(tabs) != 1 {
			return observeError(fail(UnsupportedContent, "current topology differs"))
		}
		tab, _ := tabs[0].(map[string]any)
		for _, v := range array(object(object(tab, "documentTab"), "body"), "content") {
			el, _ := v.(map[string]any)
			if number(el, "startIndex") == p.Input.Selection.ParagraphStart {
				para := object(el, "paragraph")
				current := ""
				for _, v := range array(para, "elements") {
					element, _ := v.(map[string]any)
					current += text(object(element, "textRun"), "content")
				}
				match := strings.TrimSuffix(current, "\n") == p.After
				result.Postcondition = &match
				result.Evidence = Observed
				result.Diagnostics = []string{"postcondition observation does not establish operation authorship"}
				return result, nil
			}
		}
		result.Evidence = AbsentAtRead
		return result, nil
	}
	p := in.Reply
	if o.ReplyID != "" {
		if !identifier.MatchString(o.ReplyID) {
			return observeError(fail(InvalidPlan, "invalid retained reply identity"))
		}
		r, e := s.drive.Replies.Get(source.DocumentID, comment, o.ReplyID).IncludeDeleted(true).Fields(replyFields).Context(ctx).Do()
		if e != nil {
			return observeError(e)
		}
		if r.Id != o.ReplyID {
			return observeError(fail(LimitExceeded, "reply identity missing"))
		}
		result.Evidence = Observed
		if r.Deleted {
			result.Evidence = AbsentAtRead
		}
		match := !r.Deleted && r.Content == p.Input.Content
		result.Postcondition = &match
		return result, nil
	}
	_, replies, e := s.thread(ctx, source.DocumentID, comment, false)
	if e != nil {
		return observeError(e)
	}
	for _, r := range replies {
		if !r.Deleted && r.Content == p.Input.Content {
			result.Candidates = append(result.Candidates, r.Id)
		}
	}
	result.Evidence = Ambiguous
	result.Diagnostics = []string{"matching reply text is a manual reconciliation candidate, never proof of this operation"}
	return result, nil
}
