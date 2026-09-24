package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNeutralProjectAndProfileLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mdoc.yaml")
	if err := WriteNeutralProject(path, "testing", false); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(context.Background(), ResolverOptions{ProjectPath: path, GlobalPath: filepath.Join(t.TempDir(), "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != 2 || resolved.ProfileName != "testing" || resolved.ProjectID == "" || resolved.WorkspaceID != "" {
		t.Fatalf("neutral project = %#v", resolved)
	}
	if strings.Contains(resolved.Profile.Destination.StagingFolderName, "Momentum") || resolved.Profile.Style.ReferenceDOCX != "" {
		t.Fatalf("neutral profile contains legacy defaults: %#v", resolved.Profile)
	}
	if err := AddProfile(path, "review-two", false); err != nil {
		t.Fatal(err)
	}
	_, names, err := ListProfiles(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "review-two,testing" {
		t.Fatalf("profiles = %#v", names)
	}
	if err := AddProfile(path, "review-two", false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate profile error = %v", err)
	}
	if err := AddProfile(path, "review-two", true); err != nil {
		t.Fatal(err)
	}
}

func TestWriteNeutralProjectDoesNotOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mdoc.yaml")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteNeutralProject(path, "testing", false); err == nil {
		t.Fatal("expected existing config error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("existing config changed to %q", data)
	}
}

func TestNewProjectsWithSameProfileReceiveDifferentIdentity(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "one", ".mdoc.yaml"), filepath.Join(root, "two", ".mdoc.yaml")}
	identities := []string{}
	for _, path := range paths {
		if err := WriteNeutralProject(path, "default", false); err != nil {
			t.Fatal(err)
		}
		resolved, err := Resolve(context.Background(), ResolverOptions{ProjectPath: path, GlobalPath: filepath.Join(root, "missing.yaml"), Environment: map[string]string{}})
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, resolved.Identity())
	}
	if identities[0] == identities[1] {
		t.Fatalf("new project identities collided: %q", identities[0])
	}
}

func TestProfileSelectionOrderAndAmbiguity(t *testing.T) {
	profiles := map[string]RuntimeProfile{"alpha": NeutralProfile(), "beta": NeutralProfile()}
	for _, test := range []struct {
		name        string
		explicit    string
		environment string
		project     string
		global      string
		want        string
	}{
		{"explicit", "alpha", "beta", "beta", "beta", "alpha"},
		{"environment", "", "alpha", "beta", "beta", "alpha"},
		{"project", "", "", "alpha", "beta", "alpha"},
		{"global", "", "", "", "beta", "beta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectRuntimeProfile(test.explicit, test.environment, test.project, test.global, profiles)
			if err != nil || got != test.want {
				t.Fatalf("selection = %q, %v", got, err)
			}
		})
	}
	_, err := selectRuntimeProfile("", "", "", "", profiles)
	if err == nil || !strings.Contains(err.Error(), "alpha, beta") || !strings.Contains(err.Error(), "--profile") {
		t.Fatalf("ambiguity error = %v", err)
	}
}
