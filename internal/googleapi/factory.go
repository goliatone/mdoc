package googleapi

import (
	"context"
	"net/http"

	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type Clients struct {
	Drive Drive
	Docs  Docs
}

type Factory interface {
	New(context.Context, *http.Client) (Clients, error)
}

type ServiceFactory struct{}

func (ServiceFactory) New(ctx context.Context, client *http.Client) (Clients, error) {
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return Clients{}, classify("create Drive API client", err)
	}
	docsService, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return Clients{}, classify("create Docs API client", err)
	}
	return Clients{Drive: NewDriveClient(driveService), Docs: NewDocsClient(docsService)}, nil
}
