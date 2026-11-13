package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/goliatone/mdoc/contribution"
	"github.com/goliatone/mdoc/internal/contributionstate"
)

type ContributionCredentials interface {
	ContributionClient(context.Context) (*http.Client, string, error)
}

// Contributions owns the CLI's claims and receipts, keeping transport adapters thin.
type Contributions struct {
	Credentials ContributionCredentials
	API         contribution.API
	Binding     string
}

func (s *Contributions) provider(ctx context.Context) (contribution.API, string, error) {
	if s.API != nil {
		return s.API, s.Binding, nil
	}
	if s.Credentials == nil {
		return nil, "", contributionError(contribution.AccountUnavailable, "Google credentials unavailable; run mdoc auth login")
	}
	client, binding, e := s.Credentials.ContributionClient(ctx)
	if e != nil {
		return nil, "", contributionError(contribution.AccountUnavailable, "Google credentials unavailable; run mdoc auth login")
	}
	api, e := contribution.New(contribution.Options{HTTPClient: client, AccountBinding: binding})
	return api, binding, e
}
func contributionError(c contribution.Code, m string) error {
	return &contribution.Error{Code: c, Message: m}
}

type retainedPlan struct {
	kind   contribution.Kind
	meta   contribution.OperationMeta
	digest string
	create *contribution.CreatePlan
	update *contribution.UpdatePlan
	reply  *contribution.ReplyPlan
	raw    []byte
}

func parseContributionPlan(raw []byte) (retainedPlan, error) {
	var header struct {
		Kind contribution.Kind `json:"kind"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return retainedPlan{}, contributionError(contribution.InvalidPlan, "invalid plan")
	}
	var p retainedPlan
	p.kind = header.Kind
	var v any
	var e error
	switch header.Kind {
	case contribution.CreateKind:
		x, err := contribution.DecodeCreatePlan(raw)
		e = err
		p.create = &x
		p.meta = x.Input.Meta
		p.digest = x.Digest
		v = x
	case contribution.UpdateKind:
		x, err := contribution.DecodeUpdatePlan(raw)
		e = err
		p.update = &x
		p.meta = x.Input.Meta
		p.digest = x.Digest
		v = x
	case contribution.ReplyKind:
		x, err := contribution.DecodeReplyPlan(raw)
		e = err
		p.reply = &x
		p.meta = x.Input.Meta
		p.digest = x.Digest
		v = x
	default:
		e = contributionError(contribution.InvalidPlan, "unknown contribution kind")
	}
	if e != nil {
		return p, e
	}
	p.raw, _ = json.Marshal(v)
	return p, nil
}
func (p retainedPlan) input() any {
	if p.create != nil {
		return p.create.Input
	}
	if p.update != nil {
		return p.update.Input
	}
	return p.reply.Input
}
func (p retainedPlan) inspection(o contribution.Outcome) contribution.InspectInput {
	return contribution.InspectInput{Create: p.create, Update: p.update, Reply: p.reply, Previous: o}
}
func (s *Contributions) Prepare(ctx context.Context, kind contribution.Kind, input []byte, journal string) (json.RawMessage, error) {
	api, binding, e := s.provider(ctx)
	if e != nil {
		return nil, e
	}
	var v any
	var meta *contribution.OperationMeta
	var create contribution.CreateInput
	var update contribution.UpdateInput
	var reply contribution.ReplyInput
	switch kind {
	case contribution.CreateKind:
		v = &create
		meta = &create.Meta
	case contribution.UpdateKind:
		v = &update
		meta = &update.Meta
	case contribution.ReplyKind:
		v = &reply
		meta = &reply.Meta
	default:
		return nil, contributionError(contribution.InvalidInput, "unknown operation")
	}
	if e = contribution.DecodeJSON(input, v); e != nil {
		return nil, e
	}
	if meta.SchemaVersion == 0 {
		meta.SchemaVersion = 1
	}
	if meta.OperationID == "" {
		meta.OperationID, e = contribution.NewOperationID()
		if e != nil {
			return nil, e
		}
	}
	if meta.AccountBinding == "" {
		meta.AccountBinding = binding
	}
	if meta.AccountBinding != binding {
		return nil, contributionError(contribution.InvalidPlan, "input belongs to a different account connection")
	}
	store, e := contributionstate.Open(journal)
	if e != nil {
		return nil, e
	}
	defer store.Close()
	record, e := store.Load(meta.Namespace, meta.OperationID)
	if e != nil {
		return nil, e
	}
	if record != nil {
		p, e := parseContributionPlan(record.Plan)
		if e != nil {
			return nil, e
		}
		// Preparation resolves an omitted provider/tab. Reuse that retained
		// identity when comparing the same caller input on a later invocation.
		if p.update != nil {
			if update.Source.Provider == "" {
				update.Source.Provider = p.update.Input.Source.Provider
			}
			if update.Source.TabID == "" {
				update.Source.TabID = p.update.Input.Source.TabID
			}
		}
		if p.reply != nil {
			if reply.Source.Provider == "" {
				reply.Source.Provider = p.reply.Input.Source.Provider
			}
			if reply.Source.TabID == "" {
				reply.Source.TabID = p.reply.Input.Source.TabID
			}
		}
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(p.input())
		if p.kind != kind || !bytes.Equal(a, b) {
			return nil, contributionError(contribution.InvalidPlan, "operation is already bound to different input; use its retained plan")
		}
		return record.Plan, nil
	}
	var prepared any
	switch kind {
	case contribution.CreateKind:
		prepared, e = api.PrepareCreate(ctx, create)
	case contribution.UpdateKind:
		prepared, e = api.PrepareUpdate(ctx, update)
	case contribution.ReplyKind:
		prepared, e = api.PrepareReply(ctx, reply)
	}
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(prepared)
	if e != nil {
		return nil, e
	}
	if _, e = parseContributionPlan(raw); e != nil {
		return nil, e
	}
	record = &contributionstate.Record{Version: 1, Plan: raw, State: "prepared"}
	if e = store.Save(meta.Namespace, meta.OperationID, record); e != nil {
		return nil, e
	}
	return raw, nil
}
func (s *Contributions) Execute(ctx context.Context, kind contribution.Kind, raw []byte, expected, journal string, write bool) (contribution.Outcome, error) {
	p, e := parseContributionPlan(raw)
	if e != nil {
		return contribution.Outcome{Status: contribution.NotSent, Code: contribution.InvalidPlan}, e
	}
	o := contribution.Outcome{Kind: p.kind, Meta: p.meta, RequestDigest: p.digest, Status: contribution.NotSent}
	if p.update != nil {
		o.DocumentID = p.update.Input.Source.DocumentID
		o.TabID = p.update.Input.Source.TabID
	}
	if p.reply != nil {
		o.DocumentID = p.reply.Input.Source.DocumentID
		o.TabID = p.reply.Input.Source.TabID
		o.CommentID = p.reply.Input.CommentID
	}
	reject := func(code contribution.Code, message string) (contribution.Outcome, error) {
		o.Code = code
		if code == contribution.InvalidPlan {
			o.Status = contribution.Conflict
		}
		return o, contributionError(code, message)
	}
	if kind != p.kind || !write || expected != p.digest {
		return reject(contribution.InvalidPlan, "exact plan kind, --expect-digest and explicit --write are required")
	}
	store, e := contributionstate.Open(journal)
	if e != nil {
		return o, e
	}
	defer store.Close()
	record, e := store.Load(p.meta.Namespace, p.meta.OperationID)
	if e != nil {
		return o, e
	}
	if record == nil {
		return reject(contribution.InvalidPlan, "plan must be prepared in this journal before execution")
	}
	retained, e := parseContributionPlan(record.Plan)
	if e != nil {
		return o, e
	}
	if !bytes.Equal(p.raw, retained.raw) {
		return reject(contribution.InvalidPlan, "plan differs from the journal's retained authorization")
	}
	if e = validateContributionRecord(p, record); e != nil {
		return o, e
	}
	if record.State != "prepared" {
		o = record.Outcome
		if record.State == "sending" {
			o.Status = contribution.Unknown
			o.Code = contribution.ReconciliationRequired
		}
		if o.Status == contribution.Committed {
			return o, nil
		}
		return o, contributionError(contribution.ReconciliationRequired, "operation has already been claimed; inspect it without resending")
	}
	id, e := contribution.NewOperationID()
	if e != nil {
		return o, e
	}
	o.AttemptID = id
	o.Status = contribution.Unknown
	o.Code = contribution.ReconciliationRequired
	record.State = "sending"
	record.Outcome = o
	if e = store.Save(p.meta.Namespace, p.meta.OperationID, record); e != nil {
		return o, e
	}
	api, binding, e := s.provider(ctx)
	if e == nil && binding != p.meta.AccountBinding {
		e = contributionError(contribution.InvalidPlan, "account connection changed; prepare a new operation")
	}
	if e != nil {
		o.Status = contribution.NotSent
		o.Code = contribution.AccountUnavailable
		if typed, ok := e.(*contribution.Error); ok {
			o.Code = typed.Code
			if typed.Code == contribution.InvalidPlan {
				o.Status = contribution.Conflict
			}
		}
	} else {
		attempt := contribution.Attempt{ID: id, FirstTransmission: true}
		switch kind {
		case contribution.CreateKind:
			o, e = api.Create(ctx, contribution.CreateRequest{Plan: *p.create, ExpectedDigest: expected, Attempt: attempt})
		case contribution.UpdateKind:
			o, e = api.ApplyUpdate(ctx, contribution.UpdateRequest{Plan: *p.update, ExpectedDigest: expected, Attempt: attempt})
		case contribution.ReplyKind:
			o, e = api.Reply(ctx, contribution.ReplyRequest{Plan: *p.reply, ExpectedDigest: expected, Attempt: attempt})
		}
	}
	record.State = "finished"
	record.Outcome = o
	if saveErr := store.Save(p.meta.Namespace, p.meta.OperationID, record); saveErr != nil {
		return o, saveErr
	}
	return o, e
}
func (s *Contributions) Inspect(ctx context.Context, raw []byte, journal string) (contribution.Inspection, error) {
	p, e := parseContributionPlan(raw)
	if e != nil {
		return contribution.Inspection{}, e
	}
	store, e := contributionstate.Open(journal)
	if e != nil {
		return contribution.Inspection{}, e
	}
	defer store.Close()
	record, e := store.Load(p.meta.Namespace, p.meta.OperationID)
	if e != nil {
		return contribution.Inspection{}, e
	}
	if record == nil {
		return contribution.Inspection{}, contributionError(contribution.InvalidPlan, "retained operation record is missing")
	}
	retained, e := parseContributionPlan(record.Plan)
	if e != nil || !bytes.Equal(p.raw, retained.raw) {
		return contribution.Inspection{}, contributionError(contribution.InvalidPlan, "inspection plan differs from journal")
	}
	if e = validateContributionRecord(p, record); e != nil {
		return contribution.Inspection{}, e
	}
	if record.State == "prepared" {
		return contribution.Inspection{}, contributionError(contribution.InvalidInput, "operation has not been claimed")
	}
	o := record.Outcome
	if record.State == "sending" {
		o.Status = contribution.Unknown
		o.Code = contribution.ReconciliationRequired
	}
	api, binding, e := s.provider(ctx)
	if e != nil {
		return contribution.Inspection{Outcome: o, Evidence: contribution.Inaccessible}, e
	}
	if binding != p.meta.AccountBinding {
		return contribution.Inspection{Outcome: o, Evidence: contribution.Inaccessible}, contributionError(contribution.InvalidPlan, "account connection changed")
	}
	result, e := api.InspectOutcome(ctx, p.inspection(o))
	record.Inspection = &result
	record.Outcome = result.Outcome
	record.State = "finished"
	if saveErr := store.Save(p.meta.Namespace, p.meta.OperationID, record); saveErr != nil {
		return result, saveErr
	}
	return result, e
}

func validateContributionRecord(p retainedPlan, r *contributionstate.Record) error {
	bad := func() error {
		return contributionError(contribution.InvalidPlan, "journal claim/receipt is inconsistent; preserve it for manual recovery")
	}
	o := r.Outcome
	if r.State == "prepared" {
		if o.Status != "" || o.AttemptID != "" {
			return bad()
		}
		return nil
	}
	if o.Kind != p.kind || o.Meta != p.meta || o.RequestDigest != p.digest || o.AttemptID == "" {
		return bad()
	}
	if p.update != nil && (o.DocumentID != p.update.Input.Source.DocumentID || o.TabID != p.update.Input.Source.TabID) {
		return bad()
	}
	if p.reply != nil && (o.DocumentID != p.reply.Input.Source.DocumentID || o.TabID != p.reply.Input.Source.TabID || o.CommentID != p.reply.Input.CommentID) {
		return bad()
	}
	if r.State == "sending" && o.Status != contribution.Unknown {
		return bad()
	}
	switch o.Status {
	case contribution.Committed:
		if o.DocumentID == "" || p.reply != nil && o.ReplyID == "" {
			return bad()
		}
	case contribution.NotSent, contribution.Conflict, contribution.Unknown:
	default:
		return bad()
	}
	return nil
}
