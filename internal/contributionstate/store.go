package contributionstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/goliatone/mdoc/contribution"
	"golang.org/x/sys/unix"
)

// Store is a private, explicitly located journal. Lock ownership spans dispatch.
type Store struct {
	dir, lock *os.File
	path      string
}

func problem(m string) error { return &contribution.Error{Code: contribution.InvalidInput, Message: m} }
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, problem("an explicit journal directory is required")
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, problem("invalid journal path")
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return nil, problem("cannot create journal")
	}
	info, e := os.Lstat(abs)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, problem("journal must be a private directory (0700), not a symlink")
	}
	dir, e := os.Open(abs)
	if e != nil {
		return nil, problem("cannot open journal")
	}
	fd, e := unix.Openat(int(dir.Fd()), ".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if e != nil {
		dir.Close()
		return nil, problem("cannot open journal lock")
	}
	lock := os.NewFile(uintptr(fd), ".lock")
	stat, e := lock.Stat()
	if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		lock.Close()
		dir.Close()
		return nil, problem("journal lock must be private")
	}
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		lock.Close()
		dir.Close()
		return nil, problem("another contribution command owns this journal")
	}
	return &Store{dir: dir, lock: lock, path: abs}, nil
}
func (s *Store) Close() error {
	unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	s.lock.Close()
	return s.dir.Close()
}
func key(namespace, id string) string {
	b, _ := json.Marshal([]string{namespace, id})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) + ".json"
}
func (s *Store) Load(namespace, id string) (*Record, error) {
	fd, e := unix.Openat(int(s.dir.Fd()), key(namespace, id), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(e, unix.ENOENT) {
		return nil, nil
	}
	if e != nil {
		return nil, problem("cannot read journal record")
	}
	f := os.NewFile(uintptr(fd), "record")
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, problem("journal record must be private")
	}
	b, e := io.ReadAll(io.LimitReader(f, contribution.MaxPlan+1))
	if e != nil {
		return nil, problem("cannot read journal record")
	}
	var r Record
	if contribution.DecodeJSON(b, &r) != nil || r.Version != 1 {
		return nil, problem("journal record is invalid; preserve it for recovery")
	}
	switch r.State {
	case "prepared", "sending", "finished":
	default:
		return nil, problem("journal record has an invalid state")
	}
	return &r, nil
}
func (s *Store) Save(namespace, id string, r *Record) error {
	b, e := json.Marshal(r)
	if e != nil || len(b) > contribution.MaxPlan {
		return problem("journal record exceeds limit")
	}
	f, e := os.CreateTemp(s.path, ".record-*")
	if e != nil {
		return problem("cannot create journal record")
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return problem("cannot write journal record")
	}
	if e = f.Sync(); e != nil {
		return problem("cannot sync journal record")
	}
	if e = f.Close(); e != nil {
		return problem("cannot close journal record")
	}
	if e = unix.Renameat(int(s.dir.Fd()), filepath.Base(name), int(s.dir.Fd()), key(namespace, id)); e != nil {
		return problem("cannot replace journal record")
	}
	if e = s.dir.Sync(); e != nil {
		return problem("cannot sync journal directory")
	}
	return nil
}
