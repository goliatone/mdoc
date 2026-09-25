package cli

import (
	"encoding/json"
	"os"

	"github.com/goliatone/mdoc/internal/app"
	"github.com/goliatone/mdoc/workingdraft"
)

type WorkingDraftCmd struct {
	Inspect    DraftInspectCmd    `cmd:"" help:"Inspect an app-authorized Google Doc; output JSON."`
	Capture    DraftCaptureCmd    `cmd:"" help:"Capture an immutable snapshot to stdout as JSON."`
	Compare    DraftCompareCmd    `cmd:"" help:"Compare saved snapshots and current title/body JSON offline."`
	Discussion DraftDiscussionCmd `cmd:"" help:"Read discussion independently of body conversion; output JSON."`
}
type DraftSource struct {
	Source string `arg:"" help:"App-authorized document ID or Google Docs URL."`
}
type DraftInspectCmd struct{ DraftSource }
type DraftCaptureCmd struct{ DraftSource }
type DraftDiscussionCmd struct {
	DraftSource
	Cursor string `help:"Continuation cursor from a prior page."`
	All    bool   `help:"Read up to 100 pages, preserving partial results on failure."`
}
type DraftCompareCmd struct {
	Baseline string `arg:"" help:"Baseline snapshot JSON file."`
	Incoming string `arg:"" help:"Incoming snapshot JSON file."`
	Current  string `arg:"" help:"Current content JSON file with title and body fields."`
}

func draftService(runtime *Runtime) (workingdraft.API, error) {
	if runtime.WorkingDraft != nil {
		return runtime.WorkingDraft, nil
	}
	credentials, _ := runtime.Auth.(app.ClientProvider)
	return app.WorkingDraftService(runtime.Context, credentials)
}
func (c DraftInspectCmd) Run(r *Runtime) error {
	source, err := workingdraft.ParseSource(c.Source)
	if err != nil {
		return err
	}
	s, err := draftService(r)
	if err != nil {
		return err
	}
	result, err := s.Inspect(r.Context, source)
	if err != nil {
		return err
	}
	return writeJSON(r.Out, result)
}
func (c DraftCaptureCmd) Run(r *Runtime) error {
	source, err := workingdraft.ParseSource(c.Source)
	if err != nil {
		return err
	}
	s, err := draftService(r)
	if err != nil {
		return err
	}
	result, err := s.Capture(r.Context, source)
	if err != nil {
		return err
	}
	return writeJSON(r.Out, result)
}
func (c DraftDiscussionCmd) Run(r *Runtime) error {
	if c.All && c.Cursor != "" {
		return &workingdraft.Error{Code: workingdraft.InvalidSource, Message: "--all and --cursor cannot be combined"}
	}
	source, err := workingdraft.ParseSource(c.Source)
	if err != nil {
		return err
	}
	s, err := draftService(r)
	if err != nil {
		return err
	}
	var result workingdraft.DiscussionPage
	if c.All {
		result, err = s.ReadAllDiscussion(r.Context, source)
	} else {
		result, err = s.ReadDiscussion(r.Context, workingdraft.DiscussionInput{Source: source, Cursor: c.Cursor})
	}
	if err == nil || len(result.Threads) > 0 {
		if outputErr := writeJSON(r.Out, result); outputErr != nil {
			return outputErr
		}
	}
	return err
}
func (c DraftCompareCmd) Run(r *Runtime) error {
	baseline, err := readDraftSnapshot(c.Baseline)
	if err != nil {
		return err
	}
	incoming, err := readDraftSnapshot(c.Incoming)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(c.Current)
	if err != nil {
		return &workingdraft.Error{Code: workingdraft.SnapshotCorrupt, Message: "cannot read current content JSON"}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || fields["title"] == nil || fields["body"] == nil {
		return &workingdraft.Error{Code: workingdraft.SnapshotCorrupt, Message: "current content must contain exactly title and body strings"}
	}
	var current workingdraft.Content
	if json.Unmarshal(data, &current) != nil || string(fields["title"]) == "null" || string(fields["body"]) == "null" {
		return &workingdraft.Error{Code: workingdraft.SnapshotCorrupt, Message: "current title and body must be strings"}
	}
	result, err := workingdraft.Compare(r.Context, workingdraft.CompareInput{Baseline: baseline, Incoming: incoming, Current: current})
	if err != nil {
		return err
	}
	return writeJSON(r.Out, result)
}
func readDraftSnapshot(path string) (workingdraft.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workingdraft.Snapshot{}, &workingdraft.Error{Code: workingdraft.SnapshotCorrupt, Message: "cannot read snapshot JSON"}
	}
	return workingdraft.DecodeSnapshot(data)
}
