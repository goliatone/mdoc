package googleapi

import (
	"context"
	"errors"
	"fmt"
)

type StableDrive interface {
	GetFile(context.Context, string) (File, error)
	ExportDocument(context.Context, string, string) ([]byte, error)
	ListComments(context.Context, string) (CommentStatus, error)
}

type StableDocs interface {
	GetDocument(context.Context, string) (Document, error)
}

type StableReader struct {
	Drive    StableDrive
	Docs     StableDocs
	Attempts int
	Backoff  func(context.Context, int) error
}

func (r StableReader) Read(ctx context.Context, fileID string, expectedTabs []TabTopology) (StableExport, error) {
	if r.Drive == nil || r.Docs == nil {
		return StableExport{}, &Error{Kind: KindAPI, Operation: "read stable Google Markdown export", Cause: errors.New("Drive and Docs clients are required")}
	}
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	backoff := r.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	var lastChange string
	for attempt := 0; attempt < attempts; attempt++ {
		beforeFile, err := r.Drive.GetFile(ctx, fileID)
		if err != nil {
			return StableExport{}, err
		}
		beforeDocument, err := r.Docs.GetDocument(ctx, fileID)
		if err != nil {
			return StableExport{}, err
		}
		if err := validateStableIdentity(fileID, beforeFile, beforeDocument); err != nil {
			return StableExport{}, err
		}
		if err := ValidateSingleRootTopology(beforeDocument.Tabs); err != nil {
			return StableExport{}, err
		}
		if len(expectedTabs) > 0 && !sameTopology(beforeDocument.Tabs, expectedTabs) {
			return StableExport{}, &Error{Kind: KindUnsupported, Operation: "read stable Google Markdown export", Cause: errors.New("Google Docs tab topology changed from the captured baseline")}
		}
		if beforeDocument.SuggestionsDetected {
			return StableExport{}, &Error{Kind: KindSuggestions, Operation: "read stable Google Markdown export", Cause: errors.New("unresolved suggestions must be accepted or rejected before pull")}
		}
		content, err := r.Drive.ExportDocument(ctx, fileID, MarkdownMimeType)
		if err != nil {
			return StableExport{}, err
		}
		afterFile, err := r.Drive.GetFile(ctx, fileID)
		if err != nil {
			return StableExport{}, err
		}
		afterDocument, err := r.Docs.GetDocument(ctx, fileID)
		if err != nil {
			return StableExport{}, err
		}
		if err := validateStableIdentity(fileID, afterFile, afterDocument); err != nil {
			return StableExport{}, err
		}
		if err := ValidateSingleRootTopology(afterDocument.Tabs); err != nil {
			return StableExport{}, err
		}
		if !sameTopology(beforeDocument.Tabs, afterDocument.Tabs) {
			return StableExport{}, &Error{Kind: KindUnsupported, Operation: "read stable Google Markdown export", Cause: errors.New("Google Docs tab topology changed during export")}
		}
		if len(expectedTabs) > 0 && !sameTopology(afterDocument.Tabs, expectedTabs) {
			return StableExport{}, &Error{Kind: KindUnsupported, Operation: "read stable Google Markdown export", Cause: errors.New("Google Docs tab topology changed from the captured baseline")}
		}
		if afterDocument.SuggestionsDetected {
			return StableExport{}, &Error{Kind: KindSuggestions, Operation: "read stable Google Markdown export", Cause: errors.New("unresolved suggestions must be accepted or rejected before pull")}
		}
		if beforeFile.Version != afterFile.Version || beforeDocument.RevisionID != afterDocument.RevisionID {
			lastChange = fmt.Sprintf("Drive version %s to %s, Docs revision %s to %s", beforeFile.Version, afterFile.Version, beforeDocument.RevisionID, afterDocument.RevisionID)
			if attempt < attempts-1 {
				if err := backoff(ctx, attempt); err != nil {
					return StableExport{}, err
				}
				continue
			}
			break
		}
		comments, err := r.Drive.ListComments(ctx, fileID)
		if err != nil {
			return StableExport{}, err
		}
		return StableExport{File: afterFile, Document: afterDocument, Content: content, Comments: comments}, nil
	}
	return StableExport{}, &Error{Kind: KindUnstable, Operation: "read stable Google Markdown export", Cause: fmt.Errorf("document changed during %d attempts: %s", attempts, lastChange)}
}

func ValidateSingleRootTopology(tabs []TabTopology) error {
	if len(tabs) != 1 {
		return &Error{Kind: KindUnsupported, Operation: "validate Google Docs tabs", Cause: fmt.Errorf("expected one root tab with no children, found %d tabs", len(tabs))}
	}
	tab := tabs[0]
	if tab.ID == "" || tab.ParentID != "" || tab.Index != 0 {
		return &Error{Kind: KindUnsupported, Operation: "validate Google Docs tabs", Cause: errors.New("the only supported tab must have an ID, root position, and index zero")}
	}
	return nil
}

func validateStableIdentity(fileID string, file File, document Document) error {
	if fileID == "" || file.ID != fileID || document.ID != fileID {
		return &Error{Kind: KindConflict, Operation: "read stable Google Markdown export", Cause: errors.New("Drive and Docs file identity does not match the requested target")}
	}
	if file.MimeType != DocumentMimeType || file.Trashed {
		return &Error{Kind: KindConflict, Operation: "read stable Google Markdown export", Cause: errors.New("target is not an active native Google Doc")}
	}
	if file.Version == "" || document.RevisionID == "" {
		return &Error{Kind: KindConflict, Operation: "read stable Google Markdown export", Cause: errors.New("Drive version and Docs revision are required")}
	}
	return nil
}

func sameTopology(left, right []TabTopology) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
