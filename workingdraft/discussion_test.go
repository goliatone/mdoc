package workingdraft_test

import (
	"context"
	"errors"
	wd "github.com/goliatone/mdoc/workingdraft"
	"google.golang.org/api/drive/v3"
	"testing"
)

type pagedProvider struct{ *provider }

func (p pagedProvider) Comments(_ context.Context, _ string, cursor string) (*drive.CommentList, error) {
	if cursor != "" {
		return nil, errors.New("secret provider failure")
	}
	return &drive.CommentList{NextPageToken: "next", Comments: []*drive.Comment{{Id: "thread", Content: "review", Replies: []*drive.Reply{{Id: "reply", Deleted: true}}}}}, nil
}
func TestDiscussionIndependentAndPartialReadCannotAuthorizeDeletion(t *testing.T) {
	d := document("draft")
	d.Tabs[0].DocumentTab.Body.Content[1].Paragraph.Elements[0].TextRun.SuggestedInsertionIds = []string{"suggestion"}
	s, _ := wd.New(wd.Options{Provider: pagedProvider{&provider{doc: d}}})
	ctx := context.Background()
	capture, err := s.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil || capture.BodyUsable {
		t.Fatalf("suggestion: %v", err)
	}
	page, err := s.ReadDiscussion(ctx, wd.DiscussionInput{Source: wd.SourceRef{DocumentID: "doc"}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Complete || len(page.Threads) != 1 || page.Threads[0].Author != nil || page.Threads[0].Anchor != nil || !page.Threads[0].Replies[0].Deleted {
		t.Fatal("incorrect partial or unknown metadata")
	}
	partial, err := s.ReadAllDiscussion(ctx, wd.SourceRef{DocumentID: "doc"})
	var typed *wd.Error
	if !errors.As(err, &typed) || typed.Code != wd.IncompleteDiscussion || partial.Complete || len(partial.Threads) != 1 {
		t.Fatalf("unsafe partial refresh: %+v %v", partial, err)
	}
}
