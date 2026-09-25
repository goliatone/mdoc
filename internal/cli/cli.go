package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/goliatone/mdoc/internal/app"
	"github.com/goliatone/mdoc/internal/auth"
	mdocconfig "github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/probe"
)

type AuthService interface {
	Login(context.Context) (auth.LoginResult, error)
	Status(context.Context) (auth.Status, error)
	Logout(context.Context) error
}

type ProbeService interface {
	Google(context.Context, probe.GoogleOptions) (probe.GoogleResult, error)
	Inspect(context.Context, string) (probe.InspectResult, error)
	Cleanup(context.Context, string) error
}

type Runtime struct {
	Context  context.Context
	App      app.Service
	Auth     AuthService
	Probe    ProbeService
	Out      io.Writer
	Explicit map[string]bool
}

type CLI struct {
	Version  kong.VersionFlag `name:"version" help:"Print the mdoc version and exit."`
	Init     InitCmd          `cmd:"" help:"Create a workspace configuration."`
	Config   ConfigCmd        `cmd:"" help:"Inspect effective configuration."`
	Profile  ProfileCmd       `cmd:"" help:"Create and inspect project profiles."`
	Auth     AuthCmd          `cmd:"" help:"Manage Google authorization."`
	Setup    SetupCmd         `cmd:"" help:"Create or find app owned Google folders."`
	Validate ValidateCmd      `cmd:"" help:"Validate configuration and source documents."`
	Publish  PublishCmd       `cmd:"" help:"Plan and publish documents."`
	Plan     PlanCmd          `cmd:"" help:"Build a read only publish plan."`
	Status   StatusCmd        `cmd:"" help:"Show local, remote, and review state."`
	Doctor   DoctorCmd        `cmd:"" help:"Check local and project capabilities without publishing."`
	State    StateCmd         `cmd:"" help:"Manage local publish state."`
	Open     OpenCmd          `cmd:"" help:"Open a published Google Doc."`
	Review   ReviewCmd        `cmd:"" help:"Read review changes without modifying sources or Google Docs."`
	Probe    ProbeCmd         `cmd:"" hidden:"" help:"Run disposable feasibility probes."`
}

var Version = "dev"

type CommonFlags struct {
	Config        string   `name:"config" help:"Explicit project config path. By default mdoc searches parent directories."`
	Profile       string   `name:"profile" help:"Publish profile name."`
	JSON          bool     `name:"json" help:"Write machine readable JSON."`
	Quiet         bool     `name:"quiet" help:"Only write errors and the entry URL."`
	Root          string   `name:"root" help:"Project or direct mode root override."`
	Sources       []string `name:"source" help:"Source file, directory, or pattern. Repeat for more values."`
	Entry         string   `name:"entry" help:"Entry document override."`
	ReferenceDOCX string   `name:"reference-docx" help:"Reference DOCX override. Use an empty value to clear it."`
	StateFile     string   `name:"state" help:"Publish state path override."`
	PandocBinary  string   `name:"pandoc" help:"Pandoc binary override."`
	Output        string   `name:"output" enum:"human,json" default:"human" help:"Output format override."`
	TitlePolicy   string   `name:"title-policy" enum:"required,optional" default:"required" help:"Title validation override."`
	HeadingJumps  string   `name:"heading-jumps" enum:"error,warning,allow" default:"error" help:"Heading jump policy override."`
	LinkPolicy    string   `name:"link-policy" enum:"error,warning,preserve" default:"error" help:"Unpublished Markdown link policy override."`
	FieldFiles    []string `name:"fields-file" help:"Local YAML or JSON field file. Repeat to apply in order."`
	Fields        []string `name:"field" help:"Field override as <json-pointer>=<json-value>. Repeat to apply in order."`
	Bundle        string   `name:"bundle" help:"Select one bundle publication by ID."`
}

func (flags CommonFlags) options(explicit map[string]bool) app.CommonOptions {
	overrides := map[string]any{}
	set := func(path string, value any) { setOverride(overrides, path, value) }
	if explicit["root"] {
		set("root", flags.Root)
	}
	if explicit["source"] {
		set("profile.sources.include", clearableList(flags.Sources))
	}
	if explicit["entry"] {
		set("profile.entry", flags.Entry)
	}
	if explicit["reference-docx"] {
		set("profile.style.reference_docx", flags.ReferenceDOCX)
	}
	if explicit["state"] {
		set("profile.state_file", flags.StateFile)
	}
	if explicit["pandoc"] {
		set("pandoc_binary", flags.PandocBinary)
	}
	if explicit["output"] {
		set("output.format", flags.Output)
	}
	if explicit["json"] {
		format := "human"
		if flags.JSON {
			format = "json"
		}
		set("output.format", format)
	}
	if explicit["quiet"] {
		set("output.quiet", flags.Quiet)
	}
	if explicit["title-policy"] {
		set("profile.validation.title", flags.TitlePolicy)
	}
	if explicit["heading-jumps"] {
		set("profile.validation.heading_jumps", flags.HeadingJumps)
	}
	if explicit["link-policy"] {
		set("profile.validation.unpublished_markdown_links", flags.LinkPolicy)
	}
	return app.CommonOptions{Config: flags.Config, Profile: flags.Profile, JSON: flags.JSON, Quiet: flags.Quiet, Overrides: overrides, FieldFiles: append([]string(nil), flags.FieldFiles...), FieldAssignments: append([]string(nil), flags.Fields...), Bundle: flags.Bundle}
}

type InitCmd struct {
	Config  string `name:"config" default:".mdoc.yaml" help:"Config path to create."`
	Force   bool   `help:"Replace an existing config."`
	Profile string `name:"profile" default:"default" help:"Initial profile name."`
}

func (command InitCmd) Run(runtime *Runtime) error {
	return runtime.App.Init(runtime.Context, app.InitOptions{Config: command.Config, Force: command.Force, Profile: command.Profile})
}

type SetupCmd struct {
	CommonFlags
	StagingFolderName string `name:"staging-folder-name" help:"Create-time staging folder display name."`
	ReviewFolderName  string `name:"review-folder-name" help:"Create-time review folder display name."`
	ParentFolderID    string `name:"parent-folder-id" help:"My Drive parent folder ID."`
	StagingFolderID   string `name:"staging-folder-id" help:"Existing staging folder ID to adopt."`
	ReviewFolderID    string `name:"review-folder-id" help:"Existing review folder ID to adopt."`
}

func (command SetupCmd) Run(runtime *Runtime) error {
	options := command.options(runtime.Explicit)
	for name, override := range map[string]struct {
		path  string
		value string
	}{
		"staging-folder-name": {"profile.destination.staging_folder_name", command.StagingFolderName},
		"review-folder-name":  {"profile.destination.review_folder_name", command.ReviewFolderName},
		"parent-folder-id":    {"profile.destination.parent_folder_id", command.ParentFolderID},
		"staging-folder-id":   {"profile.destination.staging_folder_id", command.StagingFolderID},
		"review-folder-id":    {"profile.destination.review_folder_id", command.ReviewFolderID},
	} {
		if runtime.Explicit[name] {
			setOverride(options.Overrides, override.path, override.value)
		}
	}
	return runtime.App.Setup(runtime.Context, options)
}

type ValidateCmd struct {
	CommonFlags
	Remote        bool     `help:"Include Google access and target checks."`
	DirectSources []string `arg:"" optional:"" name:"source" help:"File, directory, or pattern for direct local mode."`
}

func (command ValidateCmd) Run(runtime *Runtime) error {
	options := command.options(runtime.Explicit)
	options.AllowDirect = true
	options.Sources = append([]string(nil), command.DirectSources...)
	options.Root = command.Root
	options.Entry = command.Entry
	return runtime.App.Validate(runtime.Context, app.ValidateOptions{CommonOptions: options, Remote: command.Remote})
}

type PublishCmd struct {
	CommonFlags
	DryRun    bool     `name:"dry-run" help:"Show the plan without remote writes."`
	Files     []string `name:"file" help:"Publish only this source. Repeat for more files."`
	NewReview bool     `name:"new-review" help:"Create a new immutable review generation."`
}

func (command PublishCmd) Run(runtime *Runtime) error {
	return runtime.App.Publish(runtime.Context, app.PublishOptions{CommonOptions: command.options(runtime.Explicit), DryRun: command.DryRun, Files: command.Files, NewReview: command.NewReview})
}

type PlanCmd struct {
	CommonFlags
	Files     []string `name:"file" help:"Plan only this source. Repeat for more files."`
	NewReview bool     `name:"new-review" help:"Plan a new immutable review generation."`
}

func (command PlanCmd) Run(runtime *Runtime) error {
	return runtime.App.Plan(runtime.Context, app.PublishOptions{CommonOptions: command.options(runtime.Explicit), DryRun: true, Files: command.Files, NewReview: command.NewReview})
}

type StatusCmd struct {
	CommonFlags
	Remote        bool     `help:"Include remote read only checks."`
	DirectSources []string `arg:"" optional:"" name:"source" help:"File, directory, or pattern for direct local mode."`
}

func (command StatusCmd) Run(runtime *Runtime) error {
	options := command.options(runtime.Explicit)
	options.AllowDirect = true
	options.Sources = append([]string(nil), command.DirectSources...)
	options.Root = command.Root
	options.Entry = command.Entry
	options.Remote = command.Remote
	return runtime.App.Status(runtime.Context, options)
}

type DoctorCmd struct {
	CommonFlags
	DirectSources []string `arg:"" optional:"" name:"source" help:"File, directory, or pattern for direct local mode."`
}

func (command DoctorCmd) Run(runtime *Runtime) error {
	options := command.options(runtime.Explicit)
	options.AllowDirect = true
	options.Sources = append([]string(nil), command.DirectSources...)
	options.Root = command.Root
	options.Entry = command.Entry
	return runtime.App.Doctor(runtime.Context, app.DoctorOptions{CommonOptions: options})
}

type ConfigCmd struct {
	Show ConfigShowCmd `cmd:"" help:"Show effective values and their sources."`
}

type ConfigShowCmd struct{ CommonFlags }

func (command ConfigShowCmd) Run(runtime *Runtime) error {
	options := command.options(runtime.Explicit)
	resolved, err := mdocconfig.Resolve(runtime.Context, mdocconfig.ResolverOptions{ProjectPath: command.Config, Profile: command.Profile, Overrides: options.Overrides})
	if err != nil {
		return app.WrapError(app.ClassCommand, "config_invalid", err.Error(), err)
	}
	if resolved.Output.Format == "json" {
		redacted := resolved.Redacted()
		return writeJSON(runtime.Out, redacted)
	}
	fmt.Fprintf(runtime.Out, "Project config: %s\n", resolved.ProjectPath)
	if resolved.GlobalPath != "" {
		fmt.Fprintf(runtime.Out, "Global config: %s\n", resolved.GlobalPath)
	}
	fmt.Fprintf(runtime.Out, "Project identity: %s\n", resolved.Identity())
	fmt.Fprintf(runtime.Out, "Profile: %s\n", resolved.ProfileName)
	fmt.Fprintf(runtime.Out, "Root: %s (%s)\n", resolved.Root, resolved.Origins["root"].Kind)
	fmt.Fprintf(runtime.Out, "Pandoc: %s (%s)\n", resolved.PandocBinary, resolved.Origins["pandoc_binary"].Kind)
	fmt.Fprintln(runtime.Out, "Fields: values are redacted; field values become document content and must not contain secrets")
	return nil
}

type ProfileCmd struct {
	Add  ProfileAddCmd  `cmd:"" help:"Add a neutral local profile."`
	List ProfileListCmd `cmd:"" help:"List configured profiles."`
	Show ProfileShowCmd `cmd:"" help:"Show one effective profile."`
}

type ProfileAddCmd struct {
	Config string `name:"config" help:"Explicit project config path."`
	Name   string `arg:"" name:"name" help:"Profile name."`
	Force  bool   `help:"Replace an existing profile."`
}

func (command ProfileAddCmd) Run(runtime *Runtime) error {
	path, _, err := mdocconfig.ListProfiles(command.Config, "")
	if err != nil {
		return app.WrapError(app.ClassCommand, "config_invalid", err.Error(), err)
	}
	if err := mdocconfig.AddProfile(path, command.Name, command.Force); err != nil {
		return app.WrapError(app.ClassCommand, "profile_add_failed", err.Error(), err)
	}
	fmt.Fprintf(runtime.Out, "Added profile %s to %s\n", command.Name, path)
	return nil
}

type ProfileListCmd struct {
	Config string `name:"config" help:"Explicit project config path."`
	JSON   bool   `name:"json" help:"Write machine readable JSON."`
	Output string `name:"output" enum:"human,json" default:"human" help:"Output format override."`
	Quiet  bool   `name:"quiet" help:"Suppress successful output."`
}

func (command ProfileListCmd) Run(runtime *Runtime) error {
	overrides := profileOutputOverrides(runtime.Explicit, command.JSON, command.Output, command.Quiet)
	output, err := mdocconfig.ResolveOutput(mdocconfig.ResolverOptions{ProjectPath: command.Config, Overrides: overrides})
	if err != nil {
		return app.WrapError(app.ClassCommand, "config_invalid", err.Error(), err)
	}
	path, names, err := mdocconfig.ListProfiles(command.Config, "")
	if err != nil {
		return app.WrapError(app.ClassCommand, "config_invalid", err.Error(), err)
	}
	if output.Quiet {
		return nil
	}
	if output.Format == "json" {
		return writeJSON(runtime.Out, map[string]any{"project_path": path, "profiles": names})
	}
	for _, name := range names {
		fmt.Fprintln(runtime.Out, name)
	}
	return nil
}

type ProfileShowCmd struct {
	Config string `name:"config" help:"Explicit project config path."`
	Name   string `arg:"" optional:"" name:"name" help:"Profile name. Selection rules apply when omitted."`
	JSON   bool   `name:"json" help:"Write machine readable JSON."`
	Output string `name:"output" enum:"human,json" default:"human" help:"Output format override."`
	Quiet  bool   `name:"quiet" help:"Suppress successful output."`
}

func (command ProfileShowCmd) Run(runtime *Runtime) error {
	overrides := profileOutputOverrides(runtime.Explicit, command.JSON, command.Output, command.Quiet)
	resolved, err := mdocconfig.Resolve(runtime.Context, mdocconfig.ResolverOptions{ProjectPath: command.Config, Profile: command.Name, Overrides: overrides})
	if err != nil {
		return app.WrapError(app.ClassCommand, "profile_invalid", err.Error(), err)
	}
	if resolved.Output.Quiet {
		return nil
	}
	if resolved.Output.Format == "json" {
		redacted := resolved.Redacted()
		return writeJSON(runtime.Out, map[string]any{"name": resolved.ProfileName, "profile": redacted.Profile, "origins": resolved.Origins})
	}
	fmt.Fprintf(runtime.Out, "Profile: %s\n", resolved.ProfileName)
	fmt.Fprintf(runtime.Out, "Sources: %s\n", strings.Join(resolved.Profile.Sources.Include, ", "))
	fmt.Fprintf(runtime.Out, "Entry: %s\n", resolved.Profile.Entry)
	fmt.Fprintf(runtime.Out, "Staging folder: %s\n", resolved.Profile.Destination.StagingFolderName)
	fmt.Fprintf(runtime.Out, "Review folder: %s\n", resolved.Profile.Destination.ReviewFolderName)
	fmt.Fprintln(runtime.Out, "Fields: values are redacted; field values become document content and must not contain secrets")
	return nil
}

func profileOutputOverrides(explicit map[string]bool, jsonFlag bool, outputFlag string, quietFlag bool) map[string]any {
	overrides := map[string]any{}
	if explicit["output"] {
		setOverride(overrides, "output.format", outputFlag)
	}
	if explicit["json"] {
		format := "human"
		if jsonFlag {
			format = "json"
		}
		setOverride(overrides, "output.format", format)
	}
	if explicit["quiet"] {
		setOverride(overrides, "output.quiet", quietFlag)
	}
	return overrides
}

type StateCmd struct {
	Reconcile StateReconcileCmd `cmd:"" help:"Rebuild state from app metadata."`
	Remap     StateRemapCmd     `cmd:"" help:"Move state after an intentional local rename."`
}

type StateReconcileCmd struct {
	CommonFlags
	AbandonOperation bool `name:"abandon-operation" help:"Clear an unsafe publish journal after remote recovery proves no review set is pending."`
}

func (command StateReconcileCmd) Run(runtime *Runtime) error {
	return runtime.App.Reconcile(runtime.Context, app.ReconcileOptions{CommonOptions: command.options(runtime.Explicit), AbandonOperation: command.AbandonOperation})
}

type StateRemapCmd struct {
	CommonFlags
	From string `required:"" help:"Current normalized source key."`
	To   string `required:"" help:"New normalized source key."`
}

func (command StateRemapCmd) Run(runtime *Runtime) error {
	return runtime.App.Remap(runtime.Context, app.RemapOptions{CommonOptions: command.options(runtime.Explicit), From: command.From, To: command.To})
}

type OpenCmd struct {
	CommonFlags
	File string `arg:"" optional:"" help:"Source to open. Defaults to the entry document."`
}

func (command OpenCmd) Run(runtime *Runtime) error {
	return runtime.App.Open(runtime.Context, app.OpenOptions{CommonOptions: command.options(runtime.Explicit), File: command.File})
}

type ReviewCmd struct {
	Pull      ReviewPullCmd      `cmd:"" help:"Build a proposed patch from the active Google Docs review target."`
	Bootstrap ReviewBootstrapCmd `cmd:"" help:"Build a proposed patch from an explicitly selected external Google Doc and a sealed baseline."`
}

type ReviewPullCmd struct {
	Publication string `arg:"" optional:"" name:"publication" help:"Publication ID. Defaults to the entry publication."`
	Config      string `name:"config" help:"Explicit project config path. By default mdoc searches parent directories."`
	Profile     string `name:"profile" help:"Publish profile name."`
	StateFile   string `name:"state" help:"Publish state path override."`
	JSON        bool   `name:"json" help:"Write machine readable JSON."`
	Quiet       bool   `name:"quiet" help:"Only write errors."`
	Output      string `name:"output" help:"Write the unified patch to this file." type:"path"`
	Overwrite   bool   `name:"overwrite" help:"Replace an existing patch output file."`
}

func (command ReviewPullCmd) Run(runtime *Runtime) error {
	if command.Overwrite && command.Output == "" {
		return &UsageError{Err: errors.New("--overwrite requires --output")}
	}
	overrides := map[string]any{}
	if runtime.Explicit["state"] {
		setOverride(overrides, "profile.state_file", command.StateFile)
	}
	if command.JSON {
		setOverride(overrides, "output.format", "json")
	}
	if command.Quiet {
		setOverride(overrides, "output.quiet", true)
	}
	return runtime.App.ReviewPull(runtime.Context, app.ReviewPullOptions{
		CommonOptions: app.CommonOptions{
			Config: command.Config, Profile: command.Profile, JSON: command.JSON, Quiet: command.Quiet, Overrides: overrides,
		},
		Publication: command.Publication,
		PatchOutput: command.Output,
		Overwrite:   command.Overwrite,
	})
}

type ReviewBootstrapCmd struct {
	ReviewPullCmd
	Document     string `name:"document" required:"" help:"External Google Docs URL or document ID to compare with the sealed baseline."`
	ReviewExport string `name:"review-export" type:"existingfile" help:"Local Markdown export of the selected Google Doc. Use when mdoc cannot read that document through Google Drive."`
	Partial      bool   `name:"partial" help:"Write the safe subset even when other review changes remain blocked. Requires --output."`
}

func (command ReviewBootstrapCmd) Run(runtime *Runtime) error {
	if command.Overwrite && command.Output == "" {
		return &UsageError{Err: errors.New("--overwrite requires --output")}
	}
	if command.Partial && command.Output == "" {
		return &UsageError{Err: errors.New("--partial requires --output")}
	}
	overrides := map[string]any{}
	if runtime.Explicit["state"] {
		setOverride(overrides, "profile.state_file", command.StateFile)
	}
	if command.JSON {
		setOverride(overrides, "output.format", "json")
	}
	if command.Quiet {
		setOverride(overrides, "output.quiet", true)
	}
	return runtime.App.ReviewBootstrap(runtime.Context, app.ReviewBootstrapOptions{
		ReviewPullOptions: app.ReviewPullOptions{
			CommonOptions: app.CommonOptions{
				Config: command.Config, Profile: command.Profile, JSON: command.JSON, Quiet: command.Quiet, Overrides: overrides,
			},
			Publication: command.Publication,
			PatchOutput: command.Output,
			Overwrite:   command.Overwrite,
		},
		Document:     command.Document,
		ReviewExport: command.ReviewExport,
		Partial:      command.Partial,
	})
}

type AuthCmd struct {
	Login  AuthLoginCmd  `cmd:"" help:"Sign in to Google."`
	Status AuthStatusCmd `cmd:"" help:"Show Google authorization status."`
	Logout AuthLogoutCmd `cmd:"" help:"Remove the local Google token."`
}

type AuthLoginCmd struct{}

func (AuthLoginCmd) Run(runtime *Runtime) error {
	result, err := runtime.Auth.Login(runtime.Context)
	if err != nil {
		return app.WrapError(app.ClassAuthentication, "auth_login_failed", err.Error(), err)
	}
	fmt.Fprintln(runtime.Out, "Google authorization completed.")
	fmt.Fprintf(runtime.Out, "Token: %s\n", result.TokenPath)
	if !result.ExpiresAt.IsZero() {
		fmt.Fprintf(runtime.Out, "Access token expires: %s\n", result.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

type AuthStatusCmd struct{}

func (AuthStatusCmd) Run(runtime *Runtime) error {
	result, err := runtime.Auth.Status(runtime.Context)
	if err != nil {
		return app.WrapError(app.ClassAuthentication, "auth_status_failed", err.Error(), err)
	}
	fmt.Fprintf(runtime.Out, "OAuth configured: %s\n", yesNo(result.Configured))
	if len(result.Missing) > 0 {
		fmt.Fprintf(runtime.Out, "Missing: %s\n", strings.Join(result.Missing, ", "))
	}
	fmt.Fprintf(runtime.Out, "Signed in: %s\n", yesNo(result.SignedIn))
	fmt.Fprintf(runtime.Out, "Token: %s\n", result.TokenPath)
	if result.Account != "" {
		fmt.Fprintf(runtime.Out, "Account: %s\n", result.Account)
	}
	if result.SignedIn {
		fmt.Fprintf(runtime.Out, "Refreshable: %s\n", yesNo(result.Refreshable))
		if !result.ExpiresAt.IsZero() {
			fmt.Fprintf(runtime.Out, "Access token expires: %s\n", result.ExpiresAt.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

type AuthLogoutCmd struct{}

func (AuthLogoutCmd) Run(runtime *Runtime) error {
	if err := runtime.Auth.Logout(runtime.Context); err != nil {
		return app.WrapError(app.ClassAuthentication, "auth_logout_failed", err.Error(), err)
	}
	fmt.Fprintln(runtime.Out, "Local Google token removed.")
	return nil
}

type ProbeCmd struct {
	Google  ProbeGoogleCmd  `cmd:"" help:"Create and inspect a disposable Google document."`
	Inspect ProbeInspectCmd `cmd:"" help:"Inspect a retained disposable Google document."`
	Cleanup ProbeCleanupCmd `cmd:"" help:"Trash a retained disposable probe folder."`
}

type ProbeGoogleCmd struct {
	Fixture       string `help:"Synthetic Markdown fixture." default:"testdata/feasibility/google-import.md" type:"existingfile"`
	ReferenceDOCX string `name:"reference-docx" help:"Optional approved reference DOCX." type:"existingfile" optional:""`
	Keep          bool   `help:"Keep the disposable folder for manual inspection."`
}

func (command ProbeGoogleCmd) Run(runtime *Runtime) error {
	if runtime.Probe == nil {
		return errors.New("google probe service is unavailable")
	}
	result, err := runtime.Probe.Google(runtime.Context, probe.GoogleOptions{
		Fixture:       command.Fixture,
		ReferenceDOCX: command.ReferenceDOCX,
		Keep:          command.Keep,
	})
	if err != nil {
		return err
	}
	return writeJSON(runtime.Out, result)
}

type ProbeInspectCmd struct {
	DocumentID string `arg:"" name:"document-id" help:"Google document ID."`
}

func (command ProbeInspectCmd) Run(runtime *Runtime) error {
	if runtime.Probe == nil {
		return errors.New("google probe service is unavailable")
	}
	result, err := runtime.Probe.Inspect(runtime.Context, command.DocumentID)
	if err != nil {
		return err
	}
	return writeJSON(runtime.Out, result)
}

type ProbeCleanupCmd struct {
	FolderID string `arg:"" name:"folder-id" help:"Disposable root folder ID."`
}

func (command ProbeCleanupCmd) Run(runtime *Runtime) error {
	if runtime.Probe == nil {
		return errors.New("google probe service is unavailable")
	}
	if err := runtime.Probe.Cleanup(runtime.Context, command.FolderID); err != nil {
		return err
	}
	fmt.Fprintln(runtime.Out, "Disposable Google probe folder moved to trash.")
	return nil
}

type UsageError struct {
	Err error
}

func (e *UsageError) Error() string {
	return e.Err.Error()
}

func (e *UsageError) Unwrap() error {
	return e.Err
}

func Execute(args []string, stdout, stderr io.Writer, authService AuthService, services ...any) error {
	var probeService ProbeService
	var appService app.Service
	for _, candidate := range services {
		if service, ok := candidate.(ProbeService); ok {
			probeService = service
		}
		if service, ok := candidate.(app.Service); ok {
			appService = service
		}
	}
	if authService == nil {
		service, err := auth.NewService(auth.ServiceOptions{PromptWriter: stderr})
		if err != nil {
			return err
		}
		authService = service
		if probeService == nil {
			probeService = probe.NewService(service)
		}
	}
	if appService == nil {
		provider, _ := authService.(app.ClientProvider)
		application := app.New(stdout, provider)
		application.ErrOut = stderr
		appService = application
	}
	var commandLine CLI
	parser, err := kong.New(
		&commandLine,
		kong.Name("mdoc"),
		kong.Description("Publish Markdown documents to Google Docs."),
		kong.Vars{"version": Version},
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		return fmt.Errorf("build command parser: %w", err)
	}
	parsed, err := parser.Parse(args)
	if err != nil {
		return &UsageError{Err: err}
	}
	if err := parsed.Run(&Runtime{Context: context.Background(), App: appService, Auth: authService, Probe: probeService, Out: stdout, Explicit: explicitFlags(args)}); err != nil {
		if _, ok := errors.AsType[*kong.ParseError](err); ok {
			return &UsageError{Err: err}
		}
		return err
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func ExitCode(err error) int {
	if _, ok := errors.AsType[*UsageError](err); ok {
		return 2
	}
	return app.ExitCode(err)
}

type ErrorOutput struct {
	Class    string        `json:"class"`
	Code     string        `json:"code"`
	Message  string        `json:"message"`
	ExitCode int           `json:"exit_code"`
	Recovery *app.Recovery `json:"recovery,omitempty"`
}

func WriteError(writer io.Writer, err error, jsonOutput bool) {
	if !jsonOutput {
		fmt.Fprintf(writer, "mdoc: %v\n", err)
		return
	}
	result := ErrorOutput{Class: "command", Code: "command_failed", Message: err.Error(), ExitCode: ExitCode(err)}
	if typed, ok := errors.AsType[*app.Error](err); ok {
		result.Class = string(typed.Class)
		result.Code = typed.Code
		result.Recovery = typed.Recovery
	}
	if _, ok := errors.AsType[*UsageError](err); ok {
		result.Code = "usage_error"
	}
	_ = writeJSON(writer, result)
}

func WantsJSON(args []string) bool {
	projectPath := ""
	var outputValue *bool
	var jsonValue *bool
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		switch {
		case arg == "--config" && index+1 < len(args):
			index++
			projectPath = args[index]
		case strings.HasPrefix(arg, "--config="):
			projectPath = strings.TrimPrefix(arg, "--config=")
		case arg == "--output" && index+1 < len(args):
			index++
			value := args[index] == "json"
			outputValue = &value
		case strings.HasPrefix(arg, "--output="):
			value := strings.TrimPrefix(arg, "--output=") == "json"
			outputValue = &value
		case arg == "--json":
			value := true
			jsonValue = &value
		case strings.HasPrefix(arg, "--json="):
			value, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json="))
			if err == nil {
				jsonValue = &value
			}
		}
	}
	if jsonValue != nil {
		return *jsonValue
	}
	if outputValue != nil {
		return *outputValue
	}
	output, err := mdocconfig.ResolveOutput(mdocconfig.ResolverOptions{ProjectPath: projectPath})
	return err == nil && output.Format == "json"
}
