package planner

import (
	"testing"

	"github.com/goliatone/mdoc/internal/state"
)

func TestPlannerActionMatrix(t *testing.T) {
	tests := []struct {
		name      string
		local     LocalDocument
		document  state.Document
		remote    RemoteDocument
		newReview bool
		want      Action
	}{
		{"create", LocalDocument{SourceKey: "a", SourceHash: "s", RenderHash: "r"}, state.Document{}, RemoteDocument{}, false, ActionCreate},
		{"skip", LocalDocument{SourceKey: "a", SourceHash: "s", RenderHash: "r"}, stored("s", "r"), available(), false, ActionSkip},
		{"new review", LocalDocument{SourceKey: "a", SourceHash: "changed", RenderHash: "r"}, stored("s", "r"), available(), false, ActionNewReview},
		{"explicit review", LocalDocument{SourceKey: "a", SourceHash: "s", RenderHash: "r"}, stored("s", "r"), available(), true, ActionNewReview},
		{"missing", LocalDocument{SourceKey: "a", SourceHash: "s", RenderHash: "r"}, stored("s", "r"), RemoteDocument{}, false, ActionBlocked},
		{"ambiguous", LocalDocument{SourceKey: "a", SourceHash: "s", RenderHash: "r"}, stored("s", "r"), RemoteDocument{Exists: true, Accessible: true, Ambiguous: true}, false, ActionBlocked},
		{"metadata mismatch", LocalDocument{SourceKey: "a", SourceHash: "s", RenderHash: "r"}, stored("s", "r"), RemoteDocument{Exists: true, Accessible: true, MetadataMismatch: true}, false, ActionBlocked},
		{"failed", LocalDocument{SourceKey: "a", Failed: "parse failed"}, stored("s", "r"), available(), false, ActionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publishState := state.New("workspace", "work")
			publishState.Documents["a"] = test.document
			plan := Build(Input{Local: []LocalDocument{test.local}, State: publishState, Remote: map[string]RemoteDocument{"a": test.remote}, NewReview: test.newReview})
			if len(plan.Items) != 1 || plan.Items[0].Action != test.want {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestPlannerBuildsTransitiveInboundClosure(t *testing.T) {
	publishState := state.New("workspace", "work")
	remote := map[string]RemoteDocument{}
	locals := []LocalDocument{{SourceKey: "a", SourceHash: "changed", RenderHash: "r"}, {SourceKey: "b", SourceHash: "s", RenderHash: "r", Outbound: []string{"a"}}, {SourceKey: "c", SourceHash: "s", RenderHash: "r", Outbound: []string{"b"}}}
	for _, local := range locals {
		publishState.Documents[local.SourceKey] = stored("s", "r")
		remote[local.SourceKey] = available()
	}
	plan := Build(Input{Local: locals, State: publishState, Remote: remote, Selected: map[string]bool{"a": true}})
	if len(plan.Items) != 3 {
		t.Fatalf("plan = %#v", plan)
	}
	for index, item := range plan.Items {
		wantDependent := 2
		if index == 0 {
			wantDependent = 1
		}
		if item.Action != ActionNewReview || len(item.Dependent) != wantDependent {
			t.Fatalf("item = %#v", item)
		}
	}
}

func TestPlannerUnchangedRunHasZeroWritesAndReportsRemoteChange(t *testing.T) {
	publishState := state.New("workspace", "work")
	publishState.Documents["a"] = stored("s", "r")
	remote := available()
	remote.Version = "2"
	plan := Build(Input{Local: []LocalDocument{{SourceKey: "a", Title: "A", SourceHash: "s", RenderHash: "r"}}, State: publishState, Remote: map[string]RemoteDocument{"a": remote}})
	if plan.WriteCount() != 0 || plan.Items[0].Action != ActionSkip || len(plan.Items[0].Warnings) == 0 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerReportsCommentsAndSuggestionsWithoutReplacingActive(t *testing.T) {
	publishState := state.New("workspace", "work")
	publishState.Documents["a"] = stored("s", "r")
	remote := available()
	remote.UnresolvedComments = 2
	remote.SuggestionsDetected = true
	plan := Build(Input{Local: []LocalDocument{{SourceKey: "a", Title: "A", SourceHash: "s", RenderHash: "r"}}, State: publishState, Remote: map[string]RemoteDocument{"a": remote}})
	if plan.Items[0].Action != ActionSkip || len(plan.Items[0].Warnings) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerBlocksUnavailableTargetUnlessNewReviewExplicit(t *testing.T) {
	publishState := state.New("workspace", "work")
	publishState.Documents["a"] = stored("s", "r")
	blocked := Build(Input{Local: []LocalDocument{{SourceKey: "a", SourceHash: "s", RenderHash: "r"}}, State: publishState, Remote: map[string]RemoteDocument{"a": {Exists: true, Trashed: true}}})
	allowed := Build(Input{Local: []LocalDocument{{SourceKey: "a", SourceHash: "s", RenderHash: "r"}}, State: publishState, Remote: map[string]RemoteDocument{"a": {Exists: true, Trashed: true}}, NewReview: true})
	if !blocked.Blocked || allowed.Blocked || allowed.Items[0].Action != ActionNewReview {
		t.Fatalf("blocked=%#v allowed=%#v", blocked, allowed)
	}
}

func TestPlannerReportsOrphanedState(t *testing.T) {
	publishState := state.New("workspace", "work")
	publishState.Documents["old.md"] = stored("s", "r")
	plan := Build(Input{Local: []LocalDocument{{SourceKey: "new.md", SourceHash: "s", RenderHash: "r"}}, State: publishState, Remote: map[string]RemoteDocument{}})
	if len(plan.Items) != 2 || plan.Items[1].Action != ActionOrphan {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerUsesPublicationTargetsAndKeepsDuplicateSourcesSeparate(t *testing.T) {
	publishState := state.New("workspace", "work")
	locals := []LocalTarget{
		{TargetKey: "publication:first", PublicationID: "first", PublicationKind: "source", SourceKey: "shared.md", SourceHash: "s", RenderHash: "r"},
		{TargetKey: "publication:second", PublicationID: "second", PublicationKind: "source", SourceKey: "shared.md", SourceHash: "s", RenderHash: "r"},
		{TargetKey: "publication:bundle", PublicationID: "bundle", PublicationKind: "bundle", MemberCount: 2, SourceHash: "s", RenderHash: "r"},
	}
	plan := Build(Input{Local: locals, State: publishState, Remote: map[string]RemoteDocument{}})
	if len(plan.Items) != 3 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Items[0].Target != "publication:bundle" || plan.Items[0].MemberCount != 2 || plan.Items[1].SourceKey != "shared.md" || plan.Items[2].SourceKey != "shared.md" {
		t.Fatalf("items = %#v", plan.Items)
	}
}

func TestSelectedBundleIgnoresUnrelatedRemoteFailure(t *testing.T) {
	publishState := state.New("workspace", "work")
	publishState.Documents["publication:bundle"] = stored("s", "r")
	publishState.Documents["publication:other"] = stored("s", "r")
	locals := []LocalTarget{{TargetKey: "publication:bundle", PublicationID: "bundle", PublicationKind: "bundle", SourceHash: "s", RenderHash: "r"}, {TargetKey: "publication:other", PublicationID: "other", PublicationKind: "source", SourceKey: "other.md", SourceHash: "s", RenderHash: "r"}}
	plan := Build(Input{Local: locals, State: publishState, Remote: map[string]RemoteDocument{"publication:bundle": available(), "publication:other": {Exists: true, Ambiguous: true}}, Selected: map[string]bool{"publication:bundle": true}})
	if plan.Blocked || len(plan.Items) != 1 || plan.Items[0].Target != "publication:bundle" || plan.Items[0].Action != ActionSkip {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestInternalMemberLinksDoNotCreateRemoteClosure(t *testing.T) {
	publishState := state.New("workspace", "work")
	publishState.Documents["publication:bundle"] = stored("old", "r")
	publishState.Documents["publication:other"] = stored("s", "r")
	locals := []LocalTarget{{TargetKey: "publication:bundle", PublicationID: "bundle", PublicationKind: "bundle", SourceHash: "new", RenderHash: "r"}, {TargetKey: "publication:other", PublicationID: "other", PublicationKind: "source", SourceKey: "other.md", SourceHash: "s", RenderHash: "r"}}
	remote := map[string]RemoteDocument{"publication:bundle": available(), "publication:other": available()}
	plan := Build(Input{Local: locals, State: publishState, Remote: remote, Selected: map[string]bool{"publication:bundle": true}})
	if len(plan.Items) != 1 || len(plan.Items[0].Dependent) != 1 || plan.Items[0].Dependent[0] != ActionActivation {
		t.Fatalf("plan = %#v", plan)
	}
}

func stored(sourceHash, renderHash string) state.Document {
	target := state.Target{FileID: "id", URL: "url", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready", RemoteVersion: "1"}
	return state.Document{SourceHash: sourceHash, RenderHash: renderHash, ActiveTarget: &target}
}
func available() RemoteDocument {
	return RemoteDocument{Exists: true, Accessible: true, Version: "1", Name: "A", ParentID: "review"}
}
