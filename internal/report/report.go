package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/fields"
	"github.com/goliatone/mdoc/internal/planner"
	"github.com/goliatone/mdoc/internal/state"
)

type PlanReport struct {
	SchemaVersion int            `json:"schema_version"`
	Kind          string         `json:"kind"`
	Result        string         `json:"result"`
	Items         []planner.Item `json:"items"`
	Counts        map[string]int `json:"counts"`
	Blocked       bool           `json:"blocked"`
	EntryURL      string         `json:"entry_url,omitempty"`
}

type RemoteStatusReport struct {
	SchemaVersion  int                       `json:"schema_version"`
	Kind           string                    `json:"kind"`
	Result         string                    `json:"result"`
	Project        string                    `json:"project"`
	Profile        string                    `json:"profile"`
	Files          []string                  `json:"files"`
	Documents      int                       `json:"documents"`
	Entry          string                    `json:"entry,omitempty"`
	Checks         map[string]StatusCheck    `json:"checks"`
	Changes        []LocalChange             `json:"changes,omitempty"`
	Issues         []document.Issue          `json:"issues,omitempty"`
	FieldSummaries map[string]fields.Summary `json:"field_summaries,omitempty"`
	Targets        []TargetSummary           `json:"targets,omitempty"`
	ReviewOrphans  []ReviewSnapshotOrphan    `json:"review_orphans,omitempty"`
}

type DoctorReport struct {
	SchemaVersion int                    `json:"schema_version"`
	Kind          string                 `json:"kind"`
	Result        string                 `json:"result"`
	Project       string                 `json:"project,omitempty"`
	Profile       string                 `json:"profile"`
	Checks        map[string]StatusCheck `json:"checks"`
}

type PublishReport struct {
	SchemaVersion int                     `json:"schema_version,omitempty"`
	Kind          string                  `json:"kind"`
	Plan          PlanReport              `json:"plan"`
	Targets       map[string]state.Target `json:"targets,omitempty"`
	Warnings      []string                `json:"warnings,omitempty"`
	EntryURL      string                  `json:"entry_url,omitempty"`
}

type ReviewRevisionSummary struct {
	DriveVersion string `json:"drive_version"`
	DocsRevision string `json:"docs_revision"`
}

type ReviewFileSummary struct {
	Source  string `json:"source"`
	Changed bool   `json:"changed"`
}

type ReviewChangeSummary struct {
	Index     int    `json:"index"`
	Class     string `json:"class"`
	Operation string `json:"operation"`
	Source    string `json:"source,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type ReviewIssueSummary struct {
	Source string `json:"source,omitempty"`
	Reason string `json:"reason"`
}

type ReviewPullReport struct {
	SchemaVersion  int                   `json:"schema_version"`
	Kind           string                `json:"kind"`
	Result         string                `json:"result"`
	Publication    string                `json:"publication"`
	Target         string                `json:"target"`
	Account        string                `json:"account,omitempty"`
	BaselineFileID string                `json:"baseline_file_id,omitempty"`
	FileID         string                `json:"file_id"`
	ReviewSource   string                `json:"review_source"`
	RemoteVerified bool                  `json:"remote_checks_verified"`
	PartialPatch   bool                  `json:"partial_patch"`
	Generation     int                   `json:"generation"`
	Capture        ReviewRevisionSummary `json:"capture"`
	Activation     ReviewRevisionSummary `json:"activation"`
	Review         ReviewRevisionSummary `json:"review"`
	OpenComments   int                   `json:"open_comments"`
	Counts         map[string]int        `json:"counts"`
	Files          []ReviewFileSummary   `json:"files"`
	Changes        []ReviewChangeSummary `json:"changes"`
	Conflicts      []ReviewIssueSummary  `json:"conflicts,omitempty"`
	Unsupported    []ReviewIssueSummary  `json:"unsupported,omitempty"`
	Patch          string                `json:"patch,omitempty"`
	PatchOutput    string                `json:"patch_output,omitempty"`
	NextActions    []string              `json:"next_actions,omitempty"`
}

func WriteReviewPull(writer io.Writer, value ReviewPullReport, jsonOutput, quiet bool) error {
	if quiet {
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	operation := "Review pull"
	if value.Kind == "review_bootstrap" {
		operation = "Review bootstrap"
	}
	fmt.Fprintf(writer, "%s: %s\nPublication: %s\nTarget: %s generation %d\n", operation, value.Result, value.Publication, value.Target, value.Generation)
	if value.Account != "" {
		fmt.Fprintf(writer, "Account: %s\n", value.Account)
	}
	if value.BaselineFileID != "" && value.BaselineFileID != value.FileID {
		fmt.Fprintf(writer, "Baseline document: %s\n", value.BaselineFileID)
	}
	fmt.Fprintf(writer, "Document: %s\n", value.FileID)
	fmt.Fprintf(writer, "Review source: %s\n", value.ReviewSource)
	if value.RemoteVerified {
		fmt.Fprintf(writer, "Revisions: capture=%s activation=%s review=%s\nOpen comments: %d\n", value.Capture.DocsRevision, value.Activation.DocsRevision, value.Review.DocsRevision, value.OpenComments)
	} else {
		fmt.Fprintf(writer, "Revisions: capture=%s activation=%s review=not checked\nOpen comments: not checked\n", value.Capture.DocsRevision, value.Activation.DocsRevision)
	}
	countKeys := make([]string, 0, len(value.Counts))
	for key := range value.Counts {
		countKeys = append(countKeys, key)
	}
	sort.Strings(countKeys)
	for _, key := range countKeys {
		fmt.Fprintf(writer, "%s changes: %d\n", key, value.Counts[key])
	}
	for _, file := range value.Files {
		state := "unchanged"
		if file.Changed {
			state = "changed"
		}
		fmt.Fprintf(writer, "%s %s\n", state, file.Source)
	}
	for _, conflict := range value.Conflicts {
		fmt.Fprintf(writer, "blocked: %s\n", reviewIssueText(conflict))
	}
	for _, unsupported := range value.Unsupported {
		fmt.Fprintf(writer, "unsupported: %s\n", reviewIssueText(unsupported))
	}
	if value.Patch != "" {
		fmt.Fprintln(writer, "Patch:")
		fmt.Fprint(writer, value.Patch)
		if !strings.HasSuffix(value.Patch, "\n") {
			fmt.Fprintln(writer)
		}
	}
	if value.PatchOutput != "" {
		fmt.Fprintf(writer, "Patch written: %s\n", value.PatchOutput)
	}
	for _, action := range value.NextActions {
		fmt.Fprintf(writer, "Next: %s\n", action)
	}
	return nil
}

func reviewIssueText(issue ReviewIssueSummary) string {
	if issue.Source != "" {
		return issue.Source + ": " + issue.Reason
	}
	return issue.Reason
}

type ValidationReport struct {
	SchemaVersion  int                       `json:"schema_version,omitempty"`
	Kind           string                    `json:"kind"`
	Documents      int                       `json:"documents"`
	Entry          string                    `json:"entry"`
	Issues         []document.Issue          `json:"issues"`
	Valid          bool                      `json:"valid"`
	FieldSummaries map[string]fields.Summary `json:"field_summaries,omitempty"`
	Targets        []TargetSummary           `json:"targets,omitempty"`
}

type TargetSummary struct {
	Target          string              `json:"target"`
	PublicationID   string              `json:"publication_id"`
	PublicationKind string              `json:"publication_kind"`
	Source          string              `json:"source,omitempty"`
	MemberCount     int                 `json:"member_count,omitempty"`
	Review          *ReviewTargetStatus `json:"review,omitempty"`
}

type ReviewTargetStatus struct {
	PullEnabled       bool   `json:"pull_enabled"`
	BaselineAvailable bool   `json:"baseline_available"`
	State             string `json:"state"`
	Message           string `json:"message,omitempty"`
}

type ReviewSnapshotOrphan struct {
	Path       string `json:"path"`
	Target     string `json:"target,omitempty"`
	Generation int    `json:"generation,omitempty"`
	Reason     string `json:"reason"`
}

type CheckState string

const (
	CheckPassed        CheckState = "passed"
	CheckFailed        CheckState = "failed"
	CheckUnavailable   CheckState = "unavailable"
	CheckNotApplicable CheckState = "not_applicable"
)

type StatusCheck struct {
	State   CheckState `json:"state"`
	Message string     `json:"message,omitempty"`
}

type LocalChange struct {
	Source          string `json:"source,omitempty"`
	Target          string `json:"target,omitempty"`
	PublicationID   string `json:"publication_id,omitempty"`
	PublicationKind string `json:"publication_kind,omitempty"`
	State           string `json:"state"`
}

func (change LocalChange) Identity() string {
	if change.Target != "" {
		return change.Target
	}
	return change.Source
}

type LocalStatusReport struct {
	SchemaVersion  int                       `json:"schema_version"`
	Kind           string                    `json:"kind"`
	Result         string                    `json:"result"`
	Project        string                    `json:"project,omitempty"`
	Profile        string                    `json:"profile"`
	Files          []string                  `json:"files"`
	Entry          string                    `json:"entry,omitempty"`
	Checks         map[string]StatusCheck    `json:"checks"`
	Changes        []LocalChange             `json:"changes,omitempty"`
	Issues         []document.Issue          `json:"issues,omitempty"`
	FieldSummaries map[string]fields.Summary `json:"field_summaries,omitempty"`
	Targets        []TargetSummary           `json:"targets,omitempty"`
	ReviewOrphans  []ReviewSnapshotOrphan    `json:"review_orphans,omitempty"`
}

func WriteLocalStatus(writer io.Writer, value LocalStatusReport, jsonOutput, quiet bool) error {
	if quiet {
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	fmt.Fprintf(writer, "Local status: %s\n", value.Result)
	if value.Project != "" {
		fmt.Fprintf(writer, "Project: %s\n", value.Project)
	}
	fmt.Fprintf(writer, "Profile: %s\nFiles: %d\n", value.Profile, len(value.Files))
	if value.Entry != "" {
		fmt.Fprintf(writer, "Entry: %s\n", value.Entry)
	}
	checkNames := make([]string, 0, len(value.Checks))
	for name := range value.Checks {
		checkNames = append(checkNames, name)
	}
	sort.Strings(checkNames)
	for _, name := range checkNames {
		check := value.Checks[name]
		fmt.Fprintf(writer, "%s: %s", name, check.State)
		if check.Message != "" {
			fmt.Fprintf(writer, " - %s", check.Message)
		}
		fmt.Fprintln(writer)
	}
	for _, issue := range value.Issues {
		fmt.Fprintf(writer, "%s %s: %s: %s\n", issue.Severity, issue.Code, issue.Source, issue.Message)
	}
	for _, change := range value.Changes {
		fmt.Fprintf(writer, "%s %s\n", change.State, change.Identity())
	}
	writeTargetSummaries(writer, value.Targets)
	writeFieldSummaries(writer, value.FieldSummaries)
	return nil
}

func WriteValidation(writer io.Writer, value ValidationReport, jsonOutput, quiet bool) error {
	if quiet {
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	fmt.Fprintf(writer, "Documents: %d\nEntry: %s\n", value.Documents, value.Entry)
	for _, issue := range value.Issues {
		fmt.Fprintf(writer, "%s %s: %s: %s\n", issue.Severity, issue.Code, issue.Source, issue.Message)
	}
	if value.Valid {
		fmt.Fprintln(writer, "Validation passed.")
	}
	writeTargetSummaries(writer, value.Targets)
	writeFieldSummaries(writer, value.FieldSummaries)
	return nil
}

func FromPlan(plan planner.Plan, entryURL string) PlanReport {
	return FromPlanVersion(plan, entryURL, 1)
}

func FromPlanVersion(plan planner.Plan, entryURL string, schemaVersion int) PlanReport {
	counts := map[string]int{}
	for _, item := range plan.Items {
		counts[string(item.Action)]++
		for _, action := range item.Dependent {
			counts[string(action)]++
		}
	}
	result := "ok"
	if plan.Blocked {
		result = "blocked"
	}
	return PlanReport{SchemaVersion: schemaVersion, Kind: "plan", Result: result, Items: plan.Items, Counts: counts, Blocked: plan.Blocked, EntryURL: entryURL}
}

func WriteRemoteStatus(writer io.Writer, value RemoteStatusReport, jsonOutput, quiet bool) error {
	if quiet {
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	fmt.Fprintf(writer, "Remote status: %s\nProject: %s\nProfile: %s\nDocuments: %d\n", value.Result, value.Project, value.Profile, value.Documents)
	for _, change := range value.Changes {
		fmt.Fprintf(writer, "%s %s\n", change.State, change.Identity())
	}
	for _, issue := range value.Issues {
		fmt.Fprintf(writer, "%s %s: %s: %s\n", issue.Severity, issue.Code, issue.Source, issue.Message)
	}
	writeTargetSummaries(writer, value.Targets)
	writeReviewOrphans(writer, value.ReviewOrphans)
	writeFieldSummaries(writer, value.FieldSummaries)
	return nil
}

func writeFieldSummaries(writer io.Writer, summaries map[string]fields.Summary) {
	ids := make([]string, 0, len(summaries))
	for id := range summaries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		summary := summaries[id]
		fmt.Fprintf(writer, "Fields %s: roots=%d files=%d assignments=%d hash=%s; values are redacted\n", id, summary.RootCount, summary.FileCount, summary.AssignmentCount, summary.Hash)
	}
}

func writeTargetSummaries(writer io.Writer, targets []TargetSummary) {
	for _, target := range targets {
		detail := target.PublicationKind
		if target.Source != "" {
			detail += ", source=" + target.Source
		}
		if target.MemberCount > 0 {
			detail += fmt.Sprintf(", members=%d", target.MemberCount)
		}
		fmt.Fprintf(writer, "Target %s (%s)\n", target.Target, detail)
		if target.Review != nil {
			fmt.Fprintf(writer, "Review %s: %s", target.Target, target.Review.State)
			if target.Review.Message != "" {
				fmt.Fprintf(writer, " - %s", target.Review.Message)
			}
			fmt.Fprintln(writer)
		}
	}
}

func writeReviewOrphans(writer io.Writer, orphans []ReviewSnapshotOrphan) {
	for _, orphan := range orphans {
		fmt.Fprintf(writer, "Review snapshot orphan: %s - %s; retained, remove manually only after confirmation\n", orphan.Path, orphan.Reason)
	}
}

func WriteDoctor(writer io.Writer, value DoctorReport, jsonOutput, quiet bool) error {
	if quiet {
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	fmt.Fprintf(writer, "Doctor: %s\n", value.Result)
	names := make([]string, 0, len(value.Checks))
	for name := range value.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		check := value.Checks[name]
		fmt.Fprintf(writer, "%s: %s", name, check.State)
		if check.Message != "" {
			fmt.Fprintf(writer, " - %s", check.Message)
		}
		fmt.Fprintln(writer)
	}
	return nil
}

func WritePlan(writer io.Writer, value PlanReport, jsonOutput, quiet bool) error {
	if quiet {
		if value.EntryURL != "" {
			fmt.Fprintln(writer, value.EntryURL)
		}
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	for _, item := range value.Items {
		fmt.Fprintf(writer, "%s %s", item.Action, item.Identity())
		if item.Reason != "" {
			fmt.Fprintf(writer, ": %s", item.Reason)
		}
		fmt.Fprintln(writer)
		for _, warning := range item.Warnings {
			fmt.Fprintf(writer, "warning %s: %s\n", item.Identity(), warning)
		}
	}
	writeCounts(writer, value.Counts)
	if value.EntryURL != "" {
		fmt.Fprintf(writer, "Entry: %s\n", value.EntryURL)
	}
	return nil
}

func WritePublish(writer io.Writer, value PublishReport, jsonOutput, quiet bool) error {
	if quiet {
		if value.EntryURL != "" {
			fmt.Fprintln(writer, value.EntryURL)
		}
		return nil
	}
	if jsonOutput {
		return writeJSON(writer, value)
	}
	humanPlan := value.Plan
	humanPlan.EntryURL = ""
	if err := WritePlan(writer, humanPlan, false, false); err != nil {
		return err
	}
	keys := make([]string, 0, len(value.Targets))
	for key := range value.Targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(writer, "published %s: %s\n", key, value.Targets[key].URL)
	}
	for _, warning := range value.Warnings {
		fmt.Fprintf(writer, "warning: %s\n", warning)
	}
	if value.EntryURL != "" {
		fmt.Fprintf(writer, "Entry: %s\n", value.EntryURL)
	}
	return nil
}

func writeCounts(writer io.Writer, counts map[string]int) {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return
	}
	fmt.Fprint(writer, "Counts:")
	for _, key := range keys {
		fmt.Fprintf(writer, " %s=%d", key, counts[key])
	}
	fmt.Fprintln(writer)
}
func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
