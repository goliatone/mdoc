package app

import (
	"context"
	"github.com/goliatone/mdoc/workingdraft"
)

func WorkingDraftService(ctx context.Context, credentials ClientProvider) (*workingdraft.Service, error) {
	if credentials == nil {
		return nil, &workingdraft.Error{Code: workingdraft.AccountUnavailable, Message: "run mdoc auth login before reading a working draft"}
	}
	client, err := credentials.Client(ctx)
	if err != nil {
		return nil, &workingdraft.Error{Code: workingdraft.AccountUnavailable, Message: "Google credentials unavailable; run mdoc auth login"}
	}
	return workingdraft.New(workingdraft.Options{HTTPClient: client})
}
