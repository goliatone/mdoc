package googleapi

import (
	"context"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"net/http"
)

type WorkingDraftClient struct {
	drive *drive.Service
	docs  *docs.Service
}

func NewWorkingDraftClient(ctx context.Context, client *http.Client) (*WorkingDraftClient, error) {
	d, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	g, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return &WorkingDraftClient{d, g}, nil
}
func (c *WorkingDraftClient) File(ctx context.Context, id string) (*drive.File, error) {
	return c.drive.Files.Get(id).Fields("id,name,mimeType,version,driveId,trashed").Context(ctx).Do()
}
func (c *WorkingDraftClient) Document(ctx context.Context, id string) (*docs.Document, error) {
	return c.docs.Documents.Get(id).IncludeTabsContent(true).SuggestionsViewMode("SUGGESTIONS_INLINE").Context(ctx).Do()
}
func (c *WorkingDraftClient) Comments(ctx context.Context, id, cursor string) (*drive.CommentList, error) {
	return c.drive.Comments.List(id).IncludeDeleted(true).PageSize(100).PageToken(cursor).Fields("nextPageToken,comments(id,content,author(displayName,emailAddress,me),createdTime,modifiedTime,resolved,deleted,quotedFileContent,anchor,replies(id,content,author(displayName,emailAddress,me),createdTime,modifiedTime,deleted,action))").Context(ctx).Do()
}
