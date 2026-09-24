package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/render"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

type PendingLink struct {
	OriginalTarget string
	Placeholder    string
	TargetSource   string
	HadFragment    bool
}

type ReviewPublishInput struct {
	Reader  string
	Binary  string
	Sources []reviewsync.SnapshotSource
}

type BatchItem struct {
	SourceKey       string
	SourceInput     string
	Title           string
	DOCX            []byte
	SourceHash      string
	RenderHash      string
	HashInput       *render.HashInput
	Generation      int
	ResolvedFields  map[string]any
	FieldsHash      string
	FieldFileHashes []string
	ComputedValues  map[string]any
	ComputedHash    string
	ArtifactHash    string
	HashParts       []state.HashPartDigest
	PublicationID   string
	PublicationKind string
	Members         []state.JournalMember
	LayoutHash      string
	TopologyHash    string
	Links           []PendingLink
	Review          *ReviewPublishInput
}

type BatchRequest struct {
	WorkspaceID   string
	Profile       string
	StagingID     string
	ReviewID      string
	ReviewSetID   string
	OperationID   string
	ResumeCommand string
	Items         []BatchItem
	KnownTargets  map[string]string
	UseTargets    bool
}

type BatchResult struct {
	Targets  map[string]state.Target
	Warnings []string
}

type BatchPublisher struct {
	Drive                googleapi.Drive
	Docs                 googleapi.Docs
	Store                *state.Store
	Creator              googleapi.Creator
	Retry                googleapi.RetryPolicy
	Now                  func() time.Time
	AfterCreate          func(string, string) error
	AfterReviewCandidate func(string) error
	AfterReviewSeal      func(string) error
	AfterStateSave       func() error
}

func PlaceholderURL(sourceKey string) string {
	sum := sha256.Sum256([]byte(sourceKey))
	return fmt.Sprintf("https://%x.mdoc.invalid/pending", sum[:12])
}

func (p BatchPublisher) Run(ctx context.Context, request BatchRequest) (result BatchResult, returnedErr error) {
	result = BatchResult{Targets: map[string]state.Target{}}
	if err := validateBatch(request); err != nil {
		return result, NewError(ClassConflict, "publish_plan_blocked", err.Error())
	}
	publishState, err := p.Store.Load()
	if err != nil {
		return result, WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	}
	if publishState.WorkspaceID != request.WorkspaceID || publishState.Profile != request.Profile {
		return result, NewError(ClassConflict, "state_identity_mismatch", "publish state does not match the selected workspace and profile")
	}
	items := append([]BatchItem(nil), request.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].SourceKey < items[j].SourceKey })
	journal, err := p.loadOrCreateJournal(request, items)
	if err != nil {
		if existing, loadErr := p.Store.LoadJournal(); loadErr == nil {
			return result, WithRecovery(err, "publish_recovery_required", publishRecovery(request, existing, p.Store))
		}
		return result, err
	}
	defer func() {
		if returnedErr != nil {
			returnedErr = WithRecovery(returnedErr, "publish_recovery_required", publishRecovery(request, journal, p.Store))
		}
	}()
	creator := p.Creator
	creator.Drive = p.Drive
	creator.Docs = p.Docs

	files := map[string]googleapi.NativeDocument{}
	for _, item := range items {
		entry := journal.Entries[item.SourceKey]
		if stageRank(entry.Stage) < stageRank(state.StageCreated) {
			if stageRank(entry.Stage) < stageRank(state.StageCreating) {
				if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageCreating, ""); err != nil {
					return result, partial(err)
				}
			}
			created, err := creator.Create(ctx, item.Title, request.StagingID, item.DOCX, googleapi.PublishMetadata{
				WorkspaceID: request.WorkspaceID, Profile: request.Profile, SourceKey: firstNonEmpty(item.SourceInput, legacySourceInput(request.UseTargets, item.SourceKey)),
				TargetKey: explicitTargetKey(request.UseTargets, item.SourceKey), PublicationID: item.PublicationID, PublicationKind: item.PublicationKind,
				ReviewSetID: request.ReviewSetID, Generation: item.Generation, OperationID: request.OperationID,
				ExpectedSetSize: len(items), PublishStatus: "pending",
			})
			if err != nil {
				return result, partial(err)
			}
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageCreated, created.ID); err != nil {
				return result, partial(err)
			}
			if p.AfterCreate != nil {
				if err := p.AfterCreate(item.SourceKey, created.ID); err != nil {
					return result, partial(err)
				}
			}
			files[item.SourceKey] = created
		} else {
			if entry.FileID == "" {
				return result, partial(fmt.Errorf("journal has no file ID for %s", item.SourceKey))
			}
			file, err := p.Drive.GetFile(ctx, entry.FileID)
			if err != nil {
				return result, partial(err)
			}
			doc, err := p.Docs.GetDocument(ctx, entry.FileID)
			if err != nil {
				return result, partial(err)
			}
			files[item.SourceKey] = googleapi.NativeDocument{File: file, DocsRevision: doc.RevisionID}
		}
	}

	for _, item := range items {
		entry := journal.Entries[item.SourceKey]
		if stageRank(entry.Stage) < stageRank(state.StageLinksFixed) {
			if stageRank(entry.Stage) < stageRank(state.StageFixingLinks) {
				if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageFixingLinks, entry.FileID); err != nil {
					return result, partial(err)
				}
			}
			remoteFile, err := p.Drive.GetFile(ctx, entry.FileID)
			if err != nil {
				return result, partial(err)
			}
			if remoteFile.AppProperties["mdoc_status"] != "pending" || remoteFile.AppProperties["mdoc_operation"] != request.OperationID {
				return result, partial(fmt.Errorf("link update refused for nonpending document %s", item.SourceKey))
			}
			for _, link := range item.Links {
				if link.HadFragment {
					result.Warnings = append(result.Warnings, fmt.Sprintf("%s: heading link to %s falls back to the document root", item.SourceKey, link.TargetSource))
				}
			}
			if err := p.retryPolicy().Do(ctx, func() error {
				doc, err := p.Docs.GetDocument(ctx, entry.FileID)
				if err != nil {
					return err
				}
				updates := []googleapi.LinkUpdate{}
				for _, link := range item.Links {
					targetURL := request.KnownTargets[link.TargetSource]
					if current, ok := files[link.TargetSource]; ok {
						targetURL = current.URL
					}
					placeholderRanges := doc.Links[link.Placeholder]
					if len(placeholderRanges) == 0 && len(doc.Links[targetURL]) == 0 {
						return fmt.Errorf("pending link placeholder for %s to %s was not found", item.SourceKey, link.TargetSource)
					}
					for _, textRange := range placeholderRanges {
						updates = append(updates, googleapi.LinkUpdate{StartIndex: textRange.StartIndex, EndIndex: textRange.EndIndex, URL: targetURL})
					}
				}
				if len(updates) == 0 {
					return nil
				}
				_, err = p.Docs.UpdateLinks(ctx, entry.FileID, doc.RevisionID, updates)
				return err
			}); err != nil {
				return result, partial(err)
			}
			if _, err := p.Drive.GetFile(ctx, entry.FileID); err != nil {
				return result, partial(err)
			}
			if _, err := p.Docs.GetDocument(ctx, entry.FileID); err != nil {
				return result, partial(err)
			}
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageLinksFixed, entry.FileID); err != nil {
				return result, partial(err)
			}
		}
	}

	var snapshotStore *reviewsync.SnapshotStore
	for _, item := range items {
		if item.Review == nil {
			continue
		}
		if snapshotStore == nil {
			snapshotStore, err = reviewsync.NewSnapshotStore(p.Store.Path())
			if err != nil {
				return result, partial(err)
			}
		}
		if _, err := p.captureReviewSnapshot(ctx, request, item, journal, snapshotStore); err != nil {
			return result, partial(err)
		}
	}

	for _, item := range items {
		entry := journal.Entries[item.SourceKey]
		if stageRank(entry.Stage) < stageRank(state.StageReady) {
			if stageRank(entry.Stage) < stageRank(state.StageMarkingReady) {
				if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageMarkingReady, entry.FileID); err != nil {
					return result, partial(err)
				}
			}
			if err := p.retryPolicy().Do(ctx, func() error {
				current, err := p.Drive.GetFile(ctx, entry.FileID)
				if err != nil {
					return err
				}
				if current.AppProperties["mdoc_status"] == "ready" {
					return nil
				}
				_, err = p.Drive.UpdateProperties(ctx, entry.FileID, map[string]string{"mdoc_status": "ready"})
				return err
			}); err != nil {
				return result, partial(err)
			}
			if _, err := p.Drive.GetFile(ctx, entry.FileID); err != nil {
				return result, partial(err)
			}
			if _, err := p.Docs.GetDocument(ctx, entry.FileID); err != nil {
				return result, partial(err)
			}
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageReady, entry.FileID); err != nil {
				return result, partial(err)
			}
		}
	}

	for _, item := range items {
		entry := journal.Entries[item.SourceKey]
		if stageRank(entry.Stage) < stageRank(state.StageMoved) {
			if stageRank(entry.Stage) < stageRank(state.StageMoving) {
				if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageMoving, entry.FileID); err != nil {
					return result, partial(err)
				}
			}
			if err := p.retryPolicy().Do(ctx, func() error {
				current, err := p.Drive.GetFile(ctx, entry.FileID)
				if err != nil {
					return err
				}
				if contains(current.Parents, request.ReviewID) && !contains(current.Parents, request.StagingID) {
					return nil
				}
				_, err = p.Drive.MoveFile(ctx, entry.FileID, request.ReviewID, request.StagingID)
				return err
			}); err != nil {
				return result, partial(err)
			}
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageMoved, entry.FileID); err != nil {
				return result, partial(err)
			}
		}
		finalFile, err := p.Drive.GetFile(ctx, entry.FileID)
		if err != nil {
			return result, partial(err)
		}
		finalDocument, err := p.Docs.GetDocument(ctx, entry.FileID)
		if err != nil {
			return result, partial(err)
		}
		files[item.SourceKey] = googleapi.NativeDocument{File: finalFile, DocsRevision: finalDocument.RevisionID}
	}

	sealedSnapshots := map[string]reviewsync.SnapshotManifest{}
	for _, item := range items {
		if item.Review == nil {
			continue
		}
		manifest, err := p.sealReviewSnapshot(ctx, request, item, journal, snapshotStore, files[item.SourceKey])
		if err != nil {
			return result, partial(err)
		}
		sealedSnapshots[item.SourceKey] = manifest
	}

	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	for _, item := range items {
		remote := files[item.SourceKey]
		target := state.Target{FileID: remote.ID, URL: remote.URL, ReviewSetID: request.ReviewSetID, Generation: item.Generation, OperationID: request.OperationID, PublishStatus: "ready", RemoteVersion: remote.Version, DocsRevision: remote.DocsRevision, ModifiedTime: remote.ModifiedTime, PublishedAt: now().UTC()}
		if snapshot, ok := sealedSnapshots[item.SourceKey]; ok {
			target.ReviewSnapshot = &state.ReviewSnapshotRef{Version: snapshot.Version, ManifestHash: snapshot.IntegrityHash, BaselineHash: snapshot.BaselineHash, CapturedRevision: snapshot.Capture.DocsRevision, ActivatedRevision: snapshot.Activation.DocsRevision}
		}
		documentState := publishState.Documents[item.SourceKey]
		if request.UseTargets {
			documentState = publishState.Targets[item.SourceKey]
		}
		if documentState.ActiveTarget == nil || documentState.ActiveTarget.OperationID != request.OperationID {
			if documentState.ActiveTarget != nil {
				documentState.PriorTargets = append(documentState.PriorTargets, *documentState.ActiveTarget)
			}
			documentState.ActiveTarget = &target
		} else {
			documentState.ActiveTarget = &target
		}
		documentState.SourceHash = item.SourceHash
		finalRenderHash := item.RenderHash
		if item.HashInput != nil && !request.UseTargets {
			hashInput := *item.HashInput
			hashInput.LinkTargets = cloneStrings(item.HashInput.LinkTargets)
			for _, link := range item.Links {
				targetURL := request.KnownTargets[link.TargetSource]
				if current, ok := files[link.TargetSource]; ok {
					targetURL = current.URL
				}
				if link.OriginalTarget != "" {
					hashInput.LinkTargets[link.OriginalTarget] = targetURL
				}
			}
			finalRenderHash = render.ComputeHash(hashInput)
		}
		documentState.RenderHash = finalRenderHash
		if request.UseTargets {
			documentState.TargetKey = item.SourceKey
			documentState.PublicationID = item.PublicationID
			documentState.PublicationKind = item.PublicationKind
			documentState.Source = item.SourceInput
			documentState.Members = journalMemberKeys(item.Members)
			publishState.Targets[item.SourceKey] = documentState
		} else {
			publishState.Documents[item.SourceKey] = documentState
		}
		result.Targets[item.SourceKey] = target
	}
	for _, item := range items {
		entry := journal.Entries[item.SourceKey]
		if stageRank(entry.Stage) < stageRank(state.StageCommitting) {
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageCommitting, entry.FileID); err != nil {
				return result, partial(err)
			}
		}
	}
	if err := p.Store.Save(publishState); err != nil {
		return result, partial(err)
	}
	if p.AfterStateSave != nil {
		if err := p.AfterStateSave(); err != nil {
			return result, partial(err)
		}
	}
	for _, item := range items {
		entry := journal.Entries[item.SourceKey]
		if stageRank(entry.Stage) < stageRank(state.StageCommitted) {
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageCommitted, entry.FileID); err != nil {
				return result, partial(err)
			}
		}
	}
	if err := p.Store.ClearJournal(); err != nil {
		return result, partial(err)
	}
	return result, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func legacySourceInput(useTargets bool, key string) string {
	if useTargets {
		return ""
	}
	return key
}

func explicitTargetKey(useTargets bool, key string) string {
	if useTargets {
		return key
	}
	return ""
}

func journalMemberKeys(members []state.JournalMember) []string {
	result := make([]string, 0, len(members))
	for _, member := range members {
		result = append(result, member.SourceKey)
	}
	return result
}

func publishRecovery(request BatchRequest, journal *state.Journal, store *state.Store) Recovery {
	fileIDs := map[string]string{}
	for source, entry := range journal.Entries {
		if entry.FileID != "" {
			fileIDs[source] = entry.FileID
		}
	}
	next := request.ResumeCommand
	if next == "" {
		next = fmt.Sprintf("mdoc publish --profile %s", request.Profile)
	}
	return Recovery{Kind: "publish", OperationID: journal.OperationID, ReviewSetID: journal.ReviewSetID, FileIDs: fileIDs, JournalPath: store.JournalPath(), NextCommand: next}
}

func (p BatchPublisher) retryPolicy() googleapi.RetryPolicy {
	return p.Retry
}

func (p BatchPublisher) captureReviewSnapshot(ctx context.Context, request BatchRequest, item BatchItem, journal *state.Journal, snapshots *reviewsync.SnapshotStore) (reviewsync.SnapshotManifest, error) {
	entry := journal.Entries[item.SourceKey]
	if stageRank(entry.Stage) < stageRank(state.StageReviewCaptured) {
		if stageRank(entry.Stage) < stageRank(state.StageCapturingReview) {
			if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageCapturingReview, entry.FileID); err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
			entry = journal.Entries[item.SourceKey]
		}
		loaded, loadErr := snapshots.Load(item.SourceKey, item.Generation, false)
		var manifest reviewsync.SnapshotManifest
		if loadErr == nil {
			manifest = loaded.Manifest
			if err := validateReviewSnapshotInputs(request, item, loaded); err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
		} else if errors.Is(loadErr, reviewsync.ErrSnapshotNotFound) {
			stable, err := (googleapi.StableReader{Drive: p.Drive, Docs: p.Docs}).Read(ctx, entry.FileID, nil)
			if err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
			if stable.File.AppProperties["mdoc_status"] != "pending" || stable.File.AppProperties["mdoc_operation"] != request.OperationID || !contains(stable.File.Parents, request.StagingID) {
				return reviewsync.SnapshotManifest{}, errors.New("review baseline capture target is not the pending staging document")
			}
			members := make([]reviewsync.MemberInput, 0, len(item.Review.Sources))
			for _, source := range item.Review.Sources {
				members = append(members, reviewsync.MemberInput{SourceKey: source.SourceKey, Content: source.Content})
			}
			sourceMap, err := reviewsync.BuildSourceMap(ctx, reviewsync.PandocParser{Binary: item.Review.Binary}, item.Review.Reader, members, stable.Content)
			if err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
			manifest, err = snapshots.WriteCandidate(reviewsync.CandidateInput{
				WorkspaceID: request.WorkspaceID, Profile: request.Profile, TargetKey: item.SourceKey,
				PublicationID: item.PublicationID, PublicationKind: item.PublicationKind, Reader: item.Review.Reader,
				FileID: entry.FileID, Generation: item.Generation, ReviewSetID: request.ReviewSetID, OperationID: request.OperationID,
				Capture:  reviewsync.CaptureTuple{FileID: stable.File.ID, DriveVersion: stable.File.Version, DocsRevision: stable.Document.RevisionID, Tabs: stable.Document.Tabs},
				Baseline: stable.Content, Sources: item.Review.Sources, SourceMap: sourceMap, CapturedAt: p.currentTime(),
			})
			if err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
		} else {
			return reviewsync.SnapshotManifest{}, loadErr
		}
		candidateHash := manifest.IntegrityHash
		if manifest.Status == reviewsync.SnapshotSealed {
			candidateHash = manifest.CandidateHash
		}
		if p.AfterReviewCandidate != nil {
			if err := p.AfterReviewCandidate(item.SourceKey); err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
		}
		if err := p.Store.AdvanceReviewJournal(journal, item.SourceKey, state.StageReviewCaptured, candidateHash); err != nil {
			return reviewsync.SnapshotManifest{}, err
		}
		return manifest, nil
	}
	loaded, err := snapshots.Load(item.SourceKey, item.Generation, false)
	if err != nil {
		return reviewsync.SnapshotManifest{}, err
	}
	if err := validateReviewSnapshotInputs(request, item, loaded); err != nil {
		return reviewsync.SnapshotManifest{}, err
	}
	candidateHash := loaded.Manifest.IntegrityHash
	if loaded.Manifest.Status == reviewsync.SnapshotSealed {
		candidateHash = loaded.Manifest.CandidateHash
	}
	if entry.ReviewCandidateHash != candidateHash {
		return reviewsync.SnapshotManifest{}, errors.New("review snapshot candidate hash does not match the publish journal")
	}
	return loaded.Manifest, nil
}

func (p BatchPublisher) sealReviewSnapshot(ctx context.Context, request BatchRequest, item BatchItem, journal *state.Journal, snapshots *reviewsync.SnapshotStore, remote googleapi.NativeDocument) (reviewsync.SnapshotManifest, error) {
	entry := journal.Entries[item.SourceKey]
	if stageRank(entry.Stage) < stageRank(state.StageReviewSealed) && stageRank(entry.Stage) < stageRank(state.StageSealingReview) {
		if err := p.Store.AdvanceJournal(journal, item.SourceKey, state.StageSealingReview, entry.FileID); err != nil {
			return reviewsync.SnapshotManifest{}, err
		}
		entry = journal.Entries[item.SourceKey]
	}
	loaded, err := snapshots.Load(item.SourceKey, item.Generation, false)
	if err != nil {
		return reviewsync.SnapshotManifest{}, err
	}
	if err := validateReviewSnapshotInputs(request, item, loaded); err != nil {
		return reviewsync.SnapshotManifest{}, err
	}
	document, err := p.Docs.GetDocument(ctx, remote.ID)
	if err != nil {
		return reviewsync.SnapshotManifest{}, err
	}
	if remote.ID != entry.FileID || remote.MimeType != googleapi.DocumentMimeType || remote.Trashed || !contains(remote.Parents, request.ReviewID) || contains(remote.Parents, request.StagingID) || remote.AppProperties["mdoc_status"] != "ready" || document.ID != remote.ID {
		return reviewsync.SnapshotManifest{}, errors.New("review target activation identity is incomplete")
	}
	manifest := loaded.Manifest
	if manifest.Status == reviewsync.SnapshotCandidate {
		manifest, err = snapshots.Seal(item.SourceKey, item.Generation, reviewsync.ActivationSeal{
			FileID: remote.ID, DriveVersion: remote.Version, DocsRevision: document.RevisionID,
			ReviewParent: request.ReviewID, ReadyMetadata: cloneStrings(remote.AppProperties), Tabs: document.Tabs, ActivatedAt: p.currentTime(),
		})
		if err != nil {
			return reviewsync.SnapshotManifest{}, err
		}
	} else if manifest.Activation == nil || manifest.Activation.FileID != remote.ID || manifest.Activation.DocsRevision != document.RevisionID || !sameReviewTabs(manifest.Activation.Tabs, document.Tabs) {
		return reviewsync.SnapshotManifest{}, errors.New("sealed review snapshot does not match the activated target")
	}
	entry = journal.Entries[item.SourceKey]
	if stageRank(entry.Stage) < stageRank(state.StageReviewSealed) {
		if p.AfterReviewSeal != nil {
			if err := p.AfterReviewSeal(item.SourceKey); err != nil {
				return reviewsync.SnapshotManifest{}, err
			}
		}
		if err := p.Store.AdvanceReviewJournal(journal, item.SourceKey, state.StageReviewSealed, manifest.IntegrityHash); err != nil {
			return reviewsync.SnapshotManifest{}, err
		}
	} else if entry.ReviewSealedHash != manifest.IntegrityHash {
		return reviewsync.SnapshotManifest{}, errors.New("sealed review snapshot hash does not match the publish journal")
	}
	return manifest, nil
}

func validateReviewSnapshotInputs(request BatchRequest, item BatchItem, loaded reviewsync.LoadedSnapshot) error {
	manifest := loaded.Manifest
	if manifest.WorkspaceID != request.WorkspaceID || manifest.Profile != request.Profile || manifest.TargetKey != item.SourceKey || manifest.PublicationID != item.PublicationID || manifest.PublicationKind != item.PublicationKind || manifest.Reader != item.Review.Reader || manifest.Generation != item.Generation || manifest.ReviewSetID != request.ReviewSetID || manifest.OperationID != request.OperationID {
		return errors.New("review snapshot identity does not match the publish operation")
	}
	if len(manifest.Members) != len(item.Review.Sources) {
		return errors.New("review snapshot member set changed")
	}
	for index, source := range item.Review.Sources {
		member := manifest.Members[index]
		if member.SourceKey != source.SourceKey || member.SourceHash != source.SourceHash || !bytes.Equal(loaded.Sources[source.SourceKey], source.Content) {
			return fmt.Errorf("review snapshot source %q changed", source.SourceKey)
		}
	}
	return nil
}

func sameReviewTabs(left, right []googleapi.TabTopology) bool {
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

func (p BatchPublisher) currentTime() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func validateBatch(request BatchRequest) error {
	if request.WorkspaceID == "" || request.Profile == "" || request.StagingID == "" || request.ReviewID == "" || request.ReviewSetID == "" || request.OperationID == "" || len(request.Items) == 0 {
		return errors.New("publish batch identity is incomplete")
	}
	sources := map[string]bool{}
	for _, item := range request.Items {
		if item.SourceKey == "" || item.Generation < 1 || len(item.DOCX) == 0 {
			return fmt.Errorf("publish item %q is incomplete", item.SourceKey)
		}
		if item.Review != nil && (item.PublicationID == "" || item.PublicationKind == "" || item.Review.Reader == "" || len(item.Review.Sources) == 0) {
			return fmt.Errorf("publish item %q review capture is incomplete", item.SourceKey)
		}
		if sources[item.SourceKey] {
			return fmt.Errorf("duplicate publish item %q", item.SourceKey)
		}
		sources[item.SourceKey] = true
	}
	for _, item := range request.Items {
		for _, link := range item.Links {
			if link.Placeholder == "" || link.TargetSource == "" {
				return fmt.Errorf("%s has an incomplete link action", item.SourceKey)
			}
			if !sources[link.TargetSource] && request.KnownTargets[link.TargetSource] == "" {
				return fmt.Errorf("%s has no target mapping for %s", item.SourceKey, link.TargetSource)
			}
		}
	}
	return nil
}

func (p BatchPublisher) loadOrCreateJournal(request BatchRequest, items []BatchItem) (*state.Journal, error) {
	journal, err := p.Store.LoadJournal()
	if err == nil {
		if journal.Kind != "publish" || journal.WorkspaceID != request.WorkspaceID || journal.Profile != request.Profile || journal.OperationID != request.OperationID || journal.ReviewSetID != request.ReviewSetID || journal.StagingID != request.StagingID || journal.ReviewID != request.ReviewID {
			return nil, partial(errors.New("unfinished operation does not match the requested publish"))
		}
		if !sameStringMap(journal.KnownTargets, request.KnownTargets) {
			return nil, partial(errors.New("unfinished operation target mappings changed; restore the prior state to resume, or run `mdoc state reconcile --abandon-operation` to discard the operation after a safe scan"))
		}
		if len(journal.Sources) != len(items) {
			return nil, partial(errors.New("unfinished operation source set changed"))
		}
		for _, item := range items {
			entry, ok := journal.Entries[item.SourceKey]
			if !ok {
				return nil, partial(fmt.Errorf("unfinished operation is missing %s", item.SourceKey))
			}
			artifactHash, artifactErr := batchArtifactHash(item)
			if artifactErr != nil {
				return nil, partial(artifactErr)
			}
			hashParts := batchHashParts(item)
			if entry.Generation != item.Generation || entry.SourceHash != item.SourceHash || entry.RenderHash != item.RenderHash || entry.DOCXHash != contentHash(item.DOCX) || entry.FieldsHash != item.FieldsHash || !sameStrings(entry.FieldFileHashes, item.FieldFileHashes) || entry.ComputedHash != item.ComputedHash || entry.ArtifactHash != artifactHash || !sameHashParts(entry.HashParts, hashParts) || entry.PublicationID != item.PublicationID || entry.PublicationKind != item.PublicationKind || !sameJournalMembers(entry.Members, item.Members) || entry.LayoutHash != item.LayoutHash || entry.TopologyHash != item.TopologyHash || !sameJournalLinks(entry.Links, item.Links) || entry.ReviewPullEnabled != (item.Review != nil) || item.Review != nil && entry.ReviewReader != item.Review.Reader {
				return nil, partial(fmt.Errorf("unfinished operation inputs changed for %s; restore the source, style, assets, and links used by the interrupted publish before retrying", item.SourceKey))
			}
		}
		return journal, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return nil, partial(err)
	}
	journal = &state.Journal{Kind: "publish", WorkspaceID: request.WorkspaceID, Profile: request.Profile, OperationID: request.OperationID, ReviewSetID: request.ReviewSetID, StagingID: request.StagingID, ReviewID: request.ReviewID, KnownTargets: cloneStrings(request.KnownTargets), Entries: map[string]state.JournalEntry{}}
	for _, item := range items {
		artifactHash, artifactErr := batchArtifactHash(item)
		if artifactErr != nil {
			return nil, partial(artifactErr)
		}
		hashParts := batchHashParts(item)
		journal.Sources = append(journal.Sources, item.SourceKey)
		links := make([]state.JournalLink, 0, len(item.Links))
		for _, link := range item.Links {
			links = append(links, state.JournalLink{OriginalTarget: link.OriginalTarget, Placeholder: link.Placeholder, TargetSource: link.TargetSource, HadFragment: link.HadFragment})
		}
		journal.Entries[item.SourceKey] = state.JournalEntry{
			SourceKey: item.SourceKey, Generation: item.Generation, SourceHash: item.SourceHash, RenderHash: item.RenderHash,
			DOCXHash: contentHash(item.DOCX), ResolvedFields: item.ResolvedFields, FieldsHash: item.FieldsHash,
			FieldFileHashes: append([]string(nil), item.FieldFileHashes...), ComputedValues: item.ComputedValues, ComputedHash: item.ComputedHash,
			ArtifactHash: artifactHash, HashParts: hashParts,
			PublicationID: item.PublicationID, PublicationKind: item.PublicationKind, Members: append([]state.JournalMember(nil), item.Members...), LayoutHash: item.LayoutHash, TopologyHash: item.TopologyHash,
			Links: links, ReviewPullEnabled: item.Review != nil, Stage: state.StagePlanned,
		}
		if item.Review != nil {
			entry := journal.Entries[item.SourceKey]
			entry.ReviewReader = item.Review.Reader
			journal.Entries[item.SourceKey] = entry
		}
	}
	sort.Strings(journal.Sources)
	if err := p.Store.WriteJournal(journal); err != nil {
		return nil, partial(err)
	}
	return journal, nil
}

func batchArtifactHash(item BatchItem) (string, error) {
	if item.ArtifactHash != "" {
		return item.ArtifactHash, nil
	}
	return render.ComputeArtifactHash(item.RenderHash, item.ComputedValues, item.DOCX)
}

func batchHashParts(item BatchItem) []state.HashPartDigest {
	if len(item.HashParts) > 0 {
		return append([]state.HashPartDigest(nil), item.HashParts...)
	}
	if item.HashInput == nil {
		return nil
	}
	described := render.DescribeHashInput(*item.HashInput)
	result := make([]state.HashPartDigest, 0, len(described))
	for _, part := range described {
		result = append(result, state.HashPartDigest{Name: part.Name, Hash: part.Hash})
	}
	return result
}

func sameHashParts(left, right []state.HashPartDigest) bool {
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

func sameJournalMembers(left, right []state.JournalMember) bool {
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

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func sameJournalLinks(stored []state.JournalLink, current []PendingLink) bool {
	if len(stored) != len(current) {
		return false
	}
	for index := range stored {
		left, right := stored[index], current[index]
		if left.OriginalTarget != right.OriginalTarget || left.Placeholder != right.Placeholder || left.TargetSource != right.TargetSource || left.HadFragment != right.HadFragment {
			return false
		}
	}
	return true
}

func sameStrings(left, right []string) bool {
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

func stageRank(stage state.Stage) int {
	order := []state.Stage{state.StagePlanned, state.StageCreating, state.StageCreated, state.StageFixingLinks, state.StageLinksFixed, state.StageCapturingReview, state.StageReviewCaptured, state.StageMarkingReady, state.StageReady, state.StageMoving, state.StageMoved, state.StageSealingReview, state.StageReviewSealed, state.StageCommitting, state.StageCommitted}
	for index, candidate := range order {
		if candidate == stage {
			return index
		}
	}
	return -1
}

func partial(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ambiguous") {
		return WrapError(ClassConflict, "remote_ambiguous", err.Error(), err)
	}
	return WrapError(ClassPartial, "publish_recovery_required", err.Error(), err)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func cloneStrings(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
