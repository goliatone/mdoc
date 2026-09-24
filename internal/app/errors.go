package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type ErrorClass string

const (
	ClassCommand        ErrorClass = "command"
	ClassValidation     ErrorClass = "validation"
	ClassAuthentication ErrorClass = "authentication"
	ClassConflict       ErrorClass = "conflict"
	ClassConversion     ErrorClass = "conversion"
	ClassGoogleAPI      ErrorClass = "google_api"
	ClassPartial        ErrorClass = "partial"
)

type Error struct {
	Class    ErrorClass
	Code     string
	Message  string
	Cause    error
	Recovery *Recovery
}

type Recovery struct {
	Kind        string            `json:"kind"`
	OperationID string            `json:"operation_id"`
	ReviewSetID string            `json:"review_set_id,omitempty"`
	FileIDs     map[string]string `json:"file_ids,omitempty"`
	JournalPath string            `json:"journal_path"`
	NextCommand string            `json:"next_command"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Class)
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(class ErrorClass, code, message string) error {
	return &Error{Class: class, Code: code, Message: message}
}

func WrapError(class ErrorClass, code, message string, cause error) error {
	return &Error{Class: class, Code: code, Message: message, Cause: cause}
}

func WithRecovery(err error, code string, recovery Recovery) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) && existing.Recovery != nil {
		return err
	}
	fileIDs := "none"
	if len(recovery.FileIDs) > 0 {
		keys := make([]string, 0, len(recovery.FileIDs))
		for key := range recovery.FileIDs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", key, recovery.FileIDs[key]))
		}
		fileIDs = strings.Join(parts, ",")
	}
	review := ""
	if recovery.ReviewSetID != "" {
		review = fmt.Sprintf(" review_set_id=%s", recovery.ReviewSetID)
	}
	message := fmt.Sprintf("%v; recovery: operation_id=%s%s file_ids=%s journal=%s; next: %s", err, recovery.OperationID, review, fileIDs, recovery.JournalPath, recovery.NextCommand)
	return &Error{Class: ClassPartial, Code: code, Message: message, Cause: err, Recovery: &recovery}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var typed *Error
	if !errors.As(err, &typed) {
		return 2
	}
	switch typed.Class {
	case ClassValidation:
		return 3
	case ClassAuthentication:
		return 4
	case ClassConflict:
		return 5
	case ClassConversion:
		return 6
	case ClassGoogleAPI:
		return 7
	case ClassPartial:
		return 8
	default:
		return 2
	}
}

func Unavailable(command string) error {
	return NewError(ClassCommand, "command_not_ready", fmt.Sprintf("%s is not available in this build", command))
}
