// Package candidate validates isolation boundaries before candidate resources
// are opened. Only the explicit InitRoot operation mutates the filesystem.
package candidate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type Root struct {
	path string
	info os.FileInfo
}

const (
	MarkerName    = ".nimbus-candidate-root"
	markerContent = "nimbus-candidate-root/v1\n"
)

// NewRoot requires an existing real directory with no symlinked path component.
func NewRoot(path string, protectedPaths ...string) (Root, error) {
	canonical, directory, info, err := openRoot(path)
	if err != nil {
		return Root{}, err
	}
	defer directory.Close()
	if err := validateRootScope(canonical, protectedPaths); err != nil {
		return Root{}, err
	}
	if err := validateMarkerAt(directory); err != nil {
		return Root{}, err
	}
	return Root{path: canonical, info: info}, nil
}

// InitRoot explicitly marks an existing, isolated directory as a candidate
// root. Runtime startup never creates this marker implicitly.
func InitRoot(path string, protectedPaths ...string) error {
	canonical, directory, _, err := openRoot(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := validateRootScope(canonical, protectedPaths); err != nil {
		return err
	}
	fd, err := unix.Openat(int(directory.Fd()), MarkerName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return fmt.Errorf("initialize candidate root marker: %w", err)
	}
	marker := os.NewFile(uintptr(fd), MarkerName)
	if marker == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("initialize candidate root marker: invalid descriptor")
	}
	committed := false
	defer func() {
		_ = marker.Close()
		if !committed {
			_ = unix.Unlinkat(int(directory.Fd()), MarkerName, 0)
		}
	}()
	info, err := marker.Stat()
	if err != nil {
		return fmt.Errorf("inspect candidate root marker: %w", err)
	}
	if err := validateMarkerInfo(info); err != nil {
		return err
	}
	if _, err := io.WriteString(marker, markerContent); err != nil {
		return fmt.Errorf("initialize candidate root marker: %w", err)
	}
	if err := marker.Sync(); err != nil {
		return fmt.Errorf("sync candidate root marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("close candidate root marker: %w", err)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync candidate root directory: %w", err)
	}
	committed = true
	return nil
}

func openRoot(path string) (string, *os.File, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, nil, fmt.Errorf("candidate root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve candidate root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == string(filepath.Separator) {
		return "", nil, nil, fmt.Errorf("candidate root must not be filesystem root")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return "", nil, nil, fmt.Errorf("candidate root component %q must be a real directory without symlinks: %w", component, openErr)
		}
		fd = next
	}
	directory := os.NewFile(uintptr(fd), absolute)
	if directory == nil {
		_ = unix.Close(fd)
		return "", nil, nil, fmt.Errorf("candidate root has invalid descriptor")
	}
	info, err := directory.Stat()
	if err != nil {
		_ = directory.Close()
		return "", nil, nil, err
	}
	if err := validateRootInfo(info); err != nil {
		_ = directory.Close()
		return "", nil, nil, err
	}
	return absolute, directory, info, nil
}

func validateRootInfo(info os.FileInfo) error {
	if info == nil || !info.IsDir() {
		return fmt.Errorf("candidate root must be a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink < 2 {
		return fmt.Errorf("candidate root must be owned by the current user and have valid directory links")
	}
	permissions := info.Mode().Perm()
	if permissions != 0o700 && permissions != 0o750 {
		return fmt.Errorf("candidate root mode must be exactly 0700 or 0750")
	}
	return nil
}

func validateRootScope(root string, protectedPaths []string) error {
	if root == string(filepath.Separator) {
		return fmt.Errorf("candidate root must not be filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil {
		resolvedHome, resolveErr := filepath.EvalSymlinks(home)
		if resolveErr == nil && root == filepath.Clean(resolvedHome) {
			return fmt.Errorf("candidate root must not be the user home directory")
		}
	}
	for _, protected := range protectedPaths {
		if strings.TrimSpace(protected) == "" {
			continue
		}
		absolute, err := filepath.Abs(protected)
		if err != nil {
			return fmt.Errorf("resolve protected candidate path %q: %w", protected, err)
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		absolute = filepath.Clean(absolute)
		if root == absolute || isAncestor(root, absolute) {
			return fmt.Errorf("candidate root %q is equal to or contains protected path %q", root, absolute)
		}
	}
	return nil
}

func isAncestor(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateMarkerAt(root *os.File) error {
	fd, err := unix.Openat(int(root.Fd()), MarkerName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("candidate root marker is required: %w", err)
	}
	marker := os.NewFile(uintptr(fd), MarkerName)
	if marker == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("candidate root marker has invalid descriptor")
	}
	defer marker.Close()
	info, err := marker.Stat()
	if err != nil {
		return fmt.Errorf("inspect candidate root marker: %w", err)
	}
	if err := validateMarkerInfo(info); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(marker, int64(len(markerContent)+1)))
	if err != nil {
		return fmt.Errorf("read candidate root marker: %w", err)
	}
	if string(data) != markerContent {
		return fmt.Errorf("candidate root marker content is invalid")
	}
	return nil
}

func validateMarkerInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("candidate root marker must be a regular 0600 file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return fmt.Errorf("candidate root marker must be owned by the current user and have one link")
	}
	return nil
}

func (r Root) Path() string { return r.path }

// DirFd returns a dup'd file descriptor of the root directory, verified against
// the stored metadata from NewRoot. The caller must close the returned file.
func (r Root) DirFd() (*os.File, error) {
	if r.path == "" {
		return nil, fmt.Errorf("candidate root is not initialized")
	}
	fd, err := unix.Open(r.path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), r.path)
	if dir == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("candidate root fd is invalid")
	}
	info, err := dir.Stat()
	if err != nil {
		dir.Close()
		return nil, err
	}
	if r.info == nil || !os.SameFile(r.info, info) {
		dir.Close()
		return nil, fmt.Errorf("candidate root changed after initialization")
	}
	return dir, nil
}

// RequireFile returns a canonical target and rejects root escapes, including
// escapes through an existing symlinked parent.
func (r Root) RequireFile(label, path string) (string, error) {
	if r.path == "" {
		return "", fmt.Errorf("candidate root is not initialized")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	absolute = filepath.Clean(absolute)
	relative, err := filepath.Rel(r.path, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %q must be a file below candidate root %q", label, path, r.path)
	}
	if err := validateRelativeTarget(r, filepath.ToSlash(relative)); err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	return absolute, nil
}

func validateRelativeTarget(root Root, relative string) error {
	directoryPath, directory, info, err := openRoot(root.path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if directoryPath != root.path || root.info == nil || !os.SameFile(root.info, info) {
		return fmt.Errorf("candidate root changed after initialization")
	}
	parts := strings.Split(relative, "/")
	fd, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, component := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			return nil
		}
		if openErr != nil {
			return fmt.Errorf("path component %q must be a real directory without symlinks: %w", component, openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	targetFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("target must not be a symlink: %w", err)
	}
	target := os.NewFile(uintptr(targetFD), relative)
	if target == nil {
		_ = unix.Close(targetFD)
		return fmt.Errorf("target has invalid descriptor")
	}
	defer target.Close()
	targetInfo, err := target.Stat()
	if err != nil {
		return err
	}
	if !targetInfo.Mode().IsRegular() && !targetInfo.IsDir() {
		return fmt.Errorf("target must be a regular file or directory")
	}
	return nil
}
