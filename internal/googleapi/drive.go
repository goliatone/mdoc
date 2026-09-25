package googleapi

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
	gapi "google.golang.org/api/googleapi"
)

const fileFields = "id,name,mimeType,webViewLink,version,modifiedTime,parents,driveId,trashed,appProperties,owners(emailAddress),capabilities(canAddChildren,canEdit)"

type DriveClient struct{ service *drive.Service }

func NewDriveClient(service *drive.Service) *DriveClient { return &DriveClient{service: service} }

func (c *DriveClient) EnsureFolders(ctx context.Context, spec FolderSpec) (FolderPair, error) {
	stagingMatches, err := c.FindFolders(ctx, spec.WorkspaceID, spec.Profile, "staging")
	if err != nil {
		return FolderPair{}, err
	}
	reviewMatches, err := c.FindFolders(ctx, spec.WorkspaceID, spec.Profile, "review")
	if err != nil {
		return FolderPair{}, err
	}
	if len(stagingMatches) > 1 {
		return FolderPair{}, conflict("find staging folder", fmt.Sprintf("remote metadata is ambiguous: found %d matches", len(stagingMatches)))
	}
	if len(reviewMatches) > 1 {
		return FolderPair{}, conflict("find review folder", fmt.Sprintf("remote metadata is ambiguous: found %d matches", len(reviewMatches)))
	}
	if spec.StagingID != "" && spec.StoredStagingID != "" && spec.StagingID != spec.StoredStagingID {
		return FolderPair{}, conflict("preflight staging folder", "configured and stored folder IDs differ")
	}
	if spec.ReviewID != "" && spec.StoredReviewID != "" && spec.ReviewID != spec.StoredReviewID {
		return FolderPair{}, conflict("preflight review folder", "configured and stored folder IDs differ")
	}
	if spec.ParentID != "" {
		parent, err := c.getFolder(ctx, spec.ParentID)
		if err != nil {
			return FolderPair{}, err
		}
		if err := validateDestinationFolder(parent, spec.Account, "parent", "", false); err != nil {
			return FolderPair{}, err
		}
	}
	staging, stagingAction, err := c.preflightRole(ctx, spec, "staging", firstID(spec.StagingID, spec.StoredStagingID), stagingMatches)
	if err != nil {
		return FolderPair{}, err
	}
	review, reviewAction, err := c.preflightRole(ctx, spec, "review", firstID(spec.ReviewID, spec.StoredReviewID), reviewMatches)
	if err != nil {
		return FolderPair{}, err
	}
	if staging.ID != "" && staging.ID == review.ID {
		return FolderPair{}, conflict("preflight destination folders", "staging and review resolve to the same folder")
	}
	if spec.ParentID != "" && (staging.ID == spec.ParentID || review.ID == spec.ParentID) {
		return FolderPair{}, conflict("preflight destination folders", "a role folder cannot also be the parent folder")
	}
	staging, err = c.applyFolderAction(ctx, spec, "staging", spec.StagingName, staging, stagingAction)
	if err != nil {
		return FolderPair{}, err
	}
	review, err = c.applyFolderAction(ctx, spec, "review", spec.ReviewName, review, reviewAction)
	if err != nil {
		return FolderPair{}, err
	}
	return FolderPair{Staging: staging, Review: review}, nil
}

func (c *DriveClient) preflightRole(ctx context.Context, spec FolderSpec, role, requestedID string, matches []Folder) (Folder, string, error) {
	var folder Folder
	if requestedID != "" {
		var err error
		folder, err = c.getFolder(ctx, requestedID)
		if err != nil {
			return Folder{}, "", err
		}
		if len(matches) == 1 && matches[0].ID != requestedID {
			return Folder{}, "", conflict("preflight "+role+" folder", "configured folder conflicts with existing app metadata")
		}
	} else if len(matches) == 1 {
		folder = matches[0]
	} else {
		return Folder{}, "create", nil
	}
	if err := validateDestinationFolder(folder, spec.Account, role, spec.ParentID, true); err != nil {
		return Folder{}, "", err
	}
	expected := folderProperties(spec.WorkspaceID, spec.Profile, role)
	metadataCount := 0
	for key, value := range folder.AppProperties {
		if !strings.HasPrefix(key, "mdoc_") {
			continue
		}
		metadataCount++
		if expected[key] != value {
			return Folder{}, "", conflict("preflight "+role+" folder", fmt.Sprintf("folder has conflicting %s metadata", key))
		}
	}
	if metadataCount == 0 {
		return folder, "adopt", nil
	}
	if metadataCount != len(expected) {
		return Folder{}, "", conflict("preflight "+role+" folder", "folder has partial mdoc metadata")
	}
	return folder, "reuse", nil
}

func (c *DriveClient) applyFolderAction(ctx context.Context, spec FolderSpec, role, name string, folder Folder, action string) (Folder, error) {
	properties := folderProperties(spec.WorkspaceID, spec.Profile, role)
	switch action {
	case "reuse":
		folder.Resolution = "reused"
		return folder, nil
	case "adopt":
		updated, err := retryValue(ctx, "adopt "+role+" folder", func() (*drive.File, error) {
			return c.service.Files.Update(folder.ID, &drive.File{AppProperties: properties}).Fields(fileFields).Context(ctx).Do()
		})
		if err != nil {
			return Folder{}, classify("adopt "+role+" folder", err)
		}
		result := folderFromDrive(updated)
		result.Resolution = "adopted"
		return result, nil
	case "create":
		created, err := c.createFolderReconciled(ctx, spec, role, name)
		if err != nil {
			return Folder{}, err
		}
		created.Resolution = "created"
		return created, nil
	default:
		return Folder{}, conflict("resolve "+role+" folder", "unknown folder action")
	}
}

func (c *DriveClient) createFolderReconciled(ctx context.Context, spec FolderSpec, role, name string) (Folder, error) {
	var result Folder
	err := (RetryPolicy{}).Do(ctx, func() error {
		matches, err := c.FindFolders(ctx, spec.WorkspaceID, spec.Profile, role)
		if err != nil {
			return err
		}
		switch len(matches) {
		case 0:
			created, err := c.createFolder(ctx, spec.WorkspaceID, spec.Profile, role, name, spec.ParentID)
			if err != nil {
				return err
			}
			result = created
			return nil
		case 1:
			if err := validateDestinationFolder(matches[0], spec.Account, role, spec.ParentID, true); err != nil {
				return err
			}
			result = matches[0]
			return nil
		default:
			return conflict("reconcile "+role+" folder create", fmt.Sprintf("found %d matches", len(matches)))
		}
	})
	return result, err
}

func (c *DriveClient) FindFolders(ctx context.Context, workspaceID, profile, role string) ([]Folder, error) {
	query := strings.Join([]string{
		"mimeType = '" + FolderMimeType + "'",
		"trashed = false",
		propertyQuery("mdoc_workspace", workspaceID),
		propertyQuery("mdoc_profile", profile),
		propertyQuery("mdoc_role", role),
	}, " and ")
	result := []Folder{}
	pageToken := ""
	for {
		call := c.service.Files.List().Q(query).Spaces("drive").Fields("nextPageToken,files(" + fileFields + ")").PageSize(100).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		response, err := retryValue(ctx, "find app folder", func() (*drive.FileList, error) { return call.Do() })
		if err != nil {
			return nil, err
		}
		for _, file := range response.Files {
			result = append(result, folderFromDrive(file))
		}
		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}
	return result, nil
}

func (c *DriveClient) CreateFolder(ctx context.Context, workspaceID, profile, role, name string) (Folder, error) {
	return c.createFolder(ctx, workspaceID, profile, role, name, "")
}

func (c *DriveClient) createFolder(ctx context.Context, workspaceID, profile, role, name, parentID string) (Folder, error) {
	metadata := &drive.File{Name: name, MimeType: FolderMimeType, AppProperties: folderProperties(workspaceID, profile, role)}
	if parentID != "" {
		metadata.Parents = []string{parentID}
	}
	created, err := c.service.Files.Create(metadata).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return Folder{}, classify("create "+role+" folder", err)
	}
	return folderFromDrive(created), nil
}

func (c *DriveClient) CreateDocument(ctx context.Context, input CreateDocumentInput) (File, error) {
	metadata := &drive.File{Name: input.Name, MimeType: DocumentMimeType, Parents: []string{input.ParentID}, AppProperties: input.AppProperties}
	created, err := c.service.Files.Create(metadata).Media(input.Content, gapi.ContentType(DOCXMimeType)).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return File{}, classify("create Google Doc", err)
	}
	return fileFromDrive(created), nil
}

func (c *DriveClient) FindDocuments(ctx context.Context, properties map[string]string) ([]File, error) {
	clauses := []string{"mimeType = '" + DocumentMimeType + "'", "trashed = false"}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		clauses = append(clauses, propertyQuery(key, properties[key]))
	}
	result := []File{}
	pageToken := ""
	for {
		call := c.service.Files.List().Q(strings.Join(clauses, " and ")).Spaces("drive").Fields("nextPageToken,files(" + fileFields + ")").PageSize(100).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		response, err := retryValue(ctx, "find Google Docs", func() (*drive.FileList, error) { return call.Do() })
		if err != nil {
			return nil, err
		}
		for _, file := range response.Files {
			result = append(result, fileFromDrive(file))
		}
		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}
	return result, nil
}

func (c *DriveClient) GetFile(ctx context.Context, fileID string) (File, error) {
	call := c.service.Files.Get(fileID).Fields(fileFields).Context(ctx)
	file, err := retryValue(ctx, "read Google file metadata", func() (*drive.File, error) { return call.Do() })
	if err != nil {
		return File{}, err
	}
	return fileFromDrive(file), nil
}

func (c *DriveClient) UpdateProperties(ctx context.Context, fileID string, properties map[string]string) (File, error) {
	file, err := c.service.Files.Update(fileID, &drive.File{AppProperties: properties}).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return File{}, classify("update Google file metadata", err)
	}
	return fileFromDrive(file), nil
}

func (c *DriveClient) MoveFile(ctx context.Context, fileID, addParent, removeParent string) (File, error) {
	file, err := c.service.Files.Update(fileID, &drive.File{}).AddParents(addParent).RemoveParents(removeParent).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return File{}, classify("move Google file", err)
	}
	return fileFromDrive(file), nil
}

func (c *DriveClient) ListComments(ctx context.Context, fileID string) (CommentStatus, error) {
	result := CommentStatus{}
	pageToken := ""
	for {
		call := c.service.Comments.List(fileID).Fields("nextPageToken,comments(id,resolved,deleted)").PageSize(100).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		response, err := retryValue(ctx, "read Google file comments", func() (*drive.CommentList, error) { return call.Do() })
		if err != nil {
			return result, err
		}
		result.Pages++
		for _, comment := range response.Comments {
			if !comment.Deleted && !comment.Resolved {
				result.Unresolved++
			}
		}
		if response.NextPageToken == "" {
			return result, nil
		}
		pageToken = response.NextPageToken
	}
}

func (c *DriveClient) ExportDocument(ctx context.Context, fileID, mimeType string) ([]byte, error) {
	if strings.TrimSpace(fileID) == "" {
		return nil, &Error{Kind: KindConflict, Operation: "export Google Doc", Cause: fmt.Errorf("file ID is required")}
	}
	if mimeType != MarkdownMimeType {
		return nil, &Error{Kind: KindUnsupported, Operation: "export Google Doc", Cause: fmt.Errorf("MIME type %q is unsupported", mimeType)}
	}
	response, err := c.service.Files.Export(fileID, mimeType).Context(ctx).Download()
	if err != nil {
		return nil, classify("export Google Doc as Markdown", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, MaxExportBytes+1))
	if err != nil {
		return nil, classify("read Google Markdown export", err)
	}
	if int64(len(content)) > MaxExportBytes {
		return nil, &Error{Kind: KindExportSize, Operation: "read Google Markdown export", Cause: fmt.Errorf("export exceeds the 10 MB Drive limit")}
	}
	if len(content) == 0 {
		return nil, &Error{Kind: KindEmptyExport, Operation: "read Google Markdown export", Cause: fmt.Errorf("export is empty")}
	}
	return content, nil
}

func propertyQuery(key, value string) string {
	escape := func(input string) string {
		return strings.ReplaceAll(strings.ReplaceAll(input, "\\", "\\\\"), "'", "\\'")
	}
	return fmt.Sprintf("appProperties has { key='%s' and value='%s' }", escape(key), escape(value))
}

func (c *DriveClient) getFolder(ctx context.Context, folderID string) (Folder, error) {
	call := c.service.Files.Get(folderID).Fields(fileFields).Context(ctx)
	file, err := retryValue(ctx, "read destination folder", func() (*drive.File, error) { return call.Do() })
	if err != nil {
		return Folder{}, err
	}
	return folderFromDrive(file), nil
}

func validateDestinationFolder(folder Folder, account, role, parentID string, requireEdit bool) error {
	operation := "preflight " + role + " folder"
	if folder.MimeType != FolderMimeType {
		return conflict(operation, "destination is not a folder")
	}
	if folder.Trashed {
		return conflict(operation, "destination folder is trashed")
	}
	if folder.DriveID != "" {
		return conflict(operation, "Shared Drive destinations are unsupported")
	}
	if !folder.CanAddChildren {
		return conflict(operation, "folder cannot add children")
	}
	if requireEdit && !folder.CanEdit {
		return conflict(operation, "folder cannot be edited")
	}
	if parentID != "" && !containsString(folder.Parents, parentID) {
		return conflict(operation, "folder is not under the configured parent")
	}
	if account != "" && len(folder.OwnerEmails) > 0 && !containsFold(folder.OwnerEmails, account) {
		return conflict(operation, "folder is owned by a different Google account")
	}
	return nil
}

// ValidateDestinationFolder applies the same My Drive safety checks used by setup.
func ValidateDestinationFolder(folder Folder, account, role, parentID string, requireEdit bool) error {
	return validateDestinationFolder(folder, account, role, parentID, requireEdit)
}

func folderProperties(workspaceID, profile, role string) map[string]string {
	return map[string]string{"mdoc_workspace": workspaceID, "mdoc_profile": profile, "mdoc_role": role, "mdoc_kind": "folder"}
}

func firstID(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func conflict(operation, message string) error {
	return &Error{Kind: KindConflict, Operation: operation, Cause: fmt.Errorf("%s", message)}
}

func containsString(values []string, expected string) bool {
	return slices.Contains(values, expected)
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(expected)) {
			return true
		}
	}
	return false
}

func folderFromDrive(file *drive.File) Folder {
	owners := make([]string, 0, len(file.Owners))
	for _, owner := range file.Owners {
		if owner.EmailAddress != "" {
			owners = append(owners, owner.EmailAddress)
		}
	}
	result := Folder{
		ID:            file.Id,
		Name:          file.Name,
		Role:          file.AppProperties["mdoc_role"],
		MimeType:      file.MimeType,
		Parents:       append([]string(nil), file.Parents...),
		DriveID:       file.DriveId,
		Trashed:       file.Trashed,
		OwnerEmails:   owners,
		AppProperties: cloneMap(file.AppProperties),
	}
	if file.Capabilities != nil {
		result.CanAddChildren = file.Capabilities.CanAddChildren
		result.CanEdit = file.Capabilities.CanEdit
	}
	return result
}

func fileFromDrive(file *drive.File) File {
	modified, _ := time.Parse(time.RFC3339Nano, file.ModifiedTime)
	return File{ID: file.Id, Name: file.Name, URL: file.WebViewLink, MimeType: file.MimeType, Version: strconv.FormatInt(file.Version, 10), ModifiedTime: modified, Parents: append([]string(nil), file.Parents...), Trashed: file.Trashed, AppProperties: cloneMap(file.AppProperties)}
}

func cloneMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	maps.Copy(result, input)
	return result
}
