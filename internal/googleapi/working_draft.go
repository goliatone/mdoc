package googleapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

type WorkingDraftClient struct {
	drive  *drive.Service
	client *http.Client
}

func NewWorkingDraftClient(ctx context.Context, client *http.Client) (*WorkingDraftClient, error) {
	d, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return &WorkingDraftClient{d, client}, nil
}
func (c *WorkingDraftClient) File(ctx context.Context, id string) (*drive.File, error) {
	return c.drive.Files.Get(id).Fields("id,name,mimeType,version,driveId,trashed").Context(ctx).Do()
}
func (c *WorkingDraftClient) Document(ctx context.Context, id string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://docs.googleapis.com/v1/documents/"+url.PathEscape(id)+"?includeTabsContent=true&suggestionsViewMode=SUGGESTIONS_INLINE", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	response.Body = io.NopCloser(io.LimitReader(response.Body, MaxExportBytes+1))
	if err := googleapi.CheckResponse(response); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxExportBytes {
		return nil, fmt.Errorf("document exceeds size limit")
	}
	return json.RawMessage(data), nil
}

func (c *WorkingDraftClient) Comments(ctx context.Context, id, cursor string) (*drive.CommentList, error) {
	return c.drive.Comments.List(id).IncludeDeleted(true).PageSize(100).PageToken(cursor).Fields("nextPageToken,comments(id,content,author(displayName,emailAddress,me),createdTime,modifiedTime,resolved,deleted,quotedFileContent,anchor,replies(id,content,author(displayName,emailAddress,me),createdTime,modifiedTime,deleted,action))").Context(ctx).Do()
}
