//go:build linux

// Package fileops provides fd-relative file resolution for controlled file
// operations.
//
// It deliberately does not accept a path from a model and re-resolve it later.
// The caller opens a root once, then opens the target relative to that root
// with Linux openat2 constraints. The returned *os.File is the object to use
// for reads, writes, stat and identity checks; callers must not turn it back
// into a path and open that path again.
package fileops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	// ErrInvalidRoot means the configured root is not an absolute directory.
	ErrInvalidRoot = errors.New("invalid file root")
	// ErrInvalidPath means the target is not a relative path beneath the root.
	ErrInvalidPath = errors.New("invalid file path")
	// ErrUnsafePath means the kernel refused the path constraints.
	ErrUnsafePath = errors.New("unsafe file path")
)

// Root is an opened directory used as the base for fd-relative operations.
type Root struct {
	dir  *os.File
	path string
}

// OpenRoot opens an absolute directory as the base of a file-operation scope.
// The path itself is resolved once here; every target is opened relative to
// the resulting directory fd.
func OpenRoot(path string) (*Root, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: %q is not absolute", ErrInvalidRoot, path)
	}
	dir, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q: %v", ErrInvalidRoot, path, err)
	}
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		_ = dir.Close()
		return nil, fmt.Errorf("%w: %q is not a directory", ErrInvalidRoot, path)
	}
	return &Root{dir: dir, path: filepath.Clean(path)}, nil
}

// Path reports the root path for diagnostics only. It must not be used to
// re-open a target by string concatenation.
func (r *Root) Path() string { return r.path }

// Close releases the root directory fd.
func (r *Root) Close() error {
	if r == nil || r.dir == nil {
		return nil
	}
	return r.dir.Close()
}

// Open opens a target relative to the root with openat2.
//
// The constraints reject absolute paths, "..", magic links, ordinary symlinks,
// and crossing a mount point. The caller receives the opened fd; this is the
// object whose identity and content are stable against a later rename or
// symlink swap at the same string path.
func (r *Root) Open(relative string, flags int, mode uint32) (*os.File, error) {
	if r == nil || r.dir == nil {
		return nil, fmt.Errorf("%w: root is closed", ErrInvalidRoot)
	}
	if err := validateRelative(relative); err != nil {
		return nil, err
	}
	how := &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Mode:    uint64(mode),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(int(r.dir.Fd()), relative, how)
	if err != nil {
		if errors.Is(err, syscall.ENOSYS) {
			return nil, fmt.Errorf("%w: openat2 is not supported by this kernel", ErrUnsafePath)
		}
		return nil, fmt.Errorf("%w: open %q beneath %q: %v", ErrUnsafePath, relative, r.path, err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(r.path, relative))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: wrap fd for %q", ErrUnsafePath, relative)
	}
	return file, nil
}

// Read opens an existing target read-only.
func (r *Root) Read(relative string) (*os.File, error) {
	return r.Open(relative, os.O_RDONLY, 0)
}

// Write opens a target for writing, creating/truncating it as requested.
func (r *Root) Write(relative string, flags int, mode os.FileMode) (*os.File, error) {
	writeFlags := (flags &^ unix.O_ACCMODE) | os.O_WRONLY
	return r.Open(relative, writeFlags, uint32(mode.Perm()))
}

func validateRelative(relative string) error {
	if relative == "" || filepath.IsAbs(relative) {
		return fmt.Errorf("%w: %q must be a non-empty relative path", ErrInvalidPath, relative)
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == ".." {
			return fmt.Errorf("%w: %q contains a parent component", ErrInvalidPath, relative)
		}
	}
	cleaned := filepath.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q escapes or names the root", ErrInvalidPath, relative)
	}
	return nil
}

// Identity is the kernel identity of an opened object. Two path opens that
// return different identities are not the same file even if the path string
// is unchanged.
type Identity struct {
	Dev  uint64
	Ino  uint64
	Mode uint32
	Size int64
}

// Stat returns the identity of an open file descriptor.
func Stat(file *os.File) (Identity, error) {
	if file == nil {
		return Identity{}, errors.New("fileops: nil file")
	}
	info, err := file.Stat()
	if err != nil {
		return Identity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("fileops: unsupported stat type")
	}
	return Identity{
		Dev:  uint64(stat.Dev),
		Ino:  uint64(stat.Ino),
		Mode: uint32(stat.Mode),
		Size: info.Size(),
	}, nil
}
