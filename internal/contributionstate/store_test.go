package contributionstate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestJournalProcessLock(t *testing.T) {
	if path := os.Getenv("MDOC_TEST_JOURNAL"); path != "" {
		s, e := Open(path)
		if e == nil {
			s.Close()
			os.Exit(2)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "journal")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalProcessLock$")
	cmd.Env = append(os.Environ(), "MDOC_TEST_JOURNAL="+path)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("lock not exclusive across processes: %s %v", b, e)
	}
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Save("ns", "op", &Record{Version: 1, State: "sending", Plan: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	r, e := s.Load("ns", "op")
	if e != nil || r.State != "sending" {
		t.Fatal("claim did not persist")
	}
	if e = os.Symlink("/tmp", filepath.Join(path, key("ns", "bad"))); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load("ns", "bad"); e == nil {
		t.Fatal("followed journal symlink")
	}
}

func TestJournalAncestryDurabilityAndRetry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "first", "second", "journal")
	var want []string
	for p := path; ; p = filepath.Dir(p) {
		want = append(want, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	// Fail the sync that persists the entry naming the first new ancestor.
	// The directories remain present; a retry must still sync that entry.
	var calls []string
	syncErr := errors.New("directory sync failed")
	store, err := open(path, func(p string) error {
		calls = append(calls, p)
		if _, err := os.Stat(path); err != nil {
			t.Fatal("sync before directory creation", err)
		}
		if _, err := os.Stat(filepath.Join(path, ".lock")); !os.IsNotExist(err) {
			t.Fatal("journal admitted before ancestry sync", err)
		}
		if p == root {
			return syncErr
		}
		return syncDirectory(p)
	})
	if err == nil || store != nil {
		t.Fatal("failed durability admitted a journal")
	}
	if !reflect.DeepEqual(calls, want[:4]) {
		t.Fatalf("sync ordering = %v, want %v", calls, want[:4])
	}
	calls = nil
	store, err = open(path, func(p string) error {
		calls = append(calls, p)
		return syncDirectory(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("retry skipped ancestry: %v", calls)
	}
	if err = store.Save("ns", "op", &Record{Version: 1, State: "sending", Plan: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Load("ns", "op")
	if err != nil || record == nil || record.State != "sending" {
		t.Fatal("durable claim lost", err)
	}
}
