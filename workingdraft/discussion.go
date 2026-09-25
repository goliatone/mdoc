package workingdraft

import (
	"context"
	"net/url"

	"google.golang.org/api/drive/v3"
)

func (s *Service) ReadDiscussion(ctx context.Context, input DiscussionInput) (DiscussionPage, error) {
	ref, err := canonicalSource(input.Source)
	if err != nil {
		return DiscussionPage{}, err
	}
	ref.TabID = ""
	result := DiscussionPage{Source: ref, Threads: []Thread{}, RetrievedAt: s.now().UTC()}
	f, err := s.provider.File(ctx, ref.DocumentID)
	if err != nil {
		return result, safeError(err)
	}
	if f == nil || f.Id != ref.DocumentID || f.Trashed || f.MimeType != "application/vnd.google-apps.document" {
		return result, fail(InvalidSource, "source must be an accessible native Google Doc")
	}
	if f.DriveId != "" {
		return result, fail(UnsupportedContent, "Shared Drive documents are not supported")
	}
	page, err := s.provider.Comments(ctx, ref.DocumentID, input.Cursor)
	if err != nil {
		return result, safeError(err)
	}
	if page == nil {
		return result, fail(IncompleteDiscussion, "Google returned no discussion page")
	}
	result.NextCursor = page.NextPageToken
	for _, c := range page.Comments {
		if c == nil || c.Id == "" {
			return result, fail(IncompleteDiscussion, "discussion contains an unidentified thread")
		}
		thread := Thread{ID: c.Id, Content: c.Content, Author: author(c.Author), CreatedAt: c.CreatedTime, ModifiedAt: c.ModifiedTime, Resolved: c.Resolved, Deleted: c.Deleted, SourceURL: sourceURL(ref) + "?disco=" + url.QueryEscape(c.Id), Replies: []Reply{}}
		if c.Anchor != "" {
			anchor := c.Anchor
			thread.Anchor = &anchor
		}
		if c.QuotedFileContent != nil {
			thread.QuotedContext = c.QuotedFileContent.Value
		}
		for _, r := range c.Replies {
			if r == nil || r.Id == "" {
				return result, fail(IncompleteDiscussion, "discussion contains an unidentified reply")
			}
			thread.Replies = append(thread.Replies, Reply{ID: r.Id, Content: r.Content, Author: author(r.Author), CreatedAt: r.CreatedTime, ModifiedAt: r.ModifiedTime, Deleted: r.Deleted, Action: r.Action})
		}
		result.Threads = append(result.Threads, thread)
	}
	result.Complete = input.Cursor == "" && result.NextCursor == ""
	return result, nil
}
func author(u *drive.User) *Author {
	if u == nil || u.DisplayName == "" && u.EmailAddress == "" && !u.Me {
		return nil
	}
	return &Author{DisplayName: u.DisplayName, Email: u.EmailAddress, Me: u.Me}
}

func (s *Service) ReadAllDiscussion(ctx context.Context, source SourceRef) (DiscussionPage, error) {
	result := DiscussionPage{Threads: []Thread{}}
	cursor := ""
	seen := map[string]bool{}
	threadIndex := map[string]int{}
	for count := range 100 {
		page, err := s.ReadDiscussion(ctx, DiscussionInput{Source: source, Cursor: cursor})
		result.Source = page.Source
		result.RetrievedAt = page.RetrievedAt
		result.NextCursor = page.NextCursor
		for _, thread := range page.Threads {
			if index, ok := threadIndex[thread.ID]; ok {
				result.Threads[index] = thread
			} else {
				threadIndex[thread.ID] = len(result.Threads)
				result.Threads = append(result.Threads, thread)
			}
		}
		if err != nil {
			if count == 0 {
				return result, err
			}
			return result, &Error{Code: IncompleteDiscussion, Message: "discussion refresh interrupted; retain previously seen records and retry from the first page", Retryable: true}
		}
		if page.NextCursor == "" {
			result.Complete = true
			return result, nil
		}
		if seen[page.NextCursor] {
			return result, fail(IncompleteDiscussion, "Google repeated a discussion cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return result, fail(IncompleteDiscussion, "discussion page limit reached; retain prior records")
}
