package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/sourcekey"
	"github.com/goliatone/mdoc/internal/state"
)

func (a *Application) Reconcile(ctx context.Context, options ReconcileOptions) error {
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return err
	}
	options.Config = cfg.Path
	options.Profile = profile.Name
	if err := store.Acquire(); err != nil {
		return WrapError(ClassConflict, "state_locked", err.Error(), err)
	}
	defer store.Release()
	_, journalErr := store.LoadJournal()
	journalExists := journalErr == nil || !errors.Is(journalErr, state.ErrNotFound)
	if journalErr != nil && !errors.Is(journalErr, state.ErrNotFound) && !options.AbandonOperation {
		return NewError(ClassConflict, "publish_journal_invalid", fmt.Sprintf("%v; rerun `mdoc state reconcile --abandon-operation` to clear it after a safe remote scan", journalErr))
	}
	if remap, remapErr := store.LoadRemapJournal(); remapErr == nil {
		return NewError(ClassConflict, "remap_resume_required", fmt.Sprintf("unfinished source remap %s to %s must be resumed with `mdoc state remap --from %s --to %s` before reconciliation", remap.From, remap.To, remap.From, remap.To))
	} else if !errors.Is(remapErr, state.ErrNotFound) {
		return partial(remapErr)
	}
	existingState, existingStateErr := store.Load()
	recoveredAccount := ""
	if existingStateErr == nil {
		if err := a.verifyStoredAccount(ctx, existingState); err != nil {
			return err
		}
	} else if !errors.Is(existingStateErr, state.ErrNotFound) && !errors.Is(existingStateErr, state.ErrDamaged) {
		return WrapError(ClassPartial, "state_read_failed", existingStateErr.Error(), existingStateErr)
	}
	if a.Auth == nil {
		return NewError(ClassAuthentication, "auth_unavailable", "Google authorization is unavailable; run `mdoc auth login`")
	}
	client, err := a.Auth.Client(ctx)
	if err != nil {
		return WrapError(ClassAuthentication, "auth_client_failed", err.Error(), err)
	}
	if existingState == nil {
		recoveredAccount, err = a.currentAccount(ctx)
		if err != nil {
			return err
		}
	}
	clients, err := a.GoogleFactory.New(ctx, client)
	if err != nil {
		return googleError(err)
	}
	input := state.ReconcileInput{WorkspaceID: cfg.WorkspaceID, Profile: profile.Name}
	for _, role := range []string{"staging", "review"} {
		folders, err := clients.Drive.FindFolders(ctx, cfg.WorkspaceID, profile.Name, role)
		if err != nil {
			return googleError(err)
		}
		for _, folder := range folders {
			input.Folders = append(input.Folders, state.RemoteFolder{FileID: folder.ID, Role: role})
		}
	}
	files, err := clients.Drive.FindDocuments(ctx, map[string]string{"mdoc_workspace": cfg.WorkspaceID, "mdoc_profile": profile.Name})
	if err != nil {
		return googleError(err)
	}
	publicationMembers := configuredPublicationMembers(cfg.Root, profile)
	publicationSources := configuredPublicationSources(cfg.Root, profile)
	for _, file := range files {
		properties := file.AppProperties
		generation, generationErr := strconv.Atoi(properties["mdoc_generation"])
		expected, expectedErr := strconv.Atoi(properties["mdoc_expected_set_size"])
		if generationErr != nil || expectedErr != nil || properties["mdoc_review_set"] == "" || properties["mdoc_operation"] == "" {
			return NewError(ClassConflict, "remote_metadata_invalid", fmt.Sprintf("Google file %s has incomplete mdoc recovery metadata", file.ID))
		}
		targetKey := properties["mdoc_target_key"]
		publicationID := properties["mdoc_publication"]
		publicationKind := properties["mdoc_publication_kind"]
		source := ""
		if properties["mdoc_source"] != "" {
			var sourceErr error
			source, sourceErr = sourcekey.Normalize(properties["mdoc_source"])
			if sourceErr != nil || source != properties["mdoc_source"] {
				return NewError(ClassConflict, "remote_metadata_invalid", fmt.Sprintf("Google file %s has an invalid mdoc source key", file.ID))
			}
		}
		if targetKey == "" {
			if source == "" {
				return NewError(ClassConflict, "remote_metadata_invalid", fmt.Sprintf("Google file %s has no target identity", file.ID))
			}
		} else if targetKey != "publication:"+publicationID || publicationID == "" || publicationKind != "source" && publicationKind != "bundle" || publicationKind == "source" && source == "" || publicationKind == "bundle" && source != "" {
			return NewError(ClassConflict, "remote_metadata_invalid", fmt.Sprintf("Google file %s has inconsistent publication metadata", file.ID))
		}
		if configured, exists := profile.Publications[publicationID]; targetKey != "" && exists {
			if string(configured.Kind) != publicationKind || publicationKind == "source" && publicationSources[publicationID] != source {
				return NewError(ClassConflict, "remote_metadata_invalid", fmt.Sprintf("Google file %s publication metadata does not match configured publication %s", file.ID, publicationID))
			}
		}
		doc, err := clients.Docs.GetDocument(ctx, file.ID)
		if err != nil {
			return googleError(err)
		}
		parent := ""
		if len(file.Parents) > 0 {
			parent = file.Parents[0]
		}
		input.Targets = append(input.Targets, state.RemoteTarget{
			Target:    state.Target{FileID: file.ID, URL: file.URL, ReviewSetID: properties["mdoc_review_set"], Generation: generation, OperationID: properties["mdoc_operation"], PublishStatus: properties["mdoc_status"], RemoteVersion: file.Version, DocsRevision: doc.RevisionID, ModifiedTime: file.ModifiedTime, PublishedAt: file.ModifiedTime},
			SourceKey: source, TargetKey: targetKey, PublicationID: publicationID, PublicationKind: publicationKind, Members: publicationMembers[publicationID], ExpectedSetSize: expected, ParentID: parent,
		})
	}
	result, err := state.Reconcile(input)
	if err != nil {
		return WrapError(ClassConflict, "remote_reconcile_conflict", err.Error(), err)
	}
	if existingState != nil {
		result.State.Account = existingState.Account
	} else {
		result.State.Account = recoveredAccount
	}
	recoveredSnapshots := 0
	if cfg.Version >= config.ProjectVersion3 {
		recoveredSnapshots = recoverReviewSnapshotReferences(store.Path(), result.State)
	}
	if options.AbandonOperation && journalExists && len(result.PendingSets) > 0 {
		return NewError(ClassConflict, "publish_journal_abandon_unsafe", fmt.Sprintf("cannot abandon the publish journal while pending review sets exist: %s", strings.Join(result.PendingSets, ", ")))
	}
	if err := store.Save(result.State); err != nil {
		return WrapError(ClassPartial, "state_save_failed", err.Error(), err)
	}
	if options.AbandonOperation && journalExists {
		if err := store.ClearJournal(); err != nil {
			return WrapError(ClassPartial, "publish_journal_clear_failed", err.Error(), err)
		}
		fmt.Fprintln(a.Out, "Abandoned the prior publish operation after a safe remote scan.")
	}
	fmt.Fprintf(a.Out, "Recovered %d active target mappings.\n", len(result.State.Targets))
	if recoveredSnapshots > 0 {
		fmt.Fprintf(a.Out, "Recovered %d sealed review snapshot references.\n", recoveredSnapshots)
	}
	for _, setID := range result.PendingSets {
		fmt.Fprintf(a.Out, "pending review set: %s\n", setID)
	}
	return nil
}

func configuredPublicationMembers(root string, profile config.SelectedProfile) map[string][]string {
	result := map[string][]string{}
	sources, err := document.SelectSources(root, profile)
	if err != nil {
		return result
	}
	byPath := map[string]string{}
	for _, source := range sources {
		byPath[filepath.Clean(source.Path)] = source.Key
	}
	for id, target := range profile.Publications {
		if target.Kind != config.PublicationBundle {
			continue
		}
		for _, member := range target.Members {
			if key := byPath[filepath.Clean(member.Source)]; key != "" {
				result[id] = append(result[id], key)
			}
		}
	}
	return result
}

func configuredPublicationSources(root string, profile config.SelectedProfile) map[string]string {
	result := map[string]string{}
	sources, err := document.SelectSources(root, profile)
	if err != nil {
		return result
	}
	byPath := map[string]string{}
	for _, source := range sources {
		byPath[filepath.Clean(source.Path)] = source.Key
	}
	for id, target := range profile.Publications {
		if target.Kind == config.PublicationSource {
			result[id] = byPath[filepath.Clean(target.Source)]
		}
	}
	return result
}

func (a *Application) Remap(ctx context.Context, options RemapOptions) (returnedErr error) {
	if strings.HasPrefix(options.From, "publication:") || strings.HasPrefix(options.To, "publication:") {
		return NewError(ClassCommand, "remap_publication_unsupported", "explicit publication targets keep their identity when a source changes; edit the publication source instead of remapping state")
	}
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return err
	}
	options.Config = cfg.Path
	options.Profile = profile.Name
	from, err := normalizedKey(options.From)
	if err != nil {
		return NewError(ClassCommand, "remap_source_invalid", err.Error())
	}
	to, err := normalizedKey(options.To)
	if err != nil {
		return NewError(ClassCommand, "remap_target_invalid", err.Error())
	}
	if from == to {
		return NewError(ClassCommand, "remap_same_source", "--from and --to must differ")
	}
	if err := store.Acquire(); err != nil {
		return WrapError(ClassConflict, "state_locked", err.Error(), err)
	}
	defer store.Release()
	if _, journalErr := store.LoadJournal(); journalErr == nil {
		return NewError(ClassConflict, "publish_resume_required", "an unfinished publish must be resumed before a source remap")
	} else if !errors.Is(journalErr, state.ErrNotFound) {
		return WrapError(ClassPartial, "publish_journal_invalid", fmt.Sprintf("%v; run `mdoc state reconcile --abandon-operation` to clear it after a safe remote scan", journalErr), journalErr)
	}
	remapJournal, journalErr := store.LoadRemapJournal()
	if journalErr != nil && !errors.Is(journalErr, state.ErrNotFound) {
		return partial(journalErr)
	}
	remapJournalDurable := journalErr == nil
	defer func() {
		if returnedErr != nil && remapJournalDurable {
			returnedErr = WithRecovery(returnedErr, "remap_recovery_required", remapRecovery(remapJournal, store, options.CommonOptions))
		}
	}()
	if journalErr == nil && (remapJournal.WorkspaceID != cfg.WorkspaceID || remapJournal.Profile != profile.Name || remapJournal.From != from || remapJournal.To != to) {
		return NewError(ClassConflict, "remap_resume_mismatch", fmt.Sprintf("unfinished remap is %s to %s; rerun that exact remap before starting another", remapJournal.From, remapJournal.To))
	}
	publishState, err := store.Load()
	if err != nil {
		return WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	}
	if err := a.verifyStoredAccount(ctx, publishState); err != nil {
		return err
	}
	documentState, fromExists := publishState.Documents[from]
	targetState, toExists := publishState.Documents[to]
	if info, statErr := os.Stat(filepath.Join(cfg.Root, filepath.FromSlash(to))); statErr != nil || info.IsDir() {
		return NewError(ClassValidation, "remap_target_missing", fmt.Sprintf("new source %s does not exist in the workspace", to))
	}
	if journalErr == nil {
		if fromExists && toExists {
			return NewError(ClassConflict, "remap_state_ambiguous", "both old and new source keys exist while a remap is unfinished; run `mdoc state reconcile`")
		}
		if !fromExists && toExists {
			documentState = targetState
		}
	} else {
		if !fromExists {
			return NewError(ClassConflict, "remap_source_missing", fmt.Sprintf("source %s is not present in publish state", from))
		}
		if toExists {
			return NewError(ClassConflict, "remap_target_exists", fmt.Sprintf("source %s already has publish state", to))
		}
	}
	if !fromExists && !toExists {
		return NewError(ClassConflict, "remap_state_missing", "neither the old nor new source key is present in publish state; run `mdoc state reconcile`")
	}
	if a.Auth == nil {
		return NewError(ClassAuthentication, "auth_unavailable", "Google authorization is unavailable; run `mdoc auth login`")
	}
	client, err := a.Auth.Client(ctx)
	if err != nil {
		return WrapError(ClassAuthentication, "auth_client_failed", err.Error(), err)
	}
	clients, err := a.GoogleFactory.New(ctx, client)
	if err != nil {
		return googleError(err)
	}
	targets := append([]state.Target(nil), documentState.PriorTargets...)
	if documentState.ActiveTarget != nil {
		targets = append(targets, *documentState.ActiveTarget)
	}
	if len(targets) == 0 {
		return NewError(ClassConflict, "remap_targets_missing", fmt.Sprintf("source %s has no remote targets to remap", from))
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Generation < targets[j].Generation })
	if remapJournal == nil {
		operationID, err := randomID()
		if err != nil {
			return WrapError(ClassCommand, "remap_operation_id_failed", "create remap operation ID", err)
		}
		remapJournal = &state.RemapJournal{OperationID: operationID, WorkspaceID: cfg.WorkspaceID, Profile: profile.Name, From: from, To: to, Entries: map[string]state.RemapJournalEntry{}}
		for _, target := range targets {
			remapJournal.Targets = append(remapJournal.Targets, target.FileID)
			remapJournal.Entries[target.FileID] = state.RemapJournalEntry{FileID: target.FileID, Generation: target.Generation, Stage: state.RemapStagePlanned}
		}
		if err := store.WriteRemapJournal(remapJournal); err != nil {
			return partial(err)
		}
		remapJournalDurable = true
	}
	if len(remapJournal.Targets) != len(targets) {
		return NewError(ClassConflict, "remap_target_set_changed", "publish state targets changed during the unfinished remap; run `mdoc state reconcile`")
	}
	stateTargets := map[string]bool{}
	for _, target := range targets {
		stateTargets[target.FileID] = true
	}
	for _, fileID := range remapJournal.Targets {
		if !stateTargets[fileID] {
			return NewError(ClassConflict, "remap_target_set_changed", "publish state targets changed during the unfinished remap; run `mdoc state reconcile`")
		}
	}
	updated := map[string]state.Target{}
	for _, target := range targets {
		entry := remapJournal.Entries[target.FileID]
		if entry.Stage == state.RemapStagePlanned {
			if err := store.AdvanceRemapJournal(remapJournal, target.FileID, state.RemapStageUpdating); err != nil {
				return partial(err)
			}
		}
		var file googleapi.File
		if err := (googleapi.RetryPolicy{}).Do(ctx, func() error {
			current, err := clients.Drive.GetFile(ctx, target.FileID)
			if err != nil {
				return err
			}
			if current.AppProperties["mdoc_workspace"] != cfg.WorkspaceID || current.AppProperties["mdoc_profile"] != profile.Name {
				return NewError(ClassConflict, "remap_target_metadata_mismatch", fmt.Sprintf("Google file %s does not belong to this workspace and profile", target.FileID))
			}
			switch current.AppProperties["mdoc_source"] {
			case to:
				file = current
				return nil
			case from:
				updatedFile, err := clients.Drive.UpdateProperties(ctx, target.FileID, map[string]string{"mdoc_source": to})
				if err != nil {
					return err
				}
				file = updatedFile
				return nil
			default:
				return NewError(ClassConflict, "remap_target_source_mismatch", fmt.Sprintf("Google file %s has source metadata %q instead of %q or %q", target.FileID, current.AppProperties["mdoc_source"], from, to))
			}
		}); err != nil {
			return partial(err)
		}
		file, err = clients.Drive.GetFile(ctx, target.FileID)
		if err != nil {
			return partial(err)
		}
		if file.AppProperties["mdoc_source"] != to {
			return partial(fmt.Errorf("google file %s did not retain remapped source metadata", target.FileID))
		}
		if remapJournal.Entries[target.FileID].Stage != state.RemapStageUpdated {
			if err := store.AdvanceRemapJournal(remapJournal, target.FileID, state.RemapStageUpdated); err != nil {
				return partial(err)
			}
		}
		target.RemoteVersion = file.Version
		target.ModifiedTime = file.ModifiedTime
		updated[target.FileID] = target
	}
	for index, target := range documentState.PriorTargets {
		documentState.PriorTargets[index] = updated[target.FileID]
	}
	if documentState.ActiveTarget != nil {
		target := updated[documentState.ActiveTarget.FileID]
		documentState.ActiveTarget = &target
	}
	delete(publishState.Documents, from)
	publishState.Documents[to] = documentState
	if err := store.Save(publishState); err != nil {
		return partial(err)
	}
	if err := store.ClearRemapJournal(); err != nil {
		return partial(err)
	}
	fmt.Fprintf(a.Out, "Remapped %s to %s. Remote documents were not renamed or deleted.\n", from, to)
	return nil
}

func remapRecovery(journal *state.RemapJournal, store *state.Store, options CommonOptions) Recovery {
	fileIDs := make(map[string]string, len(journal.Entries))
	for _, entry := range journal.Entries {
		fileIDs[fmt.Sprintf("generation-%d", entry.Generation)] = entry.FileID
	}
	next := fmt.Sprintf(
		"mdoc state remap --config %s --profile %s --from %s --to %s",
		strconv.Quote(options.Config),
		strconv.Quote(journal.Profile),
		strconv.Quote(journal.From),
		strconv.Quote(journal.To),
	)
	return Recovery{Kind: "remap", OperationID: journal.OperationID, FileIDs: fileIDs, JournalPath: store.RemapJournalPath(), NextCommand: next}
}

func (a *Application) Open(ctx context.Context, options OpenOptions) error {
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return err
	}
	publishState, err := store.Load()
	if err != nil {
		return WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	}
	if err := a.verifyStoredAccount(ctx, publishState); err != nil {
		return err
	}
	if cfg.Version >= 3 {
		return a.openPublication(cfg, profile, publishState, options)
	}
	sources, err := document.SelectSources(cfg.Root, profile)
	if err != nil {
		return WrapError(ClassValidation, "open_source_discovery_failed", err.Error(), err)
	}
	key := ""
	if options.File == "" {
		key, err = document.ResolveEntrySourceKey(cfg.Root, sources, profile.Entry)
		if err != nil {
			return NewError(ClassCommand, "open_source_invalid", err.Error())
		}
		if key == "" {
			return NewError(ClassCommand, "open_source_required", "entry is required when the configured source set does not select exactly one file")
		}
	} else {
		key, err = document.ResolveSelectedSourceKey(cfg.Root, sources, options.File)
		if err != nil {
			legacyKey, legacyErr := normalizedKey(options.File)
			if legacyErr != nil {
				return NewError(ClassCommand, "open_source_invalid", err.Error())
			}
			if _, exists := publishState.Documents[legacyKey]; !exists {
				return NewError(ClassCommand, "open_source_invalid", err.Error())
			}
			key = legacyKey
		}
	}
	documentState, ok := publishState.Documents[key]
	if !ok || documentState.ActiveTarget == nil {
		return NewError(ClassConflict, "open_target_missing", fmt.Sprintf("source %s has no active Google Doc", key))
	}
	fmt.Fprintln(a.Out, documentState.ActiveTarget.URL)
	if a.Browser != nil {
		if err := a.Browser.Open(documentState.ActiveTarget.URL); err != nil {
			return WrapError(ClassCommand, "browser_open_failed", fmt.Sprintf("open the URL manually: %s", documentState.ActiveTarget.URL), err)
		}
	}
	return nil
}

func (a *Application) openPublication(cfg *config.Config, profile config.SelectedProfile, publishState *state.State, options OpenOptions) error {
	publicationID := profile.EntryPublication
	if options.Bundle != "" {
		configured, ok := profile.Publications[options.Bundle]
		if !ok || configured.Kind != config.PublicationBundle {
			return NewError(ClassCommand, "open_bundle_invalid", fmt.Sprintf("bundle publication %q does not exist", options.Bundle))
		}
		publicationID = options.Bundle
	} else if options.File != "" {
		sources, err := document.SelectSources(cfg.Root, profile)
		if err != nil {
			return WrapError(ClassValidation, "open_source_discovery_failed", err.Error(), err)
		}
		key, err := document.ResolveSelectedSourceKey(cfg.Root, sources, options.File)
		if err != nil {
			return NewError(ClassCommand, "open_source_invalid", err.Error())
		}
		matches := []string{}
		for id, configured := range profile.Publications {
			if configured.Kind != config.PublicationSource {
				continue
			}
			for _, source := range sources {
				if source.Key == key && filepath.Clean(source.Path) == filepath.Clean(configured.Source) {
					matches = append(matches, id)
				}
			}
		}
		sort.Strings(matches)
		if len(matches) != 1 {
			return NewError(ClassCommand, "open_source_ambiguous", fmt.Sprintf("source %s maps to %d publications; select an entry publication or use --bundle for bundles", key, len(matches)))
		}
		publicationID = matches[0]
	}
	key := "publication:" + publicationID
	documentState, ok := publishState.Targets[key]
	if !ok || documentState.ActiveTarget == nil {
		return NewError(ClassConflict, "open_target_missing", fmt.Sprintf("publication %s has no active Google Doc", publicationID))
	}
	if cfg.Output.Format == "json" {
		return json.NewEncoder(a.Out).Encode(map[string]any{"schema_version": 2, "kind": "open", "target": key, "publication_id": publicationID, "publication_kind": documentState.PublicationKind, "url": documentState.ActiveTarget.URL})
	}
	if !cfg.Output.Quiet {
		fmt.Fprintf(a.Out, "Publication: %s (%s)\n", publicationID, documentState.PublicationKind)
	}
	fmt.Fprintln(a.Out, documentState.ActiveTarget.URL)
	if a.Browser != nil {
		if err := a.Browser.Open(documentState.ActiveTarget.URL); err != nil {
			return WrapError(ClassCommand, "browser_open_failed", fmt.Sprintf("open the URL manually: %s", documentState.ActiveTarget.URL), err)
		}
	}
	return nil
}

func normalizedKey(value string) (string, error) {
	return sourcekey.Normalize(value)
}
