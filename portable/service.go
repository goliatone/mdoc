package portable

import (
	"sync"
	"time"
)

type Service struct {
	providerMu sync.Mutex
	provider   Provider
	converter  Converter
	now        func() time.Time
	options    Options
}

func New(o Options) (*Service, error) {
	if o.HTTPClient != nil && o.TokenSource != nil || o.Provider != nil && (o.HTTPClient != nil || o.TokenSource != nil) {
		return nil, fail("invalid_config", "select exactly one provider, authenticated client or token source")
	}
	if o.Converter == nil {
		o.Converter = PandocConverter{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{provider: o.Provider, converter: o.Converter, now: o.Now, options: o}, nil
}

var _ API = (*Service)(nil)
