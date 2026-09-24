package state

import "testing"

func TestReconcileBuildsActiveAndPriorFromCompleteReadySets(t *testing.T) {
	input := ReconcileInput{
		WorkspaceID: "workspace", Profile: "work",
		Folders: []RemoteFolder{{FileID: "staging", Role: "staging"}, {FileID: "review", Role: "review"}},
		Targets: []RemoteTarget{
			readyRemote("a-one", "docs/a.md", "set-one", 1, "review"),
			readyRemote("a-two", "docs/a.md", "set-two", 2, "review"),
		},
	}
	result, err := Reconcile(input)
	if err != nil {
		t.Fatal(err)
	}
	document := result.State.Documents["docs/a.md"]
	if document.ActiveTarget == nil || document.ActiveTarget.FileID != "a-two" || len(document.PriorTargets) != 1 {
		t.Fatalf("document = %#v", document)
	}
}

func TestReconcileLeavesIncompleteSetPending(t *testing.T) {
	target := readyRemote("a", "docs/a.md", "set", 1, "staging")
	target.ExpectedSetSize = 2
	result, err := Reconcile(ReconcileInput{
		WorkspaceID: "workspace", Profile: "work", Folders: []RemoteFolder{{FileID: "review", Role: "review"}}, Targets: []RemoteTarget{target},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PendingSets) != 1 || len(result.State.Documents) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestReconcileCanonicalizesRemoteSourceKeys(t *testing.T) {
	result, err := Reconcile(ReconcileInput{
		WorkspaceID: "workspace", Profile: "work",
		Folders: []RemoteFolder{{FileID: "review", Role: "review"}},
		Targets: []RemoteTarget{readyRemote("file", "docs/cafe\u0301.md", "set", 1, "review")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.State.Documents["docs/caf\u00e9.md"]; !ok {
		t.Fatalf("documents = %#v", result.State.Documents)
	}
}

func TestReconcileLeavesMixedOperationSetPending(t *testing.T) {
	first := readyRemote("a", "docs/a.md", "set", 1, "review")
	second := readyRemote("b", "docs/b.md", "set", 1, "review")
	first.ExpectedSetSize = 2
	second.ExpectedSetSize = 2
	second.OperationID = "different-operation"
	result, err := Reconcile(ReconcileInput{
		WorkspaceID: "workspace", Profile: "work", Folders: []RemoteFolder{{FileID: "review", Role: "review"}}, Targets: []RemoteTarget{first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PendingSets) != 1 || len(result.State.Documents) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestReconcileBlocksDuplicates(t *testing.T) {
	base := readyRemote("a", "docs/a.md", "set-one", 1, "review")
	duplicate := readyRemote("b", "docs/a.md", "set-two", 1, "review")
	_, err := Reconcile(ReconcileInput{
		WorkspaceID: "workspace", Profile: "work", Folders: []RemoteFolder{{FileID: "review", Role: "review"}}, Targets: []RemoteTarget{base, duplicate},
	})
	if err == nil {
		t.Fatal("expected duplicate target error")
	}
}

func TestReconcileBlocksDuplicateFolderRoles(t *testing.T) {
	_, err := Reconcile(ReconcileInput{WorkspaceID: "workspace", Profile: "work", Folders: []RemoteFolder{{FileID: "one", Role: "review"}, {FileID: "two", Role: "review"}}})
	if err == nil {
		t.Fatal("expected duplicate folder conflict")
	}
}

func readyRemote(fileID, source, set string, generation int, parent string) RemoteTarget {
	return RemoteTarget{
		Target:    Target{FileID: fileID, URL: "https://docs.google.com/document/d/" + fileID + "/edit", ReviewSetID: set, Generation: generation, OperationID: "operation-" + fileID, PublishStatus: "ready"},
		SourceKey: source, ExpectedSetSize: 1, ParentID: parent,
	}
}
