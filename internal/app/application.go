package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/publication"
	"github.com/goliatone/mdoc/internal/render"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/sourcekey"
	"github.com/goliatone/mdoc/internal/state"
)

type ClientProvider interface {
	Client(context.Context) (*http.Client, error)
}

type AccountProvider interface {
	Account(context.Context) (string, error)
}

func (a *Application) currentAccount(ctx context.Context) (string, error) {
	if a.Account == nil {
		return "", NewError(ClassAuthentication, "account_identity_unavailable", "cannot verify the signed in Google account; run `mdoc auth status` and retry")
	}
	account, err := a.Account.Account(ctx)
	if err != nil {
		return "", WrapError(ClassAuthentication, "account_identity_failed", err.Error(), err)
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return "", NewError(ClassAuthentication, "account_identity_empty", "the signed in Google account could not be identified; run `mdoc auth login`")
	}
	return account, nil
}

func verifyAccountMatch(publishState *state.State, account string) error {
	if publishState != nil && publishState.Account != "" && !strings.EqualFold(strings.TrimSpace(publishState.Account), strings.TrimSpace(account)) {
		return NewError(ClassConflict, "account_identity_mismatch", fmt.Sprintf("publish state belongs to Google account %s, not %s; run `mdoc auth login` with the setup account", publishState.Account, account))
	}
	return nil
}

func (a *Application) verifyStoredAccount(ctx context.Context, publishState *state.State) error {
	if publishState == nil || publishState.Account == "" {
		return nil
	}
	account, err := a.currentAccount(ctx)
	if err != nil {
		return err
	}
	return verifyAccountMatch(publishState, account)
}

type Application struct {
	Out              io.Writer
	ErrOut           io.Writer
	Auth             ClientProvider
	Account          AccountProvider
	GoogleFactory    googleapi.Factory
	DocumentRunner   document.Runner
	Renderer         render.Renderer
	ArtifactGate     PublicationArtifactGate
	BatchAfterCreate func(string, string) error
	Browser          Browser
}

type PublicationArtifact struct {
	TargetKey       string
	PublicationID   string
	PublicationKind string
	DOCX            []byte
}

type PublicationArtifactGate interface {
	Finalize(context.Context, PublicationArtifact) ([]byte, error)
}

type Browser interface{ Open(string) error }
type browserFunc func(string) error

func (f browserFunc) Open(target string) error { return f(target) }

func New(out io.Writer, providers ...ClientProvider) *Application {
	application := &Application{Out: out, ErrOut: out, GoogleFactory: googleapi.ServiceFactory{}, Browser: browserFunc(openSystemBrowser)}
	if len(providers) > 0 {
		application.Auth = providers[0]
		if account, ok := providers[0].(AccountProvider); ok {
			application.Account = account
		}
	}
	return application
}

type PublishDestination struct {
	Account string
	Staging googleapi.Folder
	Review  googleapi.Folder
}

func (a *Application) verifyPublishDestination(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, publishState *state.State, clients googleapi.Clients) (PublishDestination, error) {
	account, err := a.currentAccount(ctx)
	if err != nil {
		return PublishDestination{}, err
	}
	if err := verifyAccountMatch(publishState, account); err != nil {
		return PublishDestination{}, err
	}
	result := PublishDestination{Account: account}
	for _, role := range []string{"staging", "review"} {
		folders, err := clients.Drive.FindFolders(ctx, cfg.WorkspaceID, profile.Name, role)
		if err != nil {
			return PublishDestination{}, googleError(err)
		}
		if len(folders) != 1 {
			return PublishDestination{}, NewError(ClassConflict, "publish_folder_ambiguous", fmt.Sprintf("expected one app created %s folder for workspace %s profile %s; found %d; run `mdoc setup` or resolve duplicates", role, cfg.WorkspaceID, profile.Name, len(folders)))
		}
		folder := folders[0]
		expectedID := publishState.Folders.StagingID
		if role == "review" {
			expectedID = publishState.Folders.ReviewID
		}
		if err := validateMappedDestinationFolder(folder, account, cfg, profile, expectedID, role); err != nil {
			return PublishDestination{}, NewError(ClassConflict, "publish_folder_mismatch", fmt.Sprintf("%v; run `mdoc setup` or `mdoc state reconcile` before publishing", err))
		}
		if role == "staging" {
			result.Staging = folder
		} else {
			result.Review = folder
		}
	}
	if result.Staging.ID == result.Review.ID {
		return PublishDestination{}, NewError(ClassConflict, "publish_folder_collision", "staging and review folders resolve to the same Google folder; run `mdoc setup`")
	}
	return result, nil
}

func validateMappedDestinationFolder(folder googleapi.Folder, account string, cfg *config.Config, profile config.SelectedProfile, expectedID, role string) error {
	if err := googleapi.ValidateDestinationFolder(folder, account, role, profile.ParentFolderID, true); err != nil {
		return err
	}
	if expectedID == "" || folder.ID != expectedID || folder.Role != role || folder.AppProperties["mdoc_workspace"] != cfg.WorkspaceID || folder.AppProperties["mdoc_profile"] != profile.Name || folder.AppProperties["mdoc_role"] != role || folder.AppProperties["mdoc_kind"] != "folder" {
		return fmt.Errorf("stored %s folder does not match the unique app metadata", role)
	}
	return nil
}

func (a *Application) showPublishDestination(destination PublishDestination) {
	writer := a.ErrOut
	if writer == nil {
		writer = a.Out
	}
	fmt.Fprintf(writer, "Google account: %s\n", destination.Account)
	fmt.Fprintf(writer, "Staging folder: %s (%s)\n", destination.Staging.Name, destination.Staging.ID)
	fmt.Fprintf(writer, "Review folder: %s (%s)\n", destination.Review.Name, destination.Review.ID)
}

func (a *Application) Init(_ context.Context, options InitOptions) error {
	profileName := options.Profile
	if profileName == "" {
		profileName = "default"
	}
	if err := config.WriteNeutralProject(options.Config, profileName, options.Force); err != nil {
		return WrapError(ClassCommand, "config_create_failed", err.Error(), err)
	}
	fmt.Fprintf(a.Out, "Created %s\n", options.Config)
	return nil
}

func (a *Application) Setup(ctx context.Context, options CommonOptions) error {
	cfg, profile, store, err := a.load(ctx, options)
	if err != nil {
		return err
	}
	if err := store.Acquire(); err != nil {
		return WrapError(ClassConflict, "state_locked", err.Error(), err)
	}
	defer store.Release()
	if journal, journalErr := store.LoadJournal(); journalErr == nil {
		return NewError(ClassConflict, "publish_resume_required", fmt.Sprintf("unfinished publish operation %s must be resumed with `mdoc publish` before setup", journal.OperationID))
	} else if !errors.Is(journalErr, state.ErrNotFound) {
		return WrapError(ClassPartial, "publish_journal_invalid", fmt.Sprintf("%v; run `mdoc state reconcile --abandon-operation` after a safe remote scan before setup", journalErr), journalErr)
	}
	if remap, remapErr := store.LoadRemapJournal(); remapErr == nil {
		return NewError(ClassConflict, "remap_resume_required", fmt.Sprintf("unfinished source remap %s to %s must be resumed before setup", remap.From, remap.To))
	} else if !errors.Is(remapErr, state.ErrNotFound) {
		return partial(remapErr)
	}
	publishState, err := store.Load()
	if errors.Is(err, state.ErrNotFound) {
		publishState = state.New(cfg.WorkspaceID, profile.Name)
	} else if err != nil {
		return WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	} else if publishState.WorkspaceID != cfg.WorkspaceID || publishState.Profile != profile.Name {
		return NewError(ClassConflict, "state_identity_mismatch", fmt.Sprintf("state belongs to workspace %q profile %q, not workspace %q profile %q", publishState.WorkspaceID, publishState.Profile, cfg.WorkspaceID, profile.Name))
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
	if a.Account == nil {
		return NewError(ClassAuthentication, "account_identity_unavailable", "cannot verify the signed in Google account; run `mdoc auth status` and retry")
	}
	account, err := a.Account.Account(ctx)
	if err != nil {
		return WrapError(ClassAuthentication, "account_identity_failed", err.Error(), err)
	}
	if strings.TrimSpace(account) == "" {
		return NewError(ClassAuthentication, "account_identity_empty", "the signed in Google account could not be identified; run `mdoc auth login`")
	}
	if publishState.Account != "" && !strings.EqualFold(strings.TrimSpace(publishState.Account), strings.TrimSpace(account)) {
		return NewError(ClassConflict, "account_identity_mismatch", fmt.Sprintf("publish state belongs to Google account %s, not %s; run `mdoc auth login` with the setup account", publishState.Account, account))
	}
	writer := a.ErrOut
	if writer == nil {
		writer = a.Out
	}
	fmt.Fprintf(writer, "Google account: %s\n", account)
	fmt.Fprintf(writer, "Planned staging folder: %s\n", profile.StagingFolderName)
	fmt.Fprintf(writer, "Planned review folder: %s\n", profile.ReviewFolderName)
	folders, err := clients.Drive.EnsureFolders(ctx, googleapi.FolderSpec{
		WorkspaceID:     cfg.WorkspaceID,
		Profile:         profile.Name,
		Account:         account,
		ParentID:        profile.ParentFolderID,
		StagingID:       profile.StagingFolderID,
		ReviewID:        profile.ReviewFolderID,
		StoredStagingID: publishState.Folders.StagingID,
		StoredReviewID:  publishState.Folders.ReviewID,
		StagingName:     profile.StagingFolderName,
		ReviewName:      profile.ReviewFolderName,
	})
	if err != nil {
		return googleError(err)
	}
	publishState.Folders.StagingID = folders.Staging.ID
	publishState.Folders.ReviewID = folders.Review.ID
	publishState.Account = account
	reportFolderResolution(writer, "staging", profile.StagingFolderName, folders.Staging)
	reportFolderResolution(writer, "review", profile.ReviewFolderName, folders.Review)
	if err := store.Save(publishState); err != nil {
		return WrapError(ClassPartial, "state_save_failed", err.Error(), err)
	}
	fmt.Fprintf(a.Out, "Staging folder: %s (%s)\n", folders.Staging.Name, folders.Staging.ID)
	fmt.Fprintf(a.Out, "Review folder: %s (%s)\n", folders.Review.Name, folders.Review.ID)
	return nil
}

func reportFolderResolution(writer io.Writer, role, configuredName string, folder googleapi.Folder) {
	resolution := folder.Resolution
	if resolution == "" {
		resolution = "resolved"
	}
	label := strings.ToUpper(role[:1]) + role[1:]
	fmt.Fprintf(writer, "%s folder %s: %s (%s)\n", label, resolution, folder.Name, folder.ID)
	if configuredName != "" && folder.Name != "" && folder.Name != configuredName {
		fmt.Fprintf(writer, "Warning: existing %s folder name %q differs from configured display name %q; it was not renamed.\n", role, folder.Name, configuredName)
	}
}

func (a *Application) Validate(ctx context.Context, options ValidateOptions) error {
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return err
	}
	graph, err := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
	if err != nil {
		return WrapError(ClassValidation, "discovery_failed", err.Error(), err)
	}
	publications := publication.Resolve(cfg.Version, profile, graph)
	graph.Issues = append(graph.Issues, publications.Issues...)
	if options.Bundle != "" {
		target := publications.ByID[options.Bundle]
		if target == nil || target.Kind != publication.KindBundle {
			graph.Issues = append(graph.Issues, document.Issue{Severity: document.SeverityError, Code: "publication_selection_invalid", Source: options.Bundle, Message: fmt.Sprintf("bundle publication %q does not exist", options.Bundle)})
		}
	}
	resolvedFields, fieldIssues := resolvePublicationFields(cfg, profile, publications, options.CommonOptions)
	graph.Issues = append(graph.Issues, fieldIssues...)
	if cfg.Version >= config.ProjectVersion3 && !graph.HasErrors() {
		graph.Issues = append(graph.Issues, a.validatePublicationBuildInputs(ctx, cfg, profile, graph, publications, resolvedFields, options.Bundle)...)
	}
	options.JSON = cfg.Output.Format == "json"
	options.Quiet = cfg.Output.Quiet
	if profile.ReferenceDOCX == "" {
		// A reference document is optional for neutral profiles.
	} else if info, statErr := os.Stat(profile.ReferenceDOCX); statErr != nil || info.IsDir() {
		graph.Issues = append(graph.Issues, document.Issue{Severity: document.SeverityError, Code: "style_missing", Message: fmt.Sprintf("reference DOCX %q is unavailable", profile.ReferenceDOCX)})
	} else if styleErr := a.renderer(cfg).ValidateStyle(ctx, profile.ReferenceDOCX); styleErr != nil {
		graph.Issues = append(graph.Issues, document.Issue{Severity: document.SeverityError, Code: "style_invalid", Message: fmt.Sprintf("reference DOCX %q is invalid: %v", profile.ReferenceDOCX, styleErr)})
	}
	if options.Remote {
		if store == nil {
			return NewError(ClassCommand, "project_required", "remote validation requires a project config; use --config or run mdoc init")
		}
		remoteIssues, remoteErr := a.validateRemote(ctx, cfg, profile, store, graph, options.Bundle)
		if remoteErr != nil {
			return remoteErr
		}
		graph.Issues = append(graph.Issues, remoteIssues...)
	}
	graph.SortIssues()
	for index := range graph.Issues {
		if graph.Issues[index].Source == "" {
			graph.Issues[index].Source = options.Config
		}
	}
	validationReport := report.ValidationReport{Kind: "validation", Documents: len(graph.Documents), Entry: graph.EntryKey, Issues: graph.Issues, Valid: !graph.HasErrors(), FieldSummaries: fieldSummaries(resolvedFields)}
	if cfg.Version >= config.ProjectVersion3 {
		validationReport.SchemaVersion = 2
		validationReport.Entry = string(publications.Entry.Target)
		validationReport.Targets = publicationSummaries(publications, options.Bundle)
	}
	if graph.HasErrors() {
		_ = report.WriteValidation(a.Out, validationReport, options.JSON, options.Quiet)
		return NewError(ClassValidation, "local_validation_failed", "local validation failed; fix the reported errors and retry")
	}
	return report.WriteValidation(a.Out, validationReport, options.JSON, options.Quiet)
}
func (a *Application) load(ctx context.Context, options CommonOptions) (*config.Config, config.SelectedProfile, *state.Store, error) {
	overrides := options.Overrides
	if overrides == nil {
		overrides = map[string]any{}
	}
	if options.JSON {
		setConfigOverride(overrides, "output.format", "json")
	}
	if options.Quiet {
		setConfigOverride(overrides, "output.quiet", true)
	}
	runtimeConfig, err := config.Resolve(ctx, config.ResolverOptions{
		ProjectPath:         options.Config,
		Profile:             options.Profile,
		AllowMissingProject: options.AllowDirect,
		DirectSources:       options.Sources,
		Root:                options.Root,
		Entry:               options.Entry,
		Overrides:           overrides,
	})
	if err != nil {
		return nil, config.SelectedProfile{}, nil, WrapError(ClassCommand, "config_invalid", err.Error(), err)
	}
	cfg, profile := runtimeConfig.Legacy()
	if runtimeConfig.Direct {
		return cfg, profile, nil, nil
	}
	statePath := profile.StateFile
	if statePath == "" {
		statePath, err = state.DefaultPath(cfg.WorkspaceID, profile.Name)
		if err != nil {
			return nil, config.SelectedProfile{}, nil, WrapError(ClassCommand, "state_path_invalid", err.Error(), err)
		}
	}
	return cfg, profile, state.NewStore(statePath), nil
}

func setConfigOverride(target map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := target
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func (a *Application) documentRunner(cfg *config.Config) document.Runner {
	if a.DocumentRunner != nil {
		return a.DocumentRunner
	}
	profile := cfg.Profiles
	reader := "gfm"
	for _, selected := range profile {
		if selected.Reader != "" {
			reader = selected.Reader
		}
		break
	}
	return document.CommandRunner{Binary: cfg.PandocBinary, Reader: reader}
}

func (a *Application) renderer(cfg *config.Config) render.Renderer {
	renderer := a.Renderer
	if renderer.Pandoc == "" {
		renderer.Pandoc = cfg.PandocBinary
	}
	return renderer
}

func googleError(err error) error {
	var remote *googleapi.Error
	if errors.As(err, &remote) {
		switch remote.Kind {
		case googleapi.KindPermission:
			return WrapError(ClassAuthentication, "google_permission", remote.Error(), err)
		case googleapi.KindConflict:
			return WrapError(ClassConflict, "google_conflict", remote.Error(), err)
		}
	}
	return WrapError(ClassGoogleAPI, "google_api_failed", err.Error(), err)
}

func googleErrorIs(err error, kind googleapi.ErrorKind) bool {
	var remote *googleapi.Error
	return errors.As(err, &remote) && remote.Kind == kind
}

func (a *Application) validateRemote(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, store *state.Store, graph *document.Graph, bundleSelection ...string) ([]document.Issue, error) {
	issues := []document.Issue{}
	if a.Auth == nil {
		return nil, NewError(ClassAuthentication, "auth_unavailable", "Google authorization is unavailable; run `mdoc auth login`")
	}
	client, err := a.Auth.Client(ctx)
	if err != nil {
		return nil, WrapError(ClassAuthentication, "auth_client_failed", err.Error(), err)
	}
	clients, err := a.GoogleFactory.New(ctx, client)
	if err != nil {
		return nil, googleError(err)
	}
	publishState, err := store.Load()
	if errors.Is(err, state.ErrNotFound) {
		issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "state_missing", Message: "publish state is missing; run `mdoc setup`"})
		publishState = state.New(cfg.WorkspaceID, profile.Name)
	} else if err != nil {
		return nil, WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	}
	account, err := a.currentAccount(ctx)
	if err != nil {
		return nil, err
	}
	if accountErr := verifyAccountMatch(publishState, account); accountErr != nil {
		return nil, accountErr
	}
	folderIDs := map[string]string{}
	for _, role := range []string{"staging", "review"} {
		folders, err := clients.Drive.FindFolders(ctx, cfg.WorkspaceID, profile.Name, role)
		if err != nil {
			return nil, googleError(err)
		}
		if len(folders) != 1 {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "folder_invalid", Message: fmt.Sprintf("expected one %s folder; found %d; run `mdoc setup` or resolve duplicates", role, len(folders))})
			continue
		}
		folder := folders[0]
		folderIDs[role] = folder.ID
		expectedID := publishState.Folders.StagingID
		if role == "review" {
			expectedID = publishState.Folders.ReviewID
		}
		if folderErr := validateMappedDestinationFolder(folder, account, cfg, profile, expectedID, role); folderErr != nil {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "folder_mapping_mismatch", Message: fmt.Sprintf("%v; run `mdoc setup` or `mdoc state reconcile`", folderErr)})
		}
	}
	if folderIDs["staging"] != "" && folderIDs["staging"] == folderIDs["review"] {
		issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "folder_collision", Message: "staging and review folders resolve to the same Google folder; run `mdoc setup`"})
	}
	remoteFiles, err := clients.Drive.FindDocuments(ctx, map[string]string{"mdoc_workspace": cfg.WorkspaceID, "mdoc_profile": profile.Name})
	if err != nil {
		return nil, googleError(err)
	}
	if cfg.Version >= config.ProjectVersion3 {
		selected := ""
		if len(bundleSelection) > 0 {
			selected = bundleSelection[0]
		}
		return a.validateExplicitRemoteTargets(ctx, cfg, profile, publishState, clients, remoteFiles, folderIDs, issues, selected)
	}
	activeSetIDs := map[string]bool{}
	for _, stored := range publishState.Documents {
		if stored.ActiveTarget != nil {
			activeSetIDs[stored.ActiveTarget.ReviewSetID] = true
		}
	}
	remoteSets := map[string][]state.RemoteTarget{}
	for _, file := range remoteFiles {
		setID := file.AppProperties["mdoc_review_set"]
		if !activeSetIDs[setID] {
			continue
		}
		remoteSets[setID] = append(remoteSets[setID], remoteTargetMetadata(file))
	}
	setIDs := make([]string, 0, len(activeSetIDs))
	for setID := range activeSetIDs {
		setIDs = append(setIDs, setID)
	}
	sort.Strings(setIDs)
	for _, setID := range setIDs {
		if err := state.ValidateCompleteSet(remoteSets[setID], folderIDs["review"]); err != nil {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "review_set_incomplete", Message: fmt.Sprintf("active review set %s is not complete: %v", setID, err)})
		}
	}
	keys := make([]string, 0, len(publishState.Documents))
	for key := range publishState.Documents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		stored := publishState.Documents[key]
		if stored.ActiveTarget == nil {
			continue
		}
		file, err := clients.Drive.GetFile(ctx, stored.ActiveTarget.FileID)
		if err != nil {
			if googleErrorIs(err, googleapi.KindNotFound) {
				issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_missing", Source: key, Message: "active Google Doc was not found"})
				continue
			}
			return nil, googleError(err)
		}
		if file.Trashed {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_trashed", Source: key, Message: "active Google Doc is trashed"})
		}
		expectedSetSize, expectedSetErr := strconv.Atoi(file.AppProperties["mdoc_expected_set_size"])
		remoteSource, remoteSourceErr := sourcekey.Normalize(file.AppProperties["mdoc_source"])
		metadataMatches := file.MimeType == googleapi.DocumentMimeType &&
			file.AppProperties["mdoc_workspace"] == cfg.WorkspaceID &&
			file.AppProperties["mdoc_profile"] == profile.Name &&
			remoteSourceErr == nil && remoteSource == key &&
			file.AppProperties["mdoc_review_set"] == stored.ActiveTarget.ReviewSetID &&
			file.AppProperties["mdoc_generation"] == strconv.Itoa(stored.ActiveTarget.Generation) &&
			file.AppProperties["mdoc_operation"] == stored.ActiveTarget.OperationID &&
			file.AppProperties["mdoc_status"] == stored.ActiveTarget.PublishStatus &&
			expectedSetErr == nil && expectedSetSize > 0 &&
			(stored.ActiveTarget.URL == "" || file.URL == stored.ActiveTarget.URL)
		if !metadataMatches {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_metadata_mismatch", Source: key, Message: "active Google Doc metadata does not match local state"})
		}
		if reviewID := folderIDs["review"]; reviewID != "" && !contains(file.Parents, reviewID) {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_folder_mismatch", Source: key, Message: "active Google Doc is outside the configured review folder"})
		}
		if stored.ActiveTarget.RemoteVersion != "" && file.Version != stored.ActiveTarget.RemoteVersion {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "remote_change", Source: key, Message: "Drive version changed after the last publish"})
		}
		if graph != nil {
			if local := graph.ByKey[key]; local != nil && file.Name != "" && file.Name != local.Title {
				issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "remote_rename", Source: key, Message: fmt.Sprintf("Google Doc was renamed to %q", file.Name)})
			}
		}
		comments, err := clients.Drive.ListComments(ctx, file.ID)
		if err != nil {
			return nil, googleError(err)
		}
		if comments.Unresolved > 0 {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "unresolved_comments", Source: key, Message: fmt.Sprintf("%d unresolved comment threads remain", comments.Unresolved)})
		}
		remoteDocument, err := clients.Docs.GetDocument(ctx, file.ID)
		if err != nil {
			return nil, googleError(err)
		}
		if remoteDocument.SuggestionsDetected {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "unresolved_suggestions", Source: key, Message: "unresolved suggestions remain"})
		}
		if stored.ActiveTarget.DocsRevision != "" && remoteDocument.RevisionID != stored.ActiveTarget.DocsRevision {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "docs_revision_change", Source: key, Message: "Docs revision changed after the last publish"})
		}
	}
	return issues, nil
}

func (a *Application) validateExplicitRemoteTargets(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, publishState *state.State, clients googleapi.Clients, remoteFiles []googleapi.File, folderIDs map[string]string, issues []document.Issue, bundleSelection string) ([]document.Issue, error) {
	selectedKey := ""
	if bundleSelection != "" {
		selectedKey = "publication:" + bundleSelection
	}
	activeSetTargets := map[string][]string{}
	for key, stored := range publishState.Targets {
		if selectedKey != "" && key != selectedKey || stored.ActiveTarget == nil || !strings.HasPrefix(key, "publication:") {
			continue
		}
		setID := stored.ActiveTarget.ReviewSetID
		activeSetTargets[setID] = append(activeSetTargets[setID], key)
	}
	remoteSets := map[string][]state.RemoteTarget{}
	for _, file := range remoteFiles {
		setID := file.AppProperties["mdoc_review_set"]
		if len(activeSetTargets[setID]) > 0 {
			remoteSets[setID] = append(remoteSets[setID], remoteTargetMetadata(file))
		}
	}
	for setID, targetKeys := range activeSetTargets {
		if err := state.ValidateCompleteSet(remoteSets[setID], folderIDs["review"]); err != nil {
			for _, targetKey := range targetKeys {
				issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "review_set_incomplete", Source: targetKey, Message: fmt.Sprintf("active review set %s is not complete: %v", setID, err)})
			}
		}
	}
	for key, stored := range publishState.Targets {
		if selectedKey != "" && key != selectedKey || stored.ActiveTarget == nil || !strings.HasPrefix(key, "publication:") {
			continue
		}
		active := stored.ActiveTarget
		file, err := clients.Drive.GetFile(ctx, active.FileID)
		if err != nil {
			if googleErrorIs(err, googleapi.KindNotFound) {
				issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_missing", Source: key, Message: "active Google Doc was not found"})
				continue
			}
			return nil, googleError(err)
		}
		properties := file.AppProperties
		expectedSize, expectedErr := strconv.Atoi(properties["mdoc_expected_set_size"])
		generation, generationErr := strconv.Atoi(properties["mdoc_generation"])
		metadataMatches := file.MimeType == googleapi.DocumentMimeType && properties["mdoc_workspace"] == cfg.WorkspaceID && properties["mdoc_profile"] == profile.Name && properties["mdoc_target_key"] == key && properties["mdoc_publication"] == stored.PublicationID && properties["mdoc_publication_kind"] == stored.PublicationKind && properties["mdoc_source"] == stored.Source && generationErr == nil && generation == active.Generation && properties["mdoc_review_set"] == active.ReviewSetID && properties["mdoc_operation"] == active.OperationID && properties["mdoc_status"] == active.PublishStatus && expectedErr == nil && expectedSize > 0 && (active.URL == "" || active.URL == file.URL)
		if !metadataMatches {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_metadata_mismatch", Source: key, Message: "active Google Doc metadata does not match local publication state"})
		}
		if file.Trashed {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_trashed", Source: key, Message: "active Google Doc is trashed"})
		}
		if reviewID := folderIDs["review"]; reviewID != "" && !contains(file.Parents, reviewID) {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "target_folder_mismatch", Source: key, Message: "active Google Doc is outside the configured review folder"})
		}
		comments, err := clients.Drive.ListComments(ctx, file.ID)
		if err != nil {
			return nil, googleError(err)
		}
		if comments.Unresolved > 0 {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "unresolved_comments", Source: key, Message: fmt.Sprintf("%d unresolved comment threads remain", comments.Unresolved)})
		}
		remoteDocument, err := clients.Docs.GetDocument(ctx, file.ID)
		if err != nil {
			return nil, googleError(err)
		}
		if remoteDocument.SuggestionsDetected {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "unresolved_suggestions", Source: key, Message: "unresolved suggestions remain"})
		}
		if active.RemoteVersion != "" && file.Version != active.RemoteVersion {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "remote_change", Source: key, Message: "Drive version changed after review activation"})
		}
		expectedRevision := active.DocsRevision
		if active.ReviewSnapshot != nil {
			expectedRevision = active.ReviewSnapshot.ActivatedRevision
		}
		if expectedRevision != "" && remoteDocument.RevisionID != expectedRevision {
			issues = append(issues, document.Issue{Severity: document.SeverityWarning, Code: "docs_revision_change", Source: key, Message: "Docs revision changed after review activation"})
		}
	}
	return issues, nil
}

func remoteTargetMetadata(file googleapi.File) state.RemoteTarget {
	properties := file.AppProperties
	generation, _ := strconv.Atoi(properties["mdoc_generation"])
	expected, _ := strconv.Atoi(properties["mdoc_expected_set_size"])
	parent := ""
	if len(file.Parents) > 0 {
		parent = file.Parents[0]
	}
	remoteSource, _ := sourcekey.Normalize(properties["mdoc_source"])
	targetKey := properties["mdoc_target_key"]
	if targetKey == "" && remoteSource != "" {
		targetKey = "source:" + remoteSource
	}
	return state.RemoteTarget{
		Target: state.Target{
			FileID: file.ID, URL: file.URL, ReviewSetID: properties["mdoc_review_set"], Generation: generation,
			OperationID: properties["mdoc_operation"], PublishStatus: properties["mdoc_status"], RemoteVersion: file.Version,
			ModifiedTime: file.ModifiedTime, PublishedAt: file.ModifiedTime,
		},
		SourceKey: remoteSource, TargetKey: targetKey, PublicationID: properties["mdoc_publication"], PublicationKind: properties["mdoc_publication_kind"], ExpectedSetSize: expected, ParentID: parent,
	}
}

func openSystemBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}
