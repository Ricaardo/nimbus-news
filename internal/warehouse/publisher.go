package warehouse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrPublishBusy = errors.New("warehouse publisher is busy")

type CapacityChecker interface {
	AvailableBytes(path string) (uint64, error)
}

type RetentionPlanner interface {
	Plan(snapshots []SnapshotInfo, currentID string) ([]string, error)
}

type SnapshotInfo struct {
	ID        string
	CreatedAt time.Time
}

type KeepNewestRetention struct{ Keep int }

func (policy KeepNewestRetention) Plan(snapshots []SnapshotInfo, currentID string) ([]string, error) {
	if policy.Keep < 1 {
		return nil, fmt.Errorf("warehouse retention: keep must be positive")
	}
	ordered := append([]SnapshotInfo(nil), snapshots...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].ID > ordered[j].ID
		}
		return ordered[i].CreatedAt.After(ordered[j].CreatedAt)
	})
	protected := map[string]struct{}{currentID: {}}
	for index := 0; index < len(ordered) && index < policy.Keep; index++ {
		protected[ordered[index].ID] = struct{}{}
	}
	var remove []string
	for _, snapshot := range ordered {
		if _, ok := protected[snapshot.ID]; !ok {
			remove = append(remove, snapshot.ID)
		}
	}
	sort.Strings(remove)
	return remove, nil
}

type FilesystemCapacity struct{}

func (FilesystemCapacity) AvailableBytes(path string) (uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return stats.Bavail * uint64(stats.Bsize), nil
}

type PreflightResult struct {
	RequiredBytes  uint64
	AvailableBytes uint64
}

func Preflight(candidateRoot string, validated *Validated, capacity CapacityChecker) (PreflightResult, error) {
	if validated == nil {
		return PreflightResult{}, fmt.Errorf("warehouse preflight: validated manifest is required")
	}
	if capacity == nil {
		capacity = FilesystemCapacity{}
	}
	available, err := capacity.AvailableBytes(candidateRoot)
	if err != nil {
		return PreflightResult{}, fmt.Errorf("warehouse preflight: capacity: %w", err)
	}
	required := validated.TotalBytes + uint64(len(validated.ManifestData)) + 4096
	result := PreflightResult{RequiredBytes: required, AvailableBytes: available}
	if available < required {
		return result, fmt.Errorf("warehouse preflight: insufficient capacity: available=%d required=%d", available, required)
	}
	return result, nil
}

type PublishResult struct {
	SnapshotID string
	Snapshot   string
	Pointer    string
}

func Publish(ctx context.Context, manifestPath, candidateRoot string, capacity CapacityChecker) (PublishResult, error) {
	return publish(ctx, manifestPath, candidateRoot, capacity, nil)
}

func publish(ctx context.Context, manifestPath, candidateRoot string, capacity CapacityChecker, beforeCommit func() error) (result PublishResult, err error) {
	return publishWithHooks(ctx, manifestPath, candidateRoot, capacity, nil, beforeCommit)
}

func publishWithHooks(ctx context.Context, manifestPath, candidateRoot string, capacity CapacityChecker, afterValidation, beforeCommit func() error) (result PublishResult, err error) {
	if ctx == nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: context is required")
	}
	validated, err := ValidateStaging(manifestPath)
	if err != nil {
		return PublishResult{}, err
	}
	if afterValidation != nil {
		if err := afterValidation(); err != nil {
			return PublishResult{}, err
		}
	}
	candidateHandle, err := openDirectoryNoSymlink(candidateRoot)
	if err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: candidate root: %w", err)
	}
	candidateInfo, err := candidateHandle.Stat()
	if err != nil {
		_ = candidateHandle.Close()
		return PublishResult{}, fmt.Errorf("warehouse publish: candidate root: %w", err)
	}
	if err := validateCandidateDirectoryInfo(candidateInfo); err != nil {
		_ = candidateHandle.Close()
		return PublishResult{}, err
	}
	if err := candidateHandle.Close(); err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: close candidate root: %w", err)
	}
	if _, err := Preflight(candidateRoot, validated, capacity); err != nil {
		return PublishResult{}, err
	}

	lock, err := acquirePublishLock(candidateRoot)
	if err != nil {
		return PublishResult{}, err
	}
	defer func() { err = errors.Join(err, lock.release()) }()
	if err := ensureDirectoryAt(lock.root, "snapshots", 0o750); err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: create snapshots: %w", err)
	}
	snapshots, snapshotsInfo, err := openDirectoryAt(lock.root, "snapshots")
	if err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: open snapshots: %w", err)
	}
	defer func() { err = errors.Join(err, snapshots.Close()) }()
	if err := validateOwnedDirectoryInfo("snapshots", snapshotsInfo, 0o750); err != nil {
		return PublishResult{}, err
	}
	if err := lock.verifyRootPath(); err != nil {
		return PublishResult{}, err
	}

	select {
	case <-ctx.Done():
		return PublishResult{}, ctx.Err()
	default:
	}

	snapshotPath := filepath.Join(candidateRoot, "snapshots", validated.ManifestHash)
	pointerPath := filepath.Join(candidateRoot, "current.json")
	existingRoot, existingInfo, openErr := openDirectoryAt(snapshots, validated.ManifestHash)
	if openErr == nil {
		existing, validateErr := validateStagingAt(existingRoot, existingInfo, "manifest.json", snapshotPath+string(filepath.Separator)+"manifest.json")
		closeErr := existingRoot.Close()
		if closeErr != nil && validateErr == nil {
			validateErr = closeErr
		}
		if validateErr != nil {
			return PublishResult{}, fmt.Errorf("warehouse publish: existing snapshot is invalid: %w", validateErr)
		}
		if existing.ManifestHash != validated.ManifestHash || filepath.Base(snapshotPath) != existing.ManifestHash {
			return PublishResult{}, fmt.Errorf("warehouse publish: existing snapshot manifest hash %s does not match directory/request %s", existing.ManifestHash, validated.ManifestHash)
		}
		if err := lock.verifyRootPath(); err != nil {
			return PublishResult{}, err
		}
		if err := verifyDirectoryEntryAt(lock.root, "snapshots", snapshotsInfo, 0o750); err != nil {
			return PublishResult{}, err
		}
		if err := writePointerAt(lock.root, validated.ManifestHash); err != nil {
			return PublishResult{}, err
		}
		return PublishResult{SnapshotID: validated.ManifestHash, Snapshot: snapshotPath, Pointer: pointerPath}, nil
	} else if !errors.Is(openErr, unix.ENOENT) {
		return PublishResult{}, fmt.Errorf("warehouse publish: inspect snapshot: %w", openErr)
	}

	tempName, tempRoot, tempInfo, err := makeTempDirectoryAt(snapshots, ".publishing-"+validated.ManifestHash[:12]+"-", 0o700)
	if err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: create temporary snapshot: %w", err)
	}
	committed := false
	defer func() {
		closeErr := tempRoot.Close()
		if !committed {
			cleanupErr := removeTreeAt(snapshots, tempName)
			err = errors.Join(err, closeErr, cleanupErr)
		} else {
			err = errors.Join(err, closeErr)
		}
	}()
	sourceRoot, err := openDirectoryNoSymlink(filepath.Dir(validated.ManifestPath))
	if err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: reopen staging root: %w", err)
	}
	defer sourceRoot.Close()
	sourceRootInfo, err := sourceRoot.Stat()
	if err != nil || !os.SameFile(validated.rootInfo, sourceRootInfo) {
		return PublishResult{}, fmt.Errorf("warehouse publish: staging root changed after validation")
	}
	for _, entry := range validated.Manifest.Files {
		select {
		case <-ctx.Done():
			return PublishResult{}, ctx.Err()
		default:
		}
		sourceHandle, beforeInfo, err := openRegularAt(sourceRoot, entry.Path)
		if err != nil {
			return PublishResult{}, err
		}
		copyErr := copyRegularFileAt(ctx, sourceHandle, tempRoot, entry.Path)
		closeErr := sourceHandle.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return PublishResult{}, err
		}
		afterHandle, afterInfo, err := openRegularAt(sourceRoot, entry.Path)
		if err != nil {
			return PublishResult{}, fmt.Errorf("warehouse publish: source changed after copy: %w", err)
		}
		_ = afterHandle.Close()
		if !os.SameFile(beforeInfo, afterInfo) {
			return PublishResult{}, fmt.Errorf("warehouse publish: source inode changed while copying %s", entry.Path)
		}
	}
	if err := writeFileAtSync(tempRoot, "manifest.json", validated.ManifestData, 0o640); err != nil {
		return PublishResult{}, err
	}
	if err := syncTreeDirectoriesAt(tempRoot); err != nil {
		return PublishResult{}, err
	}
	if _, err := validateStagingAt(tempRoot, tempInfo, "manifest.json", tempName+"/manifest.json"); err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: copied snapshot validation: %w", err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return PublishResult{}, fmt.Errorf("warehouse publish: interrupted before commit: %w", err)
		}
	}
	if err := lock.verifyRootPath(); err != nil {
		return PublishResult{}, err
	}
	if err := verifyDirectoryEntryAt(lock.root, "snapshots", snapshotsInfo, 0o750); err != nil {
		return PublishResult{}, err
	}
	if err := unix.Renameat(int(snapshots.Fd()), tempName, int(snapshots.Fd()), validated.ManifestHash); err != nil {
		return PublishResult{}, fmt.Errorf("warehouse publish: commit snapshot: %w", err)
	}
	committed = true
	if err := unix.Fsync(int(snapshots.Fd())); err != nil {
		return PublishResult{}, err
	}
	if err := lock.verifyRootPath(); err != nil {
		return PublishResult{}, err
	}
	if err := verifyDirectoryEntryAt(lock.root, "snapshots", snapshotsInfo, 0o750); err != nil {
		return PublishResult{}, err
	}
	if err := writePointerAt(lock.root, validated.ManifestHash); err != nil {
		return PublishResult{}, err
	}
	return PublishResult{SnapshotID: validated.ManifestHash, Snapshot: snapshotPath, Pointer: pointerPath}, nil
}

func writePointerAt(root *os.File, snapshotID string) (err error) {
	pointer := struct {
		SchemaVersion   string `json:"schema_version"`
		SnapshotID      string `json:"snapshot_id"`
		Manifest        string `json:"manifest"`
		PublishedAt     string `json:"published_at"`
		ProductionOwner bool   `json:"production_owner"`
	}{
		SchemaVersion: "warehouse-current/v1", SnapshotID: snapshotID,
		Manifest:    filepath.ToSlash(filepath.Join("snapshots", snapshotID, "manifest.json")),
		PublishedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	tempName, temp, err := createTempFileAt(root, ".current-", ".json", 0o640)
	if err != nil {
		return fmt.Errorf("warehouse publish: create pointer: %w", err)
	}
	committed := false
	defer func() {
		closeErr := temp.Close()
		if !committed {
			cleanupErr := unix.Unlinkat(int(root.Fd()), tempName, 0)
			err = errors.Join(err, closeErr, cleanupErr)
		} else {
			err = errors.Join(err, closeErr)
		}
	}()
	if _, err := temp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat(int(root.Fd()), tempName, int(root.Fd()), "current.json"); err != nil {
		return fmt.Errorf("warehouse publish: commit pointer: %w", err)
	}
	committed = true
	if err := unix.Fsync(int(root.Fd())); err != nil {
		return fmt.Errorf("warehouse publish: sync candidate root: %w", err)
	}
	return nil
}

func copyRegularFileAt(ctx context.Context, source, root *os.File, destination string) error {
	out, err := createRegularFileAt(root, destination, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, contextReader{ctx: ctx, reader: source}); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeFileAtSync(root *os.File, name string, data []byte, mode os.FileMode) error {
	handle, err := createRegularFileAt(root, name, mode)
	if err != nil {
		return err
	}
	if _, err := handle.Write(data); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-reader.ctx.Done():
		return 0, reader.ctx.Err()
	default:
		return reader.reader.Read(buffer)
	}
}

type publishLock struct {
	file     *os.File
	root     *os.File
	parent   *os.File
	rootName string
	rootInfo os.FileInfo
}

func acquirePublishLock(candidateRoot string) (*publishLock, error) {
	root, parent, rootName, err := openDirectoryNoSymlinkWithParent(candidateRoot)
	if err != nil {
		return nil, fmt.Errorf("warehouse publish: open candidate root for lock: %w", err)
	}
	rootInfo, err := root.Stat()
	if err != nil {
		_ = root.Close()
		_ = parent.Close()
		return nil, err
	}
	if err := validateCandidateDirectoryInfo(rootInfo); err != nil {
		_ = root.Close()
		_ = parent.Close()
		return nil, err
	}
	fd, err := unix.Openat(int(root.Fd()), ".publish.lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		_ = root.Close()
		_ = parent.Close()
		return nil, fmt.Errorf("warehouse publish: open advisory lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), ".publish.lock")
	if file == nil {
		_ = unix.Close(fd)
		_ = root.Close()
		_ = parent.Close()
		return nil, fmt.Errorf("warehouse publish: invalid advisory lock descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		_ = root.Close()
		_ = parent.Close()
		return nil, fmt.Errorf("warehouse publish: inspect advisory lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || stat.Mode&0o777 != 0o600 {
		_ = file.Close()
		_ = root.Close()
		_ = parent.Close()
		return nil, fmt.Errorf("warehouse publish: advisory lock must be a current-user regular 0600 file with one link")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		_ = root.Close()
		_ = parent.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrPublishBusy
		}
		return nil, fmt.Errorf("warehouse publish: acquire advisory lock: %w", err)
	}
	return &publishLock{file: file, root: root, parent: parent, rootName: rootName, rootInfo: rootInfo}, nil
}

func (lock *publishLock) verifyRootPath() error {
	if lock == nil || lock.root == nil || lock.parent == nil || lock.rootInfo == nil {
		return fmt.Errorf("warehouse publish: candidate root lock is incomplete")
	}
	fd, err := unix.Openat(int(lock.parent.Fd()), lock.rootName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("warehouse publish: candidate root changed while locked: %w", err)
	}
	current := os.NewFile(uintptr(fd), lock.rootName)
	if current == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("warehouse publish: candidate root changed while locked")
	}
	info, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil {
		return fmt.Errorf("warehouse publish: inspect locked candidate root: %w", statErr)
	}
	if closeErr != nil {
		return fmt.Errorf("warehouse publish: close locked candidate root check: %w", closeErr)
	}
	if !os.SameFile(lock.rootInfo, info) {
		return fmt.Errorf("warehouse publish: candidate root changed while locked")
	}
	if err := validateCandidateDirectoryInfo(info); err != nil {
		return err
	}
	return nil
}

func (lock *publishLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlocked := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closed := lock.file.Close()
	rootClosed := lock.root.Close()
	parentClosed := lock.parent.Close()
	lock.file = nil
	lock.root = nil
	lock.parent = nil
	if err := errors.Join(unlocked, closed, rootClosed, parentClosed); err != nil {
		return fmt.Errorf("warehouse publish: release advisory lock: %w", err)
	}
	return nil
}

func validateCandidateDirectoryInfo(info os.FileInfo) error {
	if info == nil || !info.IsDir() {
		return fmt.Errorf("warehouse publish: candidate root must be a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink < 2 {
		return fmt.Errorf("warehouse publish: candidate root must be owned by the current user and have valid directory links")
	}
	permissions := info.Mode().Perm()
	if permissions != 0o700 && permissions != 0o750 {
		return fmt.Errorf("warehouse publish: candidate root mode must be exactly 0700 or 0750")
	}
	return nil
}

func validateOwnedDirectoryInfo(label string, info os.FileInfo, mode os.FileMode) error {
	if info == nil || !info.IsDir() {
		return fmt.Errorf("warehouse publish: %s must be a directory", label)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink < 2 || info.Mode().Perm() != mode.Perm() {
		return fmt.Errorf("warehouse publish: %s must be current-user owned with mode %04o and valid links", label, mode.Perm())
	}
	return nil
}

func verifyDirectoryEntryAt(parent *os.File, name string, expected os.FileInfo, mode os.FileMode) error {
	directory, info, err := openDirectoryAt(parent, name)
	if err != nil {
		return fmt.Errorf("warehouse publish: %s changed while locked: %w", name, err)
	}
	closeErr := directory.Close()
	if closeErr != nil {
		return closeErr
	}
	if expected == nil || !os.SameFile(expected, info) {
		return fmt.Errorf("warehouse publish: %s changed while locked", name)
	}
	return validateOwnedDirectoryInfo(name, info, mode)
}

func ensureDirectoryAt(root *os.File, name string, mode uint32) error {
	if root == nil || strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid directory name")
	}
	if err := unix.Mkdirat(int(root.Fd()), name, mode); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), name)
	if directory == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("invalid directory descriptor")
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	return validateOwnedDirectoryInfo("directory", info, os.FileMode(mode))
}

func makeTempDirectoryAt(root *os.File, prefix string, mode os.FileMode) (string, *os.File, os.FileInfo, error) {
	for attempt := 0; attempt < 128; attempt++ {
		name, err := randomName(prefix, "")
		if err != nil {
			return "", nil, nil, err
		}
		if err := unix.Mkdirat(int(root.Fd()), name, uint32(mode.Perm())); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return "", nil, nil, err
		}
		directory, info, err := openDirectoryAt(root, name)
		if err != nil {
			_ = unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, nil, err
		}
		if err := validateOwnedDirectoryInfo("temporary snapshot", info, mode); err != nil {
			_ = directory.Close()
			_ = unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, nil, err
		}
		return name, directory, info, nil
	}
	return "", nil, nil, fmt.Errorf("warehouse publish: exhausted temporary directory names")
}

func createTempFileAt(root *os.File, prefix, suffix string, mode os.FileMode) (string, *os.File, error) {
	for attempt := 0; attempt < 128; attempt++ {
		name, err := randomName(prefix, suffix)
		if err != nil {
			return "", nil, err
		}
		handle, err := createRegularFileAt(root, name, mode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return name, handle, nil
	}
	return "", nil, fmt.Errorf("warehouse publish: exhausted temporary file names")
}

func randomName(prefix, suffix string) (string, error) {
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(entropy[:]) + suffix, nil
}

func createRegularFileAt(root *os.File, relative string, mode os.FileMode) (*os.File, error) {
	parent, name, err := openParentForCreateAt(root, relative)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	handle := os.NewFile(uintptr(fd), relative)
	if handle == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid regular file descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = handle.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || os.FileMode(stat.Mode&0o777) != mode.Perm() {
		_ = handle.Close()
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return nil, fmt.Errorf("created file must be a current-user regular %04o file with one link", mode.Perm())
	}
	return handle, nil
}

func openParentForCreateAt(root *os.File, relative string) (*os.File, string, error) {
	if root == nil {
		return nil, "", fmt.Errorf("trusted root descriptor is required")
	}
	if err := validateRelativePath(relative); err != nil {
		return nil, "", err
	}
	parts := strings.Split(relative, "/")
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, "", err
	}
	for _, component := range parts[:len(parts)-1] {
		if mkdirErr := unix.Mkdirat(fd, component, 0o750); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			_ = unix.Close(fd)
			return nil, "", mkdirErr
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, "", fmt.Errorf("open destination directory %q: %w", component, openErr)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			_ = unix.Close(next)
			return nil, "", err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || stat.Nlink < 2 || stat.Mode&0o777 != 0o750 {
			_ = unix.Close(next)
			return nil, "", fmt.Errorf("destination directory %q must be current-user owned with mode 0750 and valid links", component)
		}
		fd = next
	}
	parent := os.NewFile(uintptr(fd), strings.Join(parts[:len(parts)-1], "/"))
	if parent == nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("invalid parent directory descriptor")
	}
	return parent, parts[len(parts)-1], nil
}

func syncTreeDirectoriesAt(root *os.File) error {
	entries, err := root.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(root.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, _, err := openDirectoryAt(root, entry.Name())
			if err != nil {
				return err
			}
			syncErr := syncTreeDirectoriesAt(child)
			closeErr := child.Close()
			if err := errors.Join(syncErr, closeErr); err != nil {
				return err
			}
		case unix.S_IFREG:
		default:
			return fmt.Errorf("warehouse publish: temporary snapshot contains non-regular entry %q", entry.Name())
		}
	}
	return unix.Fsync(int(root.Fd()))
}

func removeTreeAt(parent *os.File, name string) error {
	directory, _, err := openDirectoryAt(parent, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	if readErr == nil {
		for _, entry := range entries {
			var stat unix.Stat_t
			if statErr := unix.Fstatat(int(directory.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
				readErr = statErr
				break
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
				if removeErr := removeTreeAt(directory, entry.Name()); removeErr != nil {
					readErr = removeErr
					break
				}
			} else if unlinkErr := unix.Unlinkat(int(directory.Fd()), entry.Name(), 0); unlinkErr != nil {
				readErr = unlinkErr
				break
			}
		}
	}
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(int(parent.Fd()))
}
