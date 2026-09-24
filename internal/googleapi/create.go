package googleapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/goliatone/mdoc/internal/sourcekey"
)

type PublishMetadata struct {
	WorkspaceID     string
	Profile         string
	SourceKey       string
	TargetKey       string
	PublicationID   string
	PublicationKind string
	ReviewSetID     string
	Generation      int
	OperationID     string
	ExpectedSetSize int
	PublishStatus   string
}

type NativeDocument struct {
	File
	DocsRevision string
}

type Creator struct {
	Drive    Drive
	Docs     Docs
	Backoff  func(context.Context, int) error
	Attempts int
}

func (c Creator) Create(ctx context.Context, name, parentID string, docx []byte, metadata PublishMetadata) (NativeDocument, error) {
	if err := metadata.Validate(); err != nil {
		return NativeDocument{}, err
	}
	properties := metadata.Properties()
	matched, err := c.reconcile(ctx, parentID, properties)
	if err != nil {
		return NativeDocument{}, err
	}
	if matched != nil {
		return c.hydrate(ctx, *matched)
	}
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	backoff := c.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		created, createErr := c.Drive.CreateDocument(ctx, CreateDocumentInput{Name: name, ParentID: parentID, Content: bytes.NewReader(docx), AppProperties: properties})
		if createErr == nil {
			return c.hydrate(ctx, created)
		}
		last = createErr
		matched, reconcileErr := c.reconcile(ctx, parentID, properties)
		if reconcileErr != nil {
			return NativeDocument{}, reconcileErr
		}
		if matched != nil {
			return c.hydrate(ctx, *matched)
		}
		if !retryable(createErr) || attempt == attempts-1 {
			break
		}
		if err := backoff(ctx, attempt); err != nil {
			return NativeDocument{}, err
		}
	}
	return NativeDocument{}, last
}

func (c Creator) reconcile(ctx context.Context, parentID string, properties map[string]string) (*File, error) {
	identityProperty := "mdoc_source"
	if properties["mdoc_target_key"] != "" {
		identityProperty = "mdoc_target_key"
	}
	matches, err := c.Drive.FindDocuments(ctx, map[string]string{"mdoc_operation": properties["mdoc_operation"], identityProperty: properties[identityProperty]})
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		match := matches[0]
		if match.MimeType != DocumentMimeType || match.Trashed || len(match.Parents) != 1 || match.Parents[0] != parentID || !sameProperties(match.AppProperties, properties) {
			return nil, &Error{Kind: KindConflict, Operation: "reconcile Google Doc create", Cause: errors.New("the matching file has unexpected metadata or location")}
		}
		return &matches[0], nil
	default:
		return nil, &Error{Kind: KindConflict, Operation: "reconcile Google Doc create", Cause: fmt.Errorf("found %d matches for operation %s and target %s", len(matches), properties["mdoc_operation"], properties[identityProperty])}
	}
}

func sameProperties(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func (c Creator) hydrate(ctx context.Context, file File) (NativeDocument, error) {
	metadata, err := c.Drive.GetFile(ctx, file.ID)
	if err != nil {
		return NativeDocument{}, err
	}
	document, err := c.Docs.GetDocument(ctx, file.ID)
	if err != nil {
		return NativeDocument{}, err
	}
	return NativeDocument{File: metadata, DocsRevision: document.RevisionID}, nil
}

func (m PublishMetadata) Validate() error {
	if m.WorkspaceID == "" || m.Profile == "" || m.ReviewSetID == "" || m.Generation < 1 || m.OperationID == "" || m.ExpectedSetSize < 1 {
		return errors.New("publish metadata is incomplete")
	}
	if m.TargetKey == "" {
		normalized, err := sourcekey.Normalize(m.SourceKey)
		if err != nil || normalized != m.SourceKey {
			return errors.New("publish source key must be normalized and workspace relative")
		}
	} else {
		if m.TargetKey != "publication:"+m.PublicationID || m.PublicationID == "" || m.PublicationKind != "source" && m.PublicationKind != "bundle" {
			return errors.New("explicit publication metadata is incomplete")
		}
		if m.PublicationKind == "source" {
			normalized, err := sourcekey.Normalize(m.SourceKey)
			if err != nil || normalized != m.SourceKey {
				return errors.New("explicit source publication input must be normalized and workspace relative")
			}
		} else if m.SourceKey != "" {
			return errors.New("bundle publication metadata cannot have a source input")
		}
	}
	if m.PublishStatus == "" {
		return errors.New("publish status is required")
	}
	return nil
}

func (m PublishMetadata) Properties() map[string]string {
	properties := map[string]string{
		"mdoc_workspace":         m.WorkspaceID,
		"mdoc_profile":           m.Profile,
		"mdoc_review_set":        m.ReviewSetID,
		"mdoc_generation":        strconv.Itoa(m.Generation),
		"mdoc_operation":         m.OperationID,
		"mdoc_expected_set_size": strconv.Itoa(m.ExpectedSetSize),
		"mdoc_status":            m.PublishStatus,
	}
	if m.SourceKey != "" {
		properties["mdoc_source"] = m.SourceKey
	}
	if m.TargetKey != "" {
		properties["mdoc_target_key"] = m.TargetKey
		properties["mdoc_publication"] = m.PublicationID
		properties["mdoc_publication_kind"] = m.PublicationKind
	}
	return properties
}

func retryable(err error) bool {
	return IsRetryable(err)
}

func defaultBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(1<<attempt) * 250 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
