package googleapi

import (
	"context"
	"io"
	"time"
)

const (
	FolderMimeType   = "application/vnd.google-apps.folder"
	DocumentMimeType = "application/vnd.google-apps.document"
	DOCXMimeType     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MarkdownMimeType = "text/markdown"
	MaxExportBytes   = int64(10 * 1024 * 1024)
)

type FolderSpec struct {
	WorkspaceID     string
	Profile         string
	Account         string
	ParentID        string
	StagingID       string
	ReviewID        string
	StoredStagingID string
	StoredReviewID  string
	StagingName     string
	ReviewName      string
}

type Folder struct {
	ID             string
	Name           string
	Role           string
	MimeType       string
	Parents        []string
	DriveID        string
	Trashed        bool
	OwnerEmails    []string
	CanAddChildren bool
	CanEdit        bool
	Resolution     string
	AppProperties  map[string]string
}

type FolderPair struct {
	Staging Folder
	Review  Folder
}

type File struct {
	ID            string
	Name          string
	URL           string
	MimeType      string
	Version       string
	ModifiedTime  time.Time
	Parents       []string
	Trashed       bool
	AppProperties map[string]string
}

type CreateDocumentInput struct {
	Name          string
	ParentID      string
	Content       io.Reader
	AppProperties map[string]string
}

type CommentStatus struct {
	Unresolved int
	Pages      int
}

type LinkUpdate struct {
	StartIndex int64
	EndIndex   int64
	URL        string
}

type Document struct {
	ID                  string
	RevisionID          string
	SuggestionsDetected bool
	Links               map[string][]TextRange
	InternalLinkCount   int
	HeadingCount        int
	InlineObjectCount   int
	TableCount          int
	SectionBreakCount   int
	Tabs                []TabTopology
}

type TabTopology struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Index    int64  `json:"index"`
	ParentID string `json:"parent_id,omitempty"`
}

type StableExport struct {
	File     File
	Document Document
	Content  []byte
	Comments CommentStatus
}

type TextRange struct {
	StartIndex int64
	EndIndex   int64
}

type Drive interface {
	EnsureFolders(context.Context, FolderSpec) (FolderPair, error)
	FindFolders(context.Context, string, string, string) ([]Folder, error)
	CreateFolder(context.Context, string, string, string, string) (Folder, error)
	CreateDocument(context.Context, CreateDocumentInput) (File, error)
	FindDocuments(context.Context, map[string]string) ([]File, error)
	GetFile(context.Context, string) (File, error)
	UpdateProperties(context.Context, string, map[string]string) (File, error)
	MoveFile(context.Context, string, string, string) (File, error)
	ListComments(context.Context, string) (CommentStatus, error)
	ExportDocument(context.Context, string, string) ([]byte, error)
}

type Docs interface {
	GetDocument(context.Context, string) (Document, error)
	UpdateLinks(context.Context, string, string, []LinkUpdate) (Document, error)
}
