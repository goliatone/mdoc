package contribution

type Code string

const (
	InvalidPlan            Code = "invalid_plan"
	InvalidInput           Code = "invalid_input"
	AccountUnavailable     Code = "account_unavailable"
	AccessDenied           Code = "access_denied"
	UnsupportedContent     Code = "unsupported_content"
	UnsupportedText        Code = "unsupported_text"
	Stale                  Code = "stale_precondition"
	TransportFailure       Code = "transport_failure"
	LimitExceeded          Code = "limit_exceeded"
	ReconciliationRequired Code = "reconciliation_required"
	Unavailable            Code = "unavailable"
)

type Error struct {
	Status  Status `json:"status,omitempty"`
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string    { return e.Message }
func fail(c Code, m string) error { return &Error{Code: c, Message: m} }
