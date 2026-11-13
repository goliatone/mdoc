//go:build !darwin && !linux

package portable

import "os"

func renameExclusive(fd int, from, to string) error {
	return fail("unavailable", "exclusive directory install requires macOS or Linux")
}

func openBundleFile(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
