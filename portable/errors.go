package portable

import "fmt"

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string                    { return e.Code + ": " + e.Message }
func fail(code, format string, args ...any) error { return &Error{code, fmt.Sprintf(format, args...)} }
