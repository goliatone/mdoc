package contributionstate

import (
	"os"
	"os/exec"
	"path/filepath"
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
