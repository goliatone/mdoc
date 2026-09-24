package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/planner"
)

func TestHumanAndJSONReportsUseSameItemsAndCounts(t *testing.T) {
	plan := planner.Plan{Items: []planner.Item{{SourceKey: "a.md", Action: planner.ActionCreate, Dependent: []planner.Action{planner.ActionLinkUpdate, planner.ActionActivation}}, {SourceKey: "b.md", Action: planner.ActionSkip}}}
	report := FromPlan(plan, "https://example.test")
	var human, encoded strings.Builder
	if err := WritePlan(&human, report, false, false); err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(&encoded, report, true, false); err != nil {
		t.Fatal(err)
	}
	var decoded PlanReport
	if err := json.Unmarshal([]byte(encoded.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Items) != len(report.Items) || decoded.Counts["create"] != 1 || decoded.Counts["link_update"] != 1 || !strings.Contains(human.String(), "create a.md") || !strings.Contains(human.String(), "create=1") {
		t.Fatalf("human=%q json=%#v", human.String(), decoded)
	}
	if decoded.Result != "ok" {
		t.Fatalf("plan result = %q", decoded.Result)
	}
}

func TestBlockedPlanHasBlockedResult(t *testing.T) {
	value := FromPlan(planner.Plan{Blocked: true}, "")
	if value.Result != "blocked" {
		t.Fatalf("plan result = %q", value.Result)
	}
}

func TestQuietReportPrintsOnlyEntryURL(t *testing.T) {
	var output strings.Builder
	value := FromPlan(planner.Plan{Items: []planner.Item{{SourceKey: "a", Action: planner.ActionSkip}}}, "https://example.test")
	if err := WritePlan(&output, value, false, true); err != nil {
		t.Fatal(err)
	}
	if output.String() != "https://example.test\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestHumanStatusReportsIncludeLocalChanges(t *testing.T) {
	changes := []LocalChange{{Source: "README.md", State: "modified"}}
	var local strings.Builder
	if err := WriteLocalStatus(&local, LocalStatusReport{Result: "ok", Profile: "review", Checks: map[string]StatusCheck{}, Changes: changes}, false, false); err != nil {
		t.Fatal(err)
	}
	var remote strings.Builder
	if err := WriteRemoteStatus(&remote, RemoteStatusReport{Result: "ok", Profile: "review", Changes: changes}, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(local.String(), "modified README.md") || !strings.Contains(remote.String(), "modified README.md") {
		t.Fatalf("local=%q remote=%q", local.String(), remote.String())
	}
}

func TestVersionTwoPlanUsesTargetIdentityWithoutContent(t *testing.T) {
	plan := planner.Plan{Items: []planner.Item{{Target: "publication:report", PublicationID: "report", PublicationKind: "bundle", MemberCount: 3, Action: planner.ActionCreate}}}
	value := FromPlanVersion(plan, "", 2)
	var output strings.Builder
	if err := WritePlan(&output, value, true, false); err != nil {
		t.Fatal(err)
	}
	encoded := output.String()
	for _, expected := range []string{`"schema_version": 2`, `"target": "publication:report"`, `"publication_id": "report"`, `"publication_kind": "bundle"`, `"member_count": 3`} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("missing %s in %s", expected, encoded)
		}
	}
	if strings.Contains(encoded, "field") || strings.Contains(encoded, "document body") {
		t.Fatalf("report leaked content: %s", encoded)
	}
}

func TestHumanVersionTwoStatusAndValidationNameTargets(t *testing.T) {
	targets := []TargetSummary{{Target: "publication:report", PublicationID: "report", PublicationKind: "bundle", MemberCount: 3}}
	var validation strings.Builder
	if err := WriteValidation(&validation, ValidationReport{Documents: 3, Entry: "publication:report", Valid: true, Targets: targets}, false, false); err != nil {
		t.Fatal(err)
	}
	var status strings.Builder
	if err := WriteLocalStatus(&status, LocalStatusReport{Result: "ok", Profile: "review", Checks: map[string]StatusCheck{}, Targets: targets}, false, false); err != nil {
		t.Fatal(err)
	}
	for name, output := range map[string]string{"validation": validation.String(), "status": status.String()} {
		if !strings.Contains(output, "Target publication:report (bundle, members=3)") {
			t.Fatalf("%s output = %q", name, output)
		}
	}
}

func TestHumanStatusPrintsEveryReviewReadinessState(t *testing.T) {
	states := []string{"disabled", "baseline_available", "pull_ready", "changed", "blocked", "unavailable"}
	targets := make([]TargetSummary, 0, len(states))
	for _, state := range states {
		targets = append(targets, TargetSummary{Target: "publication:" + state, PublicationID: state, PublicationKind: "source", Review: &ReviewTargetStatus{PullEnabled: state != "disabled", BaselineAvailable: state == "baseline_available" || state == "pull_ready" || state == "changed" || state == "blocked", State: state}})
	}
	var output strings.Builder
	if err := WriteLocalStatus(&output, LocalStatusReport{Result: "ok", Profile: "work", Checks: map[string]StatusCheck{}, Targets: targets}, false, false); err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if !strings.Contains(output.String(), "Review publication:"+state+": "+state) {
			t.Fatalf("human status missing %s: %s", state, output.String())
		}
	}
}

func TestLegacyPlanJSONKeepsSchemaOneSourceShape(t *testing.T) {
	value := FromPlan(planner.Plan{Items: []planner.Item{{SourceKey: "a.md", Action: planner.ActionSkip}}}, "")
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"target"`) || strings.Contains(string(data), `"publication_id"`) || !strings.Contains(string(data), `"schema_version":1`) || !strings.Contains(string(data), `"source":"a.md"`) {
		t.Fatalf("legacy JSON = %s", data)
	}
}

func TestPlanJSONGoldenSchemas(t *testing.T) {
	tests := []struct {
		name    string
		value   PlanReport
		fixture string
	}{
		{name: "version 1", value: FromPlan(planner.Plan{Items: []planner.Item{{SourceKey: "a.md", Action: planner.ActionSkip}}}, ""), fixture: "plan-v1.json"},
		{name: "version 2", value: FromPlanVersion(planner.Plan{Items: []planner.Item{{Target: "publication:report", PublicationID: "report", PublicationKind: "bundle", MemberCount: 3, Action: planner.ActionCreate}}}, "", 2), fixture: "plan-v2.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			if err := WritePlan(&output, test.value, true, false); err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join("..", "..", "testdata", "reports", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != string(expected) {
				t.Fatalf("plan JSON differs from %s:\n%s", test.fixture, output.String())
			}
		})
	}
}
