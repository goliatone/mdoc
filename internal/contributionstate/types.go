package contributionstate

import (
	"encoding/json"

	"github.com/goliatone/mdoc/contribution"
)

type Record struct {
	Version    int                      `json:"version"`
	Plan       json.RawMessage          `json:"plan"`
	State      string                   `json:"state"`
	Outcome    contribution.Outcome     `json:"outcome"`
	Inspection *contribution.Inspection `json:"inspection,omitempty"`
}
