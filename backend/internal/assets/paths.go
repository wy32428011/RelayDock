package assets

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Use one portable spelling for every file across NTFS, APFS and Linux.
// No device names, alternate streams, trailing dots/spaces or traversal.
var pathPart = regexp.MustCompile(`^[A-Za-z0-9._+@%~\-]{1,120}$`)

func safePath(name string) bool {
	if len(name) > 240 || !fs.ValidPath(name) || name == "." {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if !pathPart.MatchString(part) || strings.HasSuffix(part, ".") {
			return false
		}
		base, _, _ := strings.Cut(strings.ToUpper(part), ".")
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return false
		}
		if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
			return false
		}
	}
	return true
}

func localAbsolute(name string) bool {
	return len(name) <= 4096 && !strings.ContainsRune(name, 0) && filepath.IsAbs(name) && !strings.HasPrefix(filepath.ToSlash(name), "//")
}

// Root keeps access anchored to the opened directory even if the source is
// renamed, and prevents symlink races from escaping it. Lstat also excludes
// links instead of intentionally accepting even links internal to the bundle.
func openDirectory(name string) (*os.Root, error) {
	return checkedDir(func() (os.FileInfo, error) { return os.Lstat(name) }, func() (*os.Root, error) { return os.OpenRoot(name) })
}

func openChildDirectory(parent *os.Root, name string) (*os.Root, error) {
	return checkedDir(func() (os.FileInfo, error) { return parent.Lstat(name) }, func() (*os.Root, error) { return parent.OpenRoot(name) })
}

func checkedDir(lstat func() (os.FileInfo, error), open func() (*os.Root, error)) (*os.Root, error) {
	first, err := lstat()
	if err != nil {
		return nil, err
	}
	if !first.IsDir() {
		return nil, fs.ErrInvalid
	}
	root, err := open()
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !actual.IsDir() || !os.SameFile(first, actual) {
		root.Close()
		return nil, fs.ErrInvalid
	}
	return root, nil
}

func openRegular(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	// Hold each directory open while descending; do not follow links in any
	// relative path component. Go's Root adds containment if the tree races.
	parent, leaf, more := strings.Cut(name, "/")
	if more {
		sub, err := openChildDirectory(root, parent)
		if err != nil {
			return nil, nil, err
		}
		defer sub.Close()
		return openRegular(sub, leaf)
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fs.ErrInvalid
	}
	f, err := root.OpenFile(name, sourceOpenFlags, 0)
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		f.Close()
		return nil, nil, fs.ErrInvalid
	}
	return f, after, nil
}

func readSmallFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, info, err := openRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > limit {
		return nil, fs.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || int64(len(data)) != info.Size() {
		return nil, fs.ErrInvalid
	}
	return data, nil
}
