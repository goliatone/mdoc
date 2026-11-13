package portable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// OutputStore installs a fully validated file set into an explicit new directory.
// Implementations must reject existing destinations and never partially install.
type OutputStore interface {
	Install(context.Context, string, map[string][]byte) error
}
type LocalOutputStore struct{}

func (LocalOutputStore) Install(ctx context.Context, destination string, files map[string][]byte) error {
	absolute, e := filepath.Abs(destination)
	if e != nil {
		return e
	}
	base := filepath.Base(absolute)
	if base == "." || base == string(filepath.Separator) {
		return fail("path_escape", "invalid destination")
	}
	parent, e := os.OpenRoot(filepath.Dir(absolute))
	if e != nil {
		return fail("output_failure", "destination parent must already exist")
	}
	defer parent.Close()
	if _, e = parent.Lstat(base); e == nil {
		return fail("existing_output", "destination already exists")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	var nonce [16]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	stage := ".mdoc-bundle-" + hex.EncodeToString(nonce[:])
	if e = parent.Mkdir(stage, 0700); e != nil {
		return e
	}
	defer parent.RemoveAll(stage)
	root, e := parent.OpenRoot(stage)
	if e != nil {
		return e
	}
	defer root.Close()
	names := make([]string, 0, len(files))
	var reservations []Reservation
	for name := range files {
		if e = reserve(&reservations, Reservation{Path: name, Kind: "file"}); e != nil {
			return e
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if e = ctx.Err(); e != nil {
			return e
		}
		if dir := path.Dir(name); dir != "." {
			if e = root.MkdirAll(filepath.FromSlash(dir), 0755); e != nil {
				return e
			}
		}
		f, e := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if e != nil {
			return e
		}
		_, e = f.Write(files[name])
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	fd, e := parent.Open(".")
	if e != nil {
		return e
	}
	defer fd.Close()
	if e = renameExclusive(int(fd.Fd()), stage, base); e != nil {
		if errors.Is(e, os.ErrExist) {
			return fail("existing_output", "destination appeared during export")
		}
		return fail("output_failure", "exclusive installation failed")
	}
	return nil
}
