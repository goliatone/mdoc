package planner

import (
	"fmt"
	"slices"
	"sort"

	"github.com/goliatone/mdoc/internal/state"
)

type Action string

const (
	ActionCreate     Action = "create"
	ActionNewReview  Action = "new_review"
	ActionLinkUpdate Action = "link_update"
	ActionActivation Action = "activation"
	ActionSkip       Action = "skip"
	ActionBlocked    Action = "blocked"
	ActionFailed     Action = "failed"
	ActionOrphan     Action = "orphan"
)

type LocalDocument struct {
	TargetKey       string
	PublicationID   string
	PublicationKind string
	SourceKey       string
	MemberCount     int
	Title           string
	SourceHash      string
	RenderHash      string
	Outbound        []string
	Failed          string
}

type LocalTarget = LocalDocument

type RemoteDocument struct {
	Exists              bool
	Accessible          bool
	Trashed             bool
	Ambiguous           bool
	MetadataMismatch    bool
	Version             string
	Name                string
	ParentID            string
	UnresolvedComments  int
	SuggestionsDetected bool
}

type Input struct {
	Local                []LocalDocument
	State                *state.State
	Remote               map[string]RemoteDocument
	Selected             map[string]bool
	NewReview            bool
	ExpectedReviewFolder string
}

type Item struct {
	SourceKey       string   `json:"source,omitempty"`
	Target          string   `json:"target,omitempty"`
	PublicationID   string   `json:"publication_id,omitempty"`
	PublicationKind string   `json:"publication_kind,omitempty"`
	MemberCount     int      `json:"member_count,omitempty"`
	Action          Action   `json:"action"`
	Generation      int      `json:"generation,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Dependent       []Action `json:"dependent,omitempty"`
}

func (item Item) Identity() string {
	if item.Target != "" {
		return item.Target
	}
	return item.SourceKey
}

type Plan struct {
	Items   []Item `json:"items"`
	Blocked bool   `json:"blocked"`
}

func (p Plan) WriteCount() int {
	count := 0
	for _, item := range p.Items {
		if item.Action == ActionCreate || item.Action == ActionNewReview {
			count++
		}
		count += len(item.Dependent)
	}
	return count
}

func Build(input Input) Plan {
	locals := map[string]LocalDocument{}
	keys := make([]string, 0, len(input.Local))
	for _, local := range input.Local {
		key := localKey(local)
		locals[key] = local
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selected := input.Selected
	if len(selected) == 0 {
		selected = map[string]bool{}
		for _, key := range keys {
			selected[key] = true
		}
	}
	changeRoots := map[string]bool{}
	items := map[string]Item{}
	for _, key := range keys {
		local := locals[key]
		if !selected[key] {
			continue
		}
		item := classify(key, local, input)
		items[key] = item
		if item.Action == ActionCreate || item.Action == ActionNewReview {
			changeRoots[key] = true
		}
	}
	closure := inboundClosure(locals, changeRoots)
	for key := range closure {
		local, ok := locals[key]
		if !ok {
			continue
		}
		item, exists := items[key]
		if !exists {
			item = classify(key, local, input)
		}
		if item.Action == ActionSkip {
			documentState := stateForLocal(input.State, local, key)
			if documentState.ActiveTarget == nil {
				item.Action = ActionCreate
				item.Generation = 1
			} else {
				item.Action = ActionNewReview
				item.Generation = documentState.ActiveTarget.Generation + 1
			}
			item.Reason = "an outbound target URL changes in this linked review set"
		}
		items[key] = item
	}
	if len(input.Selected) == 0 {
		storedTargets := input.State.Documents
		if hasTargetLocals(input.Local) {
			storedTargets = input.State.Targets
		}
		for key, documentState := range storedTargets {
			if _, ok := locals[key]; ok {
				continue
			}
			generation := 0
			if documentState.ActiveTarget != nil {
				generation = documentState.ActiveTarget.Generation
			}
			items[key] = Item{SourceKey: key, Action: ActionOrphan, Generation: generation, Reason: "target is missing from the configured publication set"}
		}
	}
	plan := Plan{}
	writeSources := map[string]bool{}
	for key, item := range items {
		if item.Action == ActionCreate || item.Action == ActionNewReview {
			writeSources[key] = true
		}
	}
	for key, item := range items {
		if item.Action == ActionCreate || item.Action == ActionNewReview {
			for _, target := range locals[key].Outbound {
				if writeSources[target] {
					item.Dependent = append(item.Dependent, ActionLinkUpdate)
					break
				}
			}
			item.Dependent = append(item.Dependent, ActionActivation)
		}
		if item.Action == ActionBlocked || item.Action == ActionFailed {
			plan.Blocked = true
		}
		items[key] = item
	}
	itemKeys := make([]string, 0, len(items))
	for key := range items {
		itemKeys = append(itemKeys, key)
	}
	sort.Strings(itemKeys)
	for _, key := range itemKeys {
		plan.Items = append(plan.Items, items[key])
	}
	return plan
}

func classify(key string, local LocalDocument, input Input) Item {
	item := itemIdentity(key, local)
	if local.Failed != "" {
		item.Action = ActionFailed
		item.Reason = local.Failed
		return item
	}
	documentState := stateForLocal(input.State, local, key)
	active := documentState.ActiveTarget
	if active == nil {
		item.Action = ActionCreate
		item.Generation = 1
		item.Reason = "no active target"
		return item
	}
	remote, known := input.Remote[key]
	if !known || !remote.Exists || !remote.Accessible || remote.Trashed || remote.Ambiguous || remote.MetadataMismatch {
		if input.NewReview {
			item.Action = ActionNewReview
			item.Generation = active.Generation + 1
			item.Reason = "explicit new review replaces an unavailable mapping without changing it"
			return item
		}
		item.Action = ActionBlocked
		item.Generation = active.Generation
		switch {
		case !known || !remote.Exists:
			item.Reason = "active target is missing"
		case !remote.Accessible:
			item.Reason = "active target is inaccessible"
		case remote.Trashed:
			item.Reason = "active target is trashed"
		case remote.MetadataMismatch:
			item.Reason = "active target metadata does not match local state"
		default:
			item.Reason = "active target is ambiguous"
		}
		return item
	}
	if remote.Version != "" && active.RemoteVersion != "" && remote.Version != active.RemoteVersion {
		item.Warnings = append(item.Warnings, "remote version changed; active review content remains immutable")
	}
	if remote.UnresolvedComments > 0 {
		item.Warnings = append(item.Warnings, fmt.Sprintf("%d unresolved comment threads remain on the active review copy", remote.UnresolvedComments))
	}
	if remote.SuggestionsDetected {
		item.Warnings = append(item.Warnings, "unresolved suggestions remain on the active review copy")
	}
	if remote.Name != "" && local.Title != "" && remote.Name != local.Title {
		item.Warnings = append(item.Warnings, fmt.Sprintf("remote file was renamed to %q", remote.Name))
	}
	if input.ExpectedReviewFolder != "" && remote.ParentID != "" && remote.ParentID != input.ExpectedReviewFolder {
		item.Warnings = append(item.Warnings, "remote file moved outside the configured review folder")
	}
	if input.NewReview {
		item.Action = ActionNewReview
		item.Generation = active.Generation + 1
		item.Reason = "explicit new review requested"
		return item
	}
	if local.SourceHash == documentState.SourceHash && local.RenderHash == documentState.RenderHash {
		item.Action = ActionSkip
		item.Generation = active.Generation
		item.Reason = "render inputs are unchanged"
		return item
	}
	item.Action = ActionNewReview
	item.Generation = active.Generation + 1
	item.Reason = "local source or render inputs changed"
	return item
}

func stateForLocal(value *state.State, local LocalDocument, key string) state.Document {
	if local.TargetKey != "" {
		if stored, ok := value.Targets[key]; ok {
			return stored
		}
		return value.Documents[key]
	}
	return value.Documents[key]
}

func hasTargetLocals(locals []LocalDocument) bool {
	for _, local := range locals {
		if local.TargetKey != "" {
			return true
		}
	}
	return false
}

func localKey(local LocalDocument) string {
	if local.TargetKey != "" {
		return local.TargetKey
	}
	return local.SourceKey
}

func itemIdentity(key string, local LocalDocument) Item {
	if local.TargetKey == "" {
		return Item{SourceKey: key}
	}
	return Item{Target: key, PublicationID: local.PublicationID, PublicationKind: local.PublicationKind, SourceKey: local.SourceKey, MemberCount: local.MemberCount}
}

func inboundClosure(locals map[string]LocalDocument, roots map[string]bool) map[string]bool {
	closure := map[string]bool{}
	queue := []string{}
	for root := range roots {
		closure[root] = true
		queue = append(queue, root)
	}
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]
		for source, local := range locals {
			if closure[source] {
				continue
			}
			if slices.Contains(local.Outbound, target) {
				closure[source] = true
				queue = append(queue, source)
			}
		}
	}
	return closure
}
