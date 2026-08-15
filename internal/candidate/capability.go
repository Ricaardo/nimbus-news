package candidate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Capability pins the candidate root inode for a complete operation. Callers
// must close it and must use relative paths for every filesystem action.
type Capability struct {
	root Root
	dir  *os.File
}

type Entry struct {
	Name string
	Mode os.FileMode
	Size int64
}

type CASStreamResult struct {
	Path     string
	Relative string
	SHA256   string
	Size     int64
}

func (r Root) OpenCapability() (*Capability, error) {
	path, directory, info, err := openRoot(r.path)
	if err != nil {
		return nil, err
	}
	if path != r.path || r.info == nil || !os.SameFile(r.info, info) {
		_ = directory.Close()
		return nil, fmt.Errorf("candidate root changed after initialization")
	}
	if err := validateMarkerAt(directory); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return &Capability{root: r, dir: directory}, nil
}

func (c *Capability) Close() error {
	if c == nil || c.dir == nil {
		return nil
	}
	err := c.dir.Close()
	c.dir = nil
	return err
}
func (c *Capability) RootPath() string { return c.root.path }

// ValidateRoot verifies that the pathname still names the inode pinned by this
// capability. Filesystem operations continue to use the pinned descriptor, but
// fail closed when the externally visible root has been replaced.
func (c *Capability) ValidateRoot() error {
	if c == nil || c.dir == nil {
		return fmt.Errorf("candidate capability is closed")
	}
	path, directory, info, err := openRoot(c.root.path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if path != c.root.path || c.root.info == nil || !os.SameFile(c.root.info, info) {
		return fmt.Errorf("candidate root changed after initialization")
	}
	return validateMarkerAt(directory)
}

func (c *Capability) Binding() string {
	stat, _ := c.root.info.Sys().(*syscall.Stat_t)
	value := c.root.path
	if stat != nil {
		value = fmt.Sprintf("%s:%d:%d:%d", value, stat.Dev, stat.Ino, stat.Uid)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (c *Capability) Relative(path string) (string, error) {
	if c == nil || c.dir == nil {
		return "", fmt.Errorf("candidate capability is closed")
	}
	if err := c.ValidateRoot(); err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("candidate relative path is required")
	}
	var relative string
	if filepath.IsAbs(path) {
		value, err := filepath.Rel(c.root.path, filepath.Clean(path))
		if err != nil {
			return "", err
		}
		relative = value
	} else {
		relative = filepath.Clean(path)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must remain below candidate root")
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid candidate path component")
		}
	}
	return strings.Join(parts, "/"), nil
}

func (c *Capability) Absolute(relative string) (string, error) {
	relative, err := c.Relative(relative)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.root.path, filepath.FromSlash(relative)), nil
}

func (c *Capability) MkdirAll(relative string, mode os.FileMode) error {
	relative, err := c.Relative(relative)
	if err != nil {
		return err
	}
	fd, err := unix.Dup(int(c.dir.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, component := range strings.Split(relative, "/") {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if err := unix.Mkdirat(fd, component, uint32(mode.Perm())); err != nil && !errors.Is(err, unix.EEXIST) {
				return err
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return fmt.Errorf("candidate directory %q: %w", component, openErr)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			unix.Close(next)
			return err
		}
		if int(stat.Uid) != os.Geteuid() || stat.Nlink < 2 || os.FileMode(stat.Mode&0o777) != mode.Perm() {
			unix.Close(next)
			return fmt.Errorf("candidate directory ownership, links, or mode mismatch")
		}
		unix.Close(fd)
		fd = next
	}
	return nil
}

func (c *Capability) openParent(relative string, create bool) (int, string, error) {
	relative, err := c.Relative(relative)
	if err != nil {
		return -1, "", err
	}
	parts := strings.Split(relative, "/")
	fd, err := unix.Dup(int(c.dir.Fd()))
	if err != nil {
		return -1, "", err
	}
	for _, component := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if create && errors.Is(openErr, unix.ENOENT) {
			if err := unix.Mkdirat(fd, component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return -1, "", err
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			unix.Close(fd)
			return -1, "", openErr
		}
		if err := validateDirectoryFD(next); err != nil {
			unix.Close(next)
			unix.Close(fd)
			return -1, "", err
		}
		unix.Close(fd)
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func validateDirectoryFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	mode := os.FileMode(stat.Mode & 0o777)
	if int(stat.Uid) != os.Geteuid() || stat.Nlink < 2 || (mode != 0o700 && mode != 0o750) {
		return fmt.Errorf("candidate directory must be current-user owned with mode 0700 or 0750")
	}
	return nil
}

func validateOwnedRegular(file *os.File, mode os.FileMode) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("candidate file must be current-user regular file with one link")
	}
	if mode != 0 && info.Mode().Perm() != mode.Perm() {
		return nil, fmt.Errorf("candidate file mode must be %04o", mode.Perm())
	}
	return info, nil
}

func (c *Capability) OpenRead(relative string, strictMode os.FileMode) (*os.File, os.FileInfo, error) {
	parent, name, err := c.openParent(relative, false)
	if err != nil {
		return nil, nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("invalid candidate descriptor")
	}
	info, err := validateOwnedRegular(file, strictMode)
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func (c *Capability) ReadFile(relative string, max int64, strictMode os.FileMode) ([]byte, error) {
	file, before, err := c.OpenRead(relative, strictMode)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if max > 0 && before.Size() > max {
		return nil, fmt.Errorf("candidate file exceeds maximum size")
	}
	reader := io.Reader(file)
	if max > 0 {
		reader = io.LimitReader(file, max+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(data)) > max {
		return nil, fmt.Errorf("candidate file exceeds maximum size")
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("candidate file changed while reading")
	}
	if err := c.ValidateRoot(); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Capability) CreateExclusive(relative string, mode os.FileMode) (*os.File, error) {
	parent, name, err := c.openParent(relative, true)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("invalid candidate descriptor")
	}
	if _, err := validateOwnedRegular(file, mode); err != nil {
		file.Close()
		_ = unix.Unlinkat(parent, name, 0)
		return nil, err
	}
	return file, nil
}

func (c *Capability) OpenLock(relative string) (*os.File, error) {
	parent, name, err := c.openParent(relative, true)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, name, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("invalid lock descriptor")
	}
	if _, err := validateOwnedRegular(file, 0o600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (c *Capability) Unlink(relative string) error {
	parent, name, err := c.openParent(relative, false)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return unix.Unlinkat(parent, name, 0)
}

func (c *Capability) SyncDir(relative string) error {
	relative, err := c.Relative(relative)
	if err != nil {
		return err
	}
	fd, err := unix.Dup(int(c.dir.Fd()))
	if err != nil {
		return err
	}
	if relative != "." {
		for _, part := range strings.Split(relative, "/") {
			next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			unix.Close(fd)
			if e != nil {
				return e
			}
			if err := validateDirectoryFD(next); err != nil {
				unix.Close(next)
				return err
			}
			fd = next
		}
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}

func (c *Capability) WriteCAS(directory, suffix string, content []byte) (string, error) {
	directory, err := c.Relative(directory)
	if err != nil {
		return "", err
	}
	if err := c.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	relative := directory + "/" + hex.EncodeToString(sum[:]) + suffix
	file, err := c.CreateExclusive(relative, 0o600)
	if errors.Is(err, unix.EEXIST) {
		existing, readErr := c.ReadFile(relative, int64(len(content)), 0o600)
		if readErr == nil && string(existing) == string(content) {
			return c.Absolute(relative)
		}
		return "", fmt.Errorf("content address collision")
	}
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = c.Unlink(relative)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := c.SyncDir(directory); err != nil {
		return "", err
	}
	if err := c.ValidateRoot(); err != nil {
		return "", err
	}
	ok = true
	return c.Absolute(relative)
}

// WriteCASStream writes producer output to an exclusive staging file and only
// publishes the content-addressed object after the producer, fsync, and local
// hash complete successfully.
func (c *Capability) WriteCASStream(ctx context.Context, directory, suffix string, maxBytes int64, producer func(io.Writer) error) (CASStreamResult, error) {
	if ctx == nil || producer == nil || maxBytes <= 0 || suffix == "" ||
		strings.ContainsAny(suffix, `/\`) {
		return CASStreamResult{}, fmt.Errorf("candidate stream CAS: invalid options")
	}
	directory, err := c.Relative(directory)
	if err != nil {
		return CASStreamResult{}, err
	}
	staging := directory + "/staging"
	if err := c.MkdirAll(staging, 0o700); err != nil {
		return CASStreamResult{}, err
	}
	lock, err := c.OpenLock(directory + "/.stream-cas.lock")
	if err != nil {
		return CASStreamResult{}, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return CASStreamResult{}, err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	if err := c.cleanupStaleCAS(staging, time.Now().Add(-time.Minute)); err != nil {
		return CASStreamResult{}, err
	}
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return CASStreamResult{}, err
	}
	temporary := staging + "/" + hex.EncodeToString(random[:]) + ".part"
	file, err := c.CreateExclusive(temporary, 0o600)
	if err != nil {
		return CASStreamResult{}, err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = c.Unlink(temporary)
		}
	}()
	writer := &boundedHashWriter{
		ctx: ctx, destination: file, hash: sha256.New(), maximum: maxBytes,
	}
	if err := producer(writer); err != nil {
		return CASStreamResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CASStreamResult{}, err
	}
	if err := file.Sync(); err != nil {
		return CASStreamResult{}, err
	}
	if err := file.Close(); err != nil {
		return CASStreamResult{}, err
	}
	sum := hex.EncodeToString(writer.hash.Sum(nil))
	target := directory + "/" + sum + suffix
	sourceParent, sourceName, err := c.openParent(temporary, false)
	if err != nil {
		return CASStreamResult{}, err
	}
	defer unix.Close(sourceParent)
	targetParent, targetName, err := c.openParent(target, true)
	if err != nil {
		return CASStreamResult{}, err
	}
	defer unix.Close(targetParent)
	linkErr := unix.Linkat(sourceParent, sourceName, targetParent, targetName, 0)
	createdTarget := linkErr == nil
	if errors.Is(linkErr, unix.EEXIST) {
		if err := verifyExistingCAS(c, target, writer.size, sum); err != nil {
			return CASStreamResult{}, err
		}
	} else if linkErr != nil {
		return CASStreamResult{}, linkErr
	}
	if err := unix.Unlinkat(sourceParent, sourceName, 0); err != nil {
		if createdTarget {
			_ = unix.Unlinkat(targetParent, targetName, 0)
		}
		return CASStreamResult{}, err
	}
	if err := c.SyncDir(directory); err != nil {
		return CASStreamResult{}, rollbackStreamTarget(c, targetParent, targetName, directory, createdTarget, err)
	}
	if err := c.SyncDir(staging); err != nil {
		return CASStreamResult{}, rollbackStreamTarget(c, targetParent, targetName, directory, createdTarget, err)
	}
	if err := c.ValidateRoot(); err != nil {
		return CASStreamResult{}, rollbackStreamTarget(c, targetParent, targetName, directory, createdTarget, err)
	}
	absolute, err := c.Absolute(target)
	if err != nil {
		return CASStreamResult{}, err
	}
	committed = true
	return CASStreamResult{Path: absolute, Relative: target, SHA256: sum, Size: writer.size}, nil
}

func (c *Capability) cleanupStaleCAS(staging string, cutoff time.Time) error {
	entries, err := c.List(staging)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name, ".part") {
			return fmt.Errorf("candidate stream CAS: unexpected staging entry")
		}
		relative := staging + "/" + entry.Name
		file, info, err := c.OpenRead(relative, 0o600)
		if err != nil {
			return fmt.Errorf("candidate stream CAS: unsafe staging entry: %w", err)
		}
		closeErr := file.Close()
		if closeErr != nil {
			return closeErr
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := c.Unlink(relative); err != nil {
			return err
		}
	}
	return c.SyncDir(staging)
}

func rollbackStreamTarget(c *Capability, targetParent int, targetName, directory string, created bool, cause error) error {
	if !created {
		return cause
	}
	unlinkErr := unix.Unlinkat(targetParent, targetName, 0)
	syncErr := c.SyncDir(directory)
	return errors.Join(cause, unlinkErr, syncErr)
}

type boundedHashWriter struct {
	ctx         context.Context
	destination io.Writer
	hash        hash.Hash
	size        int64
	maximum     int64
}

func (w *boundedHashWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if w.size+int64(len(data)) > w.maximum {
		return 0, fmt.Errorf("candidate stream CAS exceeds maximum size")
	}
	n, err := w.destination.Write(data)
	if n > 0 {
		_, _ = w.hash.Write(data[:n])
		w.size += int64(n)
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

func verifyExistingCAS(c *Capability, relative string, size int64, wantHash string) error {
	file, info, err := c.OpenRead(relative, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() != size {
		return fmt.Errorf("content address collision")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != wantHash {
		return fmt.Errorf("content address collision")
	}
	return nil
}

func (c *Capability) List(relative string) ([]Entry, error) {
	relative, err := c.Relative(relative)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Dup(int(c.dir.Fd()))
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(relative, "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		if err := validateDirectoryFD(next); err != nil {
			unix.Close(next)
			return nil, err
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), relative)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("invalid directory descriptor")
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(file.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
		out = append(out, Entry{Name: entry.Name(), Mode: unixModeToGo(uint32(stat.Mode)), Size: stat.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// unixModeToGo converts a raw Unix st_mode value to Go's os.FileMode schema.
func unixModeToGo(raw uint32) os.FileMode {
	m := os.FileMode(raw & 0o777)
	switch raw & unix.S_IFMT {
	case unix.S_IFDIR:
		m |= os.ModeDir
	case unix.S_IFLNK:
		m |= os.ModeSymlink
	case unix.S_IFSOCK:
		m |= os.ModeSocket
	case unix.S_IFIFO:
		m |= os.ModeNamedPipe
	case unix.S_IFBLK:
		m |= os.ModeDevice
	case unix.S_IFCHR:
		m |= os.ModeCharDevice | os.ModeDevice
	}
	if raw&unix.S_ISUID != 0 {
		m |= os.ModeSetuid
	}
	if raw&unix.S_ISGID != 0 {
		m |= os.ModeSetgid
	}
	if raw&unix.S_ISVTX != 0 {
		m |= os.ModeSticky
	}
	return m
}

func (c *Capability) RandomDir(parent, prefix string) (string, string, error) {
	parent, err := c.Relative(parent)
	if err != nil {
		return "", "", err
	}
	if err := c.MkdirAll(parent, 0o700); err != nil {
		return "", "", err
	}
	for i := 0; i < 16; i++ {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			return "", "", err
		}
		relative := parent + "/" + prefix + hex.EncodeToString(value[:])
		fd, name, err := c.openParent(relative, false)
		if err != nil {
			return "", "", err
		}
		mkdirErr := unix.Mkdirat(fd, name, 0o700)
		unix.Close(fd)
		if errors.Is(mkdirErr, unix.EEXIST) {
			continue
		}
		if mkdirErr != nil {
			return "", "", mkdirErr
		}
		absolute, _ := c.Absolute(relative)
		return relative, absolute, nil
	}
	return "", "", fmt.Errorf("unable to allocate candidate temporary directory")
}

func (c *Capability) MkdirExclusive(relative string, mode os.FileMode) error {
	parent, name, err := c.openParent(relative, true)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	if err := unix.Mkdirat(parent, name, uint32(mode.Perm())); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
		}
	}()
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if int(stat.Uid) != os.Geteuid() || os.FileMode(stat.Mode&0o777) != mode.Perm() {
		return fmt.Errorf("candidate directory ownership or mode mismatch")
	}
	if err := c.ValidateRoot(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (c *Capability) RemoveTree(relative string) error {
	parent, name, err := c.openParent(relative, false)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return removeTreeAt(parent, name)
}
func removeTreeAt(parent int, name string) error {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return unix.Unlinkat(parent, name, 0)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return fmt.Errorf("invalid directory descriptor")
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		file.Close()
		return err
	}
	for _, entry := range entries {
		if err := removeTreeAt(fd, entry.Name()); err != nil {
			file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}
