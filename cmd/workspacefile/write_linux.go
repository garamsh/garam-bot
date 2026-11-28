package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// openat2 is unix.Openat2, which a test replaces to model a kernel without it.
var openat2 = unix.Openat2

// writeFile replaces the file at path with in, whole, and follows no symbolic link (interface 3,
// FILE-3). The directory is opened with no link resolved at any of its components, and a temporary
// file written in it is renamed over the path's last component: a link there is replaced, never
// followed, and a reader sees the old content or the new and never part of either. A file that exists
// keeps its mode, and a new one is 0644.
func writeFile(path string, in io.Reader) error {
	dir, base := filepath.Split(path)
	if !filepath.IsAbs(path) || base == "" {
		return fmt.Errorf("path %q is not an absolute path of a file", path)
	}
	dirfd, err := openat2(unix.AT_FDCWD, dir, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	switch {
	case errors.Is(err, unix.ELOOP):
		return fmt.Errorf("%w: %s", errLink, dir)
	case errors.Is(err, unix.ENOSYS):
		// No fallback: a write that might follow a link is no write FILE-3 allows.
		return fmt.Errorf("%w: openat2 is not offered here, and Linux 5.6 or later offers it", errUnsupported)
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer unix.Close(dirfd)

	mode := uint32(0o644)
	var st unix.Stat_t
	if unix.Fstatat(dirfd, base, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
		mode = st.Mode & 0o777
	}
	tmp := "." + base + ".tmp-" + rand.Text()[:8]
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &os.PathError{Op: "create", Path: dir + tmp, Err: err}
	}
	defer unix.Unlinkat(dirfd, tmp, 0) //nolint:errcheck // gone once renamed
	f := os.NewFile(uintptr(fd), dir+tmp)
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(os.FileMode(mode)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(dirfd, tmp, dirfd, base); err != nil {
		return &os.PathError{Op: "rename", Path: path, Err: err}
	}

	return nil
}
