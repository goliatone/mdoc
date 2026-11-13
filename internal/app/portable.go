package app

import (
	"context"
	"github.com/goliatone/mdoc/portable"
)

// PortableService composes existing explicitly selected authorization for reads.
// Offline callers construct portable.New directly and never select an account.
func PortableService(ctx context.Context, credentials ClientProvider) (*portable.Service, error) {
	if credentials == nil {
		return nil, &portable.Error{Code: "account_unavailable", Message: "run mdoc auth login before reading Drive bundles"}
	}
	client, e := credentials.Client(ctx)
	if e != nil {
		return nil, &portable.Error{Code: "account_unavailable", Message: "Google credentials unavailable; run mdoc auth login"}
	}
	return portable.New(portable.Options{HTTPClient: client})
}
