package contribution

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goliatone/mdoc/workingdraft"
	"google.golang.org/api/drive/v3"
)

const commentFields = "id,content,createdTime,modifiedTime,resolved,deleted,author(displayName,emailAddress,me),quotedFileContent,anchor"
const replyFields = "id,content,createdTime,modifiedTime,deleted,action,author(displayName,emailAddress,me)"

func validReplyText(s string) error {
	if strings.TrimSpace(s) == "" || len(s) > MaxText || !utf8.ValidString(s) {
		return fail(InvalidInput, "invalid or oversized reply")
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fail(UnsupportedText, "reply contains unsupported controls")
		}
	}
	return nil
}
func (s *Service) replySource(ctx context.Context, in workingdraft.SourceRef) (workingdraft.SourceRef, error) {
	r, e := canonicalSource(in)
	if e != nil {
		return r, e
	}
	if e = s.documentAccess(ctx, r, true); e != nil {
		return r, e
	}
	d, e := s.docs.Documents.Get(r.DocumentID).IncludeTabsContent(true).SuggestionsViewMode("SUGGESTIONS_INLINE").Fields("documentId,tabs(tabProperties,childTabs(tabProperties))").Context(ctx).Do()
	if e != nil {
		return r, safe(e)
	}
	if d.DocumentId != r.DocumentID || len(d.Tabs) != 1 || d.Tabs[0] == nil || d.Tabs[0].TabProperties == nil || len(d.Tabs[0].ChildTabs) != 0 {
		return r, fail(UnsupportedContent, "replies require a single root tab")
	}
	tab := d.Tabs[0].TabProperties.TabId
	if tab == "" || r.TabID != "" && r.TabID != tab {
		return r, fail(InvalidInput, "requested tab does not match")
	}
	r.TabID = tab
	return r, nil
}
func (s *Service) thread(ctx context.Context, file, id string, requireOpen bool) (ThreadState, []*drive.Reply, error) {
	if !identifier.MatchString(id) {
		return ThreadState{}, nil, fail(InvalidInput, "invalid comment identity")
	}
	read := func() (*drive.Comment, error) {
		c, e := s.drive.Comments.Get(file, id).IncludeDeleted(true).Fields(commentFields).Context(ctx).Do()
		if e != nil {
			return nil, safe(e)
		}
		if c.Id != id {
			return nil, fail(LimitExceeded, "comment identity missing")
		}
		if requireOpen && (c.Deleted || c.Resolved) {
			return nil, fail(Stale, "thread is deleted or resolved")
		}
		return c, nil
	}
	c, e := read()
	if e != nil {
		return ThreadState{}, nil, e
	}
	var replies []*drive.Reply
	token := ""
	seen := map[string]bool{}
	ids := map[string]bool{}
	complete := false
	size := 0
	for page := 0; page < 100; page++ {
		list, e := s.drive.Replies.List(file, id).IncludeDeleted(true).PageSize(100).PageToken(token).Fields("nextPageToken,replies(" + replyFields + ")").Context(ctx).Do()
		if e != nil {
			return ThreadState{}, nil, safe(e)
		}
		b, _ := json.Marshal(list)
		size += len(b)
		if size > maxResponse {
			return ThreadState{}, nil, fail(LimitExceeded, "thread exceeds evidence limit")
		}
		for _, r := range list.Replies {
			if r == nil || !identifier.MatchString(r.Id) || ids[r.Id] {
				return ThreadState{}, nil, fail(LimitExceeded, "reply evidence incomplete or duplicated")
			}
			ids[r.Id] = true
			replies = append(replies, r)
		}
		if list.NextPageToken == "" {
			complete = true
			break
		}
		if seen[list.NextPageToken] {
			break
		}
		seen[list.NextPageToken] = true
		token = list.NextPageToken
	}
	if !complete {
		return ThreadState{}, nil, fail(LimitExceeded, "reply pagination incomplete")
	}
	after, e := read()
	if e != nil {
		return ThreadState{}, nil, e
	}
	if digest(c) != digest(after) {
		return ThreadState{}, nil, fail(Stale, "thread changed during retrieval")
	}
	sort.Slice(replies, func(i, j int) bool { return replies[i].Id < replies[j].Id })
	return ThreadState{Fingerprint: digest([]any{c, replies}), ModifiedAt: c.ModifiedTime}, replies, nil
}
func (s *Service) PrepareReply(ctx context.Context, in ReplyInput) (ReplyPlan, error) {
	if e := s.meta(in.Meta); e != nil {
		return ReplyPlan{}, e
	}
	if e := validReplyText(in.Content); e != nil {
		return ReplyPlan{}, e
	}
	r, e := s.replySource(ctx, in.Source)
	if e != nil {
		return ReplyPlan{}, e
	}
	in.Source = r
	thread, _, e := s.thread(ctx, r.DocumentID, in.CommentID, true)
	if e != nil {
		return ReplyPlan{}, e
	}
	p := ReplyPlan{Kind: ReplyKind, Input: in, Thread: thread}
	p.Digest = replyDigest(p)
	return p, validateReply(p)
}
func (s *Service) Reply(ctx context.Context, r ReplyRequest) (Outcome, error) {
	p := r.Plan
	o := outcome(ReplyKind, p.Input.Meta, p.Digest, r.Attempt)
	o.DocumentID = p.Input.Source.DocumentID
	o.TabID = p.Input.Source.TabID
	o.CommentID = p.Input.CommentID
	if e := validateReply(p); e != nil {
		return rejected(o, e)
	}
	if e := s.authorize(p.Input.Meta, p.Digest, r.ExpectedDigest, r.Attempt); e != nil {
		return rejected(o, e)
	}
	fresh, e := s.PrepareReply(ctx, p.Input)
	if e != nil {
		return rejected(o, e)
	}
	if fresh.Digest != p.Digest {
		return rejected(o, fail(Stale, "thread fingerprint changed"))
	}
	if ctx.Err() != nil {
		return rejected(o, ctx.Err())
	}
	ack, e := s.drive.Replies.Create(o.DocumentID, o.CommentID, &drive.Reply{Content: p.Input.Content}).Fields("id").Context(ctx).Do()
	if e != nil {
		return mutationError(o, e)
	}
	if ack == nil || !identifier.MatchString(ack.Id) {
		o.Status = Unknown
		return rejected(o, fail(ReconciliationRequired, "reply acknowledgment omitted identity"))
	}
	o.ReplyID = ack.Id
	o = s.committed(o)
	o.Diagnostics = []string{"thread recheck is not atomic with reply creation; concurrent thread changes remain possible"}
	return o, nil
}
