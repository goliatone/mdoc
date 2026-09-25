package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/publication"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

func (a *Application) ReviewPull(ctx context.Context, options ReviewPullOptions) error {
	return a.reviewPull(ctx, options, "", "", false)
}

func (a *Application) ReviewBootstrap(ctx context.Context, options ReviewBootstrapOptions) error {
	documentID, err := reviewDocumentID(options.Document)
	if err != nil {
		return NewError(ClassValidation, string(ReviewCodeTargetMismatch), err.Error())
	}
	return a.reviewPull(ctx, options.ReviewPullOptions, documentID, options.ReviewExport, options.Partial)
}

func (a *Application) reviewPull(ctx context.Context, options ReviewPullOptions, externalDocumentID, reviewExport string, partial bool) error {
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return err
	}
	publicationID, _, err := SelectReviewPublication(profile, options.Publication)
	if err != nil {
		return err
	}
	if store == nil {
		return NewError(ClassValidation, string(ReviewCodePublicationInvalid), "review pull requires project state")
	}
	if err := store.Acquire(); err != nil {
		return WrapError(ClassConflict, "state_locked", err.Error(), err)
	}
	defer store.Release()
	if journal, journalErr := store.LoadJournal(); journalErr == nil {
		return NewError(ClassConflict, string(ReviewCodeConflict), fmt.Sprintf("unfinished publish operation %s must be resumed before review pull", journal.OperationID))
	} else if !errors.Is(journalErr, state.ErrNotFound) {
		return WrapError(ClassPartial, string(ReviewCodeSnapshotInvalid), journalErr.Error(), journalErr)
	}
	if remap, remapErr := store.LoadRemapJournal(); remapErr == nil {
		return NewError(ClassConflict, string(ReviewCodeConflict), fmt.Sprintf("unfinished source remap %s to %s must be resumed before review pull", remap.From, remap.To))
	} else if !errors.Is(remapErr, state.ErrNotFound) {
		return WrapError(ClassPartial, string(ReviewCodeSnapshotInvalid), remapErr.Error(), remapErr)
	}
	publishState, err := store.Load()
	if err != nil {
		return WrapError(ClassPartial, string(ReviewCodeSnapshotInvalid), err.Error(), err)
	}
	if publishState.WorkspaceID != cfg.WorkspaceID || publishState.Profile != profile.Name {
		return NewError(ClassConflict, string(ReviewCodeTargetMismatch), "publish state does not match the selected workspace and profile")
	}
	if err := a.verifyStoredAccount(ctx, publishState); err != nil {
		return err
	}
	graph, err := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
	if err != nil {
		return WrapError(ClassValidation, string(ReviewCodePublicationInvalid), err.Error(), err)
	}
	publications := publication.Resolve(cfg.Version, profile, graph)
	if graph.HasErrors() || hasPublicationErrors(publications.Issues) {
		return NewError(ClassValidation, string(ReviewCodePublicationInvalid), "publication sources are not valid for review pull")
	}
	target := publications.ByID[publicationID]
	if target == nil {
		return NewError(ClassValidation, string(ReviewCodePublicationInvalid), fmt.Sprintf("publication %q could not be resolved", publicationID))
	}
	targetKey := string(target.Target)
	stored := publishState.Targets[targetKey]
	if stored.ActiveTarget == nil {
		return NewError(ClassConflict, string(ReviewCodeBaselineMissing), "the publication has no active review target; publish a new generation")
	}
	active := *stored.ActiveTarget
	if active.ReviewSnapshot == nil {
		return NewError(ClassConflict, string(ReviewCodeBaselineMissing), "the active target has no review baseline; publish a new generation with review pull enabled")
	}
	snapshotStore, err := reviewsync.NewSnapshotStore(store.Path())
	if err != nil {
		return WrapError(ClassPartial, string(ReviewCodeSnapshotInvalid), err.Error(), err)
	}
	snapshot, err := snapshotStore.Load(targetKey, active.Generation, true)
	if err != nil {
		code := ReviewCodeSnapshotInvalid
		if errors.Is(err, reviewsync.ErrSnapshotNotFound) {
			code = ReviewCodeBaselineMissing
		}
		return WrapError(ClassConflict, string(code), err.Error(), err)
	}
	reviewFileID := active.FileID
	reportKind := "review_pull"
	baselineFileID := ""
	reviewSource := "google_docs"
	remoteVerified := true
	var clients googleapi.Clients
	if reviewExport == "" {
		if a.Auth == nil {
			return NewError(ClassAuthentication, "auth_unavailable", "Google authorization is unavailable; run `mdoc auth login`")
		}
		client, clientErr := a.Auth.Client(ctx)
		if clientErr != nil {
			return WrapError(ClassAuthentication, "auth_client_failed", clientErr.Error(), clientErr)
		}
		clients, err = a.GoogleFactory.New(ctx, client)
		if err != nil {
			return googleError(err)
		}
	}
	if externalDocumentID == "" {
		remoteFile, preflightErr := reviewTargetPreflight(ctx, clients.Drive, cfg, profile, publishState, stored, active, targetKey)
		if preflightErr != nil {
			return preflightErr
		}
		if err := validateActiveSnapshot(snapshot, stored, active, *active.ReviewSnapshot, cfg.WorkspaceID, profile.Name, publishState.Folders.ReviewID, remoteFile.AppProperties); err != nil {
			return WrapError(ClassConflict, string(ReviewCodeSnapshotInvalid), err.Error(), err)
		}
	} else {
		if err := validateSnapshotReference(snapshot, stored, active, *active.ReviewSnapshot, cfg.WorkspaceID, profile.Name, publishState.Folders.ReviewID); err != nil {
			return WrapError(ClassConflict, string(ReviewCodeSnapshotInvalid), err.Error(), err)
		}
		reviewFileID = externalDocumentID
		reportKind = "review_bootstrap"
		baselineFileID = active.FileID
	}
	var stable googleapi.StableExport
	if reviewExport != "" {
		content, readErr := readLocalReviewExport(reviewExport)
		if readErr != nil {
			return NewError(ClassValidation, string(ReviewCodePublicationInvalid), readErr.Error())
		}
		stable = googleapi.StableExport{File: googleapi.File{ID: reviewFileID, MimeType: googleapi.DocumentMimeType}, Content: content}
		reviewSource = "local_markdown_export"
		remoteVerified = false
	} else {
		stable, err = (googleapi.StableReader{Drive: clients.Drive, Docs: clients.Docs}).Read(ctx, reviewFileID, snapshot.Manifest.Capture.Tabs)
		if err != nil {
			return reviewReadError(err)
		}
	}
	if externalDocumentID == "" {
		if err := validateReviewTargetFile(stable.File, cfg, profile, publishState, stored, active, targetKey); err != nil {
			return NewError(ClassConflict, string(ReviewCodeTargetMismatch), "stable export target identity changed after preflight")
		}
	}
	classifyReview := reviewsync.ParseAndClassifyReview
	if reviewExport != "" {
		classifyReview = reviewsync.ParseAndClassifyPortableReview
	}
	classification, err := classifyReview(ctx, reviewsync.PandocParser{Binary: cfg.PandocBinary}, snapshot.Manifest.Reader, snapshot.Manifest.SourceMap, stable.Content)
	if err != nil {
		return WrapError(ClassConflict, string(ReviewCodeConflict), err.Error(), err)
	}
	current, sourcePaths, err := currentReviewSources(target, snapshot.Manifest.Members)
	if err != nil {
		return WrapError(ClassConflict, string(ReviewCodeConflict), err.Error(), err)
	}
	proposals, err := reviewsync.BuildProposals(snapshot, classification, current)
	if err != nil {
		return WrapError(ClassConflict, string(ReviewCodeConflict), err.Error(), err)
	}
	patch, err := reviewsync.BuildPatchContext(ctx, proposals.Files)
	if err != nil {
		return WrapError(ClassConflict, string(ReviewCodeConflict), err.Error(), err)
	}
	counts := map[string]int{}
	for _, change := range classification.Changes {
		counts[string(change.Class)]++
	}
	changes := make([]report.ReviewChangeSummary, 0, len(classification.Changes))
	unsupported := []report.ReviewIssueSummary{}
	conflicts := []report.ReviewIssueSummary{}
	unsupportedKeys := map[string]bool{}
	conflictKeys := map[string]bool{}
	for _, change := range classification.Changes {
		changes = append(changes, report.ReviewChangeSummary{Index: change.Index, Class: string(change.Class), Operation: string(change.Operation), Source: change.Member, Reason: change.Reason})
		key := reviewIssueKey(change.Member, change.Reason)
		if change.Class == reviewsync.ChangeUnsupported {
			unsupported = append(unsupported, report.ReviewIssueSummary{Source: change.Member, Reason: change.Reason})
			unsupportedKeys[key] = true
		}
		if change.Class == reviewsync.ChangeConflict {
			conflicts = append(conflicts, report.ReviewIssueSummary{Source: change.Member, Reason: change.Reason})
			conflictKeys[key] = true
		}
	}
	for _, conflict := range proposals.Conflicts {
		key := reviewIssueKey(conflict.SourceKey, conflict.Reason)
		if !unsupportedKeys[key] && !conflictKeys[key] {
			conflicts = append(conflicts, report.ReviewIssueSummary{Source: conflict.SourceKey, Reason: conflict.Reason})
			conflictKeys[key] = true
		}
	}
	resultCode := ReviewCodeNoChanges
	next := []string{}
	if counts[string(reviewsync.ChangeConflict)] > 0 {
		resultCode = ReviewCodeConflict
		next = append(next, "resolve the reported conflicts in Google Docs or local Markdown, then pull again")
	} else if counts[string(reviewsync.ChangeUnsupported)] > 0 {
		resultCode = ReviewCodeUnsupported
		next = append(next, "apply unsupported structural edits in local Markdown and publish a new generation")
	} else if len(proposals.Conflicts) > 0 {
		resultCode = ReviewCodeConflict
		next = append(next, "resolve the reported conflicts in Google Docs or local Markdown, then pull again")
	} else if len(patch) > 0 {
		resultCode = ReviewCodeCleanPatch
		next = append(next, "review and apply the patch to local Markdown")
	} else {
		next = append(next, "no source patch is needed")
	}
	blocked := resultCode == ReviewCodeConflict || resultCode == ReviewCodeUnsupported
	partialPatch := blocked && partial && len(patch) > 0
	if blocked && !partialPatch {
		patch = nil
	}
	if partialPatch {
		next = append([]string{"the partial patch contains only classified safe edits; resolve every reported blocked change separately"}, next...)
	}
	patchOutput := ""
	if options.PatchOutput != "" && len(patch) > 0 {
		patchOutput, err = writeReviewPatch(options.PatchOutput, patch, options.Overwrite, sourcePaths)
		if err != nil {
			return NewError(ClassCommand, string(ReviewCodeConflict), err.Error())
		}
	}
	changedFiles := map[string]bool{}
	for _, file := range proposals.Files {
		changedFiles[file.SourceKey] = file.Changed
	}
	files := make([]report.ReviewFileSummary, 0, len(snapshot.Manifest.Members))
	for _, member := range snapshot.Manifest.Members {
		files = append(files, report.ReviewFileSummary{Source: member.SourceKey, Changed: changedFiles[member.SourceKey]})
	}
	value := report.ReviewPullReport{
		SchemaVersion: 1, Kind: reportKind, Result: string(resultCode), Publication: publicationID, Target: targetKey,
		Account: publishState.Account, BaselineFileID: baselineFileID, FileID: reviewFileID, ReviewSource: reviewSource, RemoteVerified: remoteVerified, PartialPatch: partialPatch, Generation: active.Generation,
		Capture:      report.ReviewRevisionSummary{DriveVersion: snapshot.Manifest.Capture.DriveVersion, DocsRevision: snapshot.Manifest.Capture.DocsRevision},
		Activation:   report.ReviewRevisionSummary{DriveVersion: snapshot.Manifest.Activation.DriveVersion, DocsRevision: snapshot.Manifest.Activation.DocsRevision},
		Review:       report.ReviewRevisionSummary{DriveVersion: stable.File.Version, DocsRevision: stable.Document.RevisionID},
		OpenComments: stable.Comments.Unresolved, Counts: counts, Files: files, Changes: changes, Conflicts: conflicts, Unsupported: unsupported, Patch: string(patch), PatchOutput: patchOutput, NextActions: next,
	}
	if err := report.WriteReviewPull(a.Out, value, cfg.Output.Format == "json", cfg.Output.Quiet); err != nil {
		return err
	}
	switch resultCode {
	case ReviewCodeConflict:
		return NewError(ClassConflict, string(resultCode), "review pull has conflicts; no source files were changed")
	case ReviewCodeUnsupported:
		return NewError(ClassConversion, string(resultCode), "review pull contains unsupported edits; no source files were changed")
	default:
		return nil
	}
}

func readLocalReviewExport(path string) ([]byte, error) {
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("read review export: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("review export must be a regular file, not a symlink")
	}
	if info.Size() == 0 {
		return nil, errors.New("review export is empty")
	}
	if info.Size() > googleapi.MaxExportBytes {
		return nil, fmt.Errorf("review export exceeds the %d byte limit", googleapi.MaxExportBytes)
	}
	file, err := os.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("open review export: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect review export: %w", err)
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("review export changed while it was being opened")
	}
	content, err := io.ReadAll(io.LimitReader(file, googleapi.MaxExportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read review export: %w", err)
	}
	if int64(len(content)) > googleapi.MaxExportBytes {
		return nil, fmt.Errorf("review export exceeds the %d byte limit", googleapi.MaxExportBytes)
	}
	if !utf8.Valid(content) {
		return nil, errors.New("review export is not valid UTF-8 Markdown")
	}
	return content, nil
}

var reviewDocumentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func reviewDocumentID(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if reviewDocumentIDPattern.MatchString(trimmed) {
		return trimmed, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "docs.google.com" || parsed.User != nil {
		return "", errors.New("--document must be a Google Docs document ID or canonical https://docs.google.com/document/d/... URL")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 3 || parts[0] != "document" || parts[1] != "d" {
		return "", errors.New("--document URL must identify a Google Docs document")
	}
	documentID, err := url.PathUnescape(parts[2])
	if err != nil || !reviewDocumentIDPattern.MatchString(documentID) {
		return "", errors.New("--document URL contains an invalid Google Docs document ID")
	}
	return documentID, nil
}

func reviewIssueKey(source, reason string) string {
	return source + "\x00" + reason
}

func reviewTargetPreflight(ctx context.Context, drive googleapi.Drive, cfg *config.Config, profile config.SelectedProfile, publishState *state.State, stored state.Document, active state.Target, targetKey string) (googleapi.File, error) {
	file, err := drive.GetFile(ctx, active.FileID)
	if err != nil {
		return googleapi.File{}, googleError(err)
	}
	if err := validateReviewTargetFile(file, cfg, profile, publishState, stored, active, targetKey); err != nil {
		return googleapi.File{}, NewError(ClassConflict, string(ReviewCodeTargetMismatch), err.Error())
	}
	files, err := drive.FindDocuments(ctx, map[string]string{"mdoc_workspace": cfg.WorkspaceID, "mdoc_profile": profile.Name, "mdoc_target_key": targetKey, "mdoc_generation": strconv.Itoa(active.Generation)})
	if err != nil {
		return googleapi.File{}, googleError(err)
	}
	if len(files) != 1 || files[0].ID != active.FileID {
		return googleapi.File{}, NewError(ClassConflict, string(ReviewCodeTargetMismatch), "active review generation is missing or ambiguous")
	}
	return file, nil
}

func validateReviewTargetFile(file googleapi.File, cfg *config.Config, profile config.SelectedProfile, publishState *state.State, stored state.Document, active state.Target, targetKey string) error {
	properties := file.AppProperties
	generation, generationErr := strconv.Atoi(properties["mdoc_generation"])
	if file.ID != active.FileID || file.MimeType != googleapi.DocumentMimeType || file.Trashed || file.URL != active.URL || !contains(file.Parents, publishState.Folders.ReviewID) || properties["mdoc_workspace"] != cfg.WorkspaceID || properties["mdoc_profile"] != profile.Name || properties["mdoc_target_key"] != targetKey || properties["mdoc_publication"] != stored.PublicationID || properties["mdoc_publication_kind"] != stored.PublicationKind || properties["mdoc_source"] != stored.Source || generationErr != nil || generation != active.Generation || properties["mdoc_review_set"] != active.ReviewSetID || properties["mdoc_operation"] != active.OperationID || properties["mdoc_status"] != "ready" {
		return errors.New("active review target metadata does not match local state")
	}
	return nil
}

func validateActiveSnapshot(snapshot reviewsync.LoadedSnapshot, stored state.Document, active state.Target, reference state.ReviewSnapshotRef, workspace, profile, reviewFolder string, readyMetadata map[string]string) error {
	if err := validateSnapshotReference(snapshot, stored, active, reference, workspace, profile, reviewFolder); err != nil {
		return err
	}
	if !metadataContains(readyMetadata, snapshot.Manifest.Activation.ReadyMetadata) {
		return errors.New("sealed review snapshot does not match active metadata")
	}
	return nil
}

func validateSnapshotReference(snapshot reviewsync.LoadedSnapshot, stored state.Document, active state.Target, reference state.ReviewSnapshotRef, workspace, profile, reviewFolder string) error {
	manifest := snapshot.Manifest
	if reference.Version != manifest.Version || manifest.IntegrityHash != reference.ManifestHash || manifest.BaselineHash != reference.BaselineHash || manifest.Capture.DocsRevision != reference.CapturedRevision || manifest.Activation == nil || manifest.Activation.DocsRevision != reference.ActivatedRevision || manifest.WorkspaceID != workspace || manifest.Profile != profile || manifest.FileID != active.FileID || manifest.Capture.FileID != active.FileID || manifest.Activation.FileID != active.FileID || manifest.Generation != active.Generation || manifest.ReviewSetID != active.ReviewSetID || manifest.OperationID != active.OperationID || manifest.TargetKey != stored.TargetKey || manifest.PublicationID != stored.PublicationID || manifest.PublicationKind != stored.PublicationKind || manifest.Activation.ReviewParent != reviewFolder {
		return errors.New("sealed review snapshot does not match active state")
	}
	return nil
}

func metadataContains(current, sealed map[string]string) bool {
	for key, value := range sealed {
		if current[key] != value {
			return false
		}
	}
	return true
}

func hasPublicationErrors(issues []document.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == document.SeverityError {
			return true
		}
	}
	return false
}

func currentReviewSources(target *publication.Publication, members []reviewsync.SnapshotMember) (map[string][]byte, []string, error) {
	allowed := map[string]string{}
	if target.Kind == publication.KindBundle {
		for _, member := range target.Members {
			allowed[member.SourceKey] = member.Document.Path
		}
	} else if target.Source != nil {
		allowed[target.Source.SourceKey] = target.Source.Path
	}
	current := map[string][]byte{}
	paths := make([]string, 0, len(members))
	for _, member := range members {
		path := allowed[member.SourceKey]
		if path == "" {
			return nil, nil, fmt.Errorf("snapshot member %q is no longer part of the publication", member.SourceKey)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read current source %q: %w", member.SourceKey, err)
		}
		current[member.SourceKey] = content
		paths = append(paths, path)
	}
	return current, paths, nil
}

func reviewReadError(err error) error {
	if remote, ok := errors.AsType[*googleapi.Error](err); ok {
		switch remote.Kind {
		case googleapi.KindSuggestions:
			return WrapError(ClassConflict, string(ReviewCodeSuggestionsOpen), remote.Error(), err)
		case googleapi.KindUnstable:
			return WrapError(ClassConflict, string(ReviewCodeExportUnstable), remote.Error(), err)
		case googleapi.KindUnsupported:
			return WrapError(ClassConflict, string(ReviewCodeUnsupported), remote.Error(), err)
		case googleapi.KindConflict:
			return WrapError(ClassConflict, string(ReviewCodeTargetMismatch), remote.Error(), err)
		}
	}
	return googleError(err)
}

func writeReviewPatch(path string, content []byte, overwrite bool, sourcePaths []string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve patch output directory: %w", err)
	}
	resolvedOutput := filepath.Join(parent, filepath.Base(absolute))
	for _, source := range sourcePaths {
		resolvedSource, resolveErr := filepath.EvalSymlinks(source)
		if resolveErr == nil && sameFilesystemPath(resolvedOutput, resolvedSource) {
			return "", errors.New("patch output cannot overwrite a publication source")
		}
	}
	if info, statErr := os.Lstat(resolvedOutput); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("patch output must be a regular file, not a symlink")
		}
		for _, source := range sourcePaths {
			sourceInfo, sourceErr := os.Stat(source)
			if sourceErr == nil && os.SameFile(info, sourceInfo) {
				return "", errors.New("patch output cannot overwrite a publication source")
			}
		}
		if !overwrite {
			return "", errors.New("patch output already exists; use --overwrite to replace it")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	temporary, err := os.CreateTemp(parent, ".mdoc-review-patch-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if overwrite {
		err = os.Rename(temporaryPath, resolvedOutput)
	} else {
		err = os.Link(temporaryPath, resolvedOutput)
	}
	if err != nil {
		return "", err
	}
	directory, err := os.Open(parent)
	if err == nil {
		err = directory.Sync()
		directory.Close()
	}
	if err != nil {
		return "", err
	}
	return resolvedOutput, nil
}

func sameFilesystemPath(left, right string) bool {
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) && os.PathSeparator == '\\' {
		return true
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
