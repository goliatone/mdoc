package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/goliatone/mdoc/contribution"
	"github.com/goliatone/mdoc/internal/app"
)

type ContributionCmd struct {
	PrepareCreate ContributionPrepareCreateCmd `cmd:"" help:"Prepare supported Markdown creation; retain plan in a private journal."`
	Create        ContributionCreateCmd        `cmd:"" help:"Create once from the exact retained plan."`
	PrepareUpdate ContributionPrepareUpdateCmd `cmd:"" help:"Prepare one revision-guarded interior-run text change."`
	ApplyUpdate   ContributionUpdateCmd        `cmd:"" help:"Apply one retained text change."`
	PrepareReply  ContributionPrepareReplyCmd  `cmd:"" help:"Prepare a reply to one unresolved existing thread."`
	Reply         ContributionReplyCmd         `cmd:"" help:"Send one retained reply."`
	Inspect       ContributionInspectCmd       `cmd:"" help:"Inspect a retained claimed operation without resending."`
}
type ContributionPreparation struct {
	Input   string `required:"" type:"existingfile" help:"Typed input JSON file."`
	Journal string `required:"" help:"Explicit private journal directory (0700)."`
}
type ContributionExecution struct {
	Plan         string `required:"" type:"existingfile" help:"Retained plan JSON file."`
	ExpectDigest string `required:"" help:"Exact authorized plan digest."`
	Journal      string `required:"" help:"Same private journal used during preparation."`
	Write        bool   `help:"Explicitly authorize the one prepared Google mutation."`
}
type ContributionPrepareCreateCmd struct{ ContributionPreparation }
type ContributionPrepareUpdateCmd struct{ ContributionPreparation }
type ContributionPrepareReplyCmd struct{ ContributionPreparation }
type ContributionCreateCmd struct{ ContributionExecution }
type ContributionUpdateCmd struct{ ContributionExecution }
type ContributionReplyCmd struct{ ContributionExecution }
type ContributionInspectCmd struct {
	Plan    string `required:"" type:"existingfile" help:"Retained plan JSON file."`
	Journal string `required:"" help:"Journal containing the operation claim and receipt."`
}

func contributions(r *Runtime) *app.Contributions {
	if r.Contributions != nil {
		return r.Contributions
	}
	credentials, _ := r.Auth.(app.ContributionCredentials)
	return &app.Contributions{Credentials: credentials}
}
func contributionFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, &contribution.Error{Code: contribution.InvalidInput, Message: "cannot read contribution JSON"}
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, contribution.MaxPlan+1))
	if e != nil || len(b) > contribution.MaxPlan {
		return nil, &contribution.Error{Code: contribution.InvalidInput, Message: "contribution JSON exceeds limit or is unreadable"}
	}
	return b, nil
}
func (c ContributionPreparation) run(r *Runtime, k contribution.Kind) error {
	b, e := contributionFile(c.Input)
	if e != nil {
		return e
	}
	p, e := contributions(r).Prepare(r.Context, k, b, c.Journal)
	if e != nil {
		return e
	}
	return writeJSON(r.Out, p)
}
func (c ContributionExecution) run(r *Runtime, k contribution.Kind) error {
	b, e := contributionFile(c.Plan)
	if e != nil {
		return e
	}
	o, e := contributions(r).Execute(r.Context, k, b, c.ExpectDigest, c.Journal, c.Write)
	return writeContributionResult(r.Out, o, o, e)
}
func (c ContributionPrepareCreateCmd) Run(r *Runtime) error { return c.run(r, contribution.CreateKind) }
func (c ContributionPrepareUpdateCmd) Run(r *Runtime) error { return c.run(r, contribution.UpdateKind) }
func (c ContributionPrepareReplyCmd) Run(r *Runtime) error  { return c.run(r, contribution.ReplyKind) }
func (c ContributionCreateCmd) Run(r *Runtime) error        { return c.run(r, contribution.CreateKind) }
func (c ContributionUpdateCmd) Run(r *Runtime) error        { return c.run(r, contribution.UpdateKind) }
func (c ContributionReplyCmd) Run(r *Runtime) error         { return c.run(r, contribution.ReplyKind) }
func (c ContributionInspectCmd) Run(r *Runtime) error {
	b, e := contributionFile(c.Plan)
	if e != nil {
		return e
	}
	result, e := contributions(r).Inspect(r.Context, b, c.Journal)
	return writeContributionResult(r.Out, result, result.Outcome, e)
}

// Output delivery cannot change the provider outcome or erase a recovery error.
// Inspection may return an unknown historical outcome without an error, and an
// acknowledged write may have succeeded even when its result cannot be delivered.
func writeContributionResult(out io.Writer, result any, outcome contribution.Outcome, operationErr error) error {
	if outErr := writeJSON(out, result); outErr != nil {
		var recoveryErr error
		if outcome.Status == contribution.Unknown || outcome.Status == contribution.Committed {
			recoveryErr = &contribution.Error{
				Status:  outcome.Status,
				Code:    contribution.ReconciliationRequired,
				Message: "contribution result could not be delivered; inspect the retained plan with the same journal without resending",
			}
		}
		return errors.Join(recoveryErr, operationErr, fmt.Errorf("write contribution result: %w", outErr))
	}
	return operationErr
}
