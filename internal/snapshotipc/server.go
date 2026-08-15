package snapshotipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"

	"golang.org/x/sys/unix"
)

const lockRelative = "run/snapshot-v1.lock"

type ServerConfig struct {
	Root         candidate.Root
	ConfigSHA256 string
	Owners       []Owner
	Expected     []ExpectedObject
	Now          func() time.Time
}

type Server struct {
	root         candidate.Root
	rootBinding  string
	configSHA256 string
	owners       []Owner
	now          func() time.Time
	listener     *net.UnixListener
	lockFile     *os.File
	closeOnce    sync.Once
	closeErr     error
	connectionMu sync.Mutex
	connections  map[*net.UnixConn]struct{}
	connectionWG sync.WaitGroup
	closing      bool
}

func NewServer(config ServerConfig) (*Server, error) {
	if !validHash(config.ConfigSHA256) {
		return nil, fmt.Errorf("snapshot server: trusted config hash is required")
	}
	owners, err := validateOwners(config.Root, config.Owners, config.Expected)
	if err != nil {
		return nil, err
	}
	capability, err := config.Root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	if err := capability.MkdirAll("run", 0o700); err != nil {
		return nil, err
	}
	lockFile, err := capability.OpenLock(lockRelative)
	if err != nil {
		return nil, fmt.Errorf("snapshot server: open lifecycle lock: %w", err)
	}
	lockHeld := false
	defer func() {
		if !lockHeld {
			_ = lockFile.Close()
		}
	}()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, fmt.Errorf("snapshot server: lifecycle lock is held: %w", err)
	}
	lockHeld = true
	committedLock := false
	defer func() {
		if !committedLock {
			_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
			_ = lockFile.Close()
		}
	}()
	socketPath, err := capability.Absolute(SocketRelative)
	if err != nil {
		return nil, err
	}
	if err := removeStaleSocket(capability, socketPath); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("snapshot server: listen: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	committed := false
	defer func() {
		if !committed {
			_ = listener.Close()
		}
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return nil, err
	}
	if err := validateSocket(socketPath); err != nil {
		return nil, err
	}
	if err := capability.ValidateRoot(); err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	server := &Server{
		root: config.Root, rootBinding: capability.Binding(), configSHA256: config.ConfigSHA256,
		owners: owners, now: now, listener: listener, lockFile: lockFile,
		connections: make(map[*net.UnixConn]struct{}),
	}
	committedLock = true
	committed = true
	return server, nil
}

func (s *Server) Serve(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("snapshot server: context is required")
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	for {
		connection, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("snapshot server: accept: %w", err)
		}
		if !s.trackConnection(connection) {
			_ = connection.Close()
			return nil
		}
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_ = s.handle(requestCtx, connection)
		cancel()
		_ = connection.Close()
		s.untrackConnection(connection)
	}
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		listenerErr := s.listener.Close()
		s.connectionMu.Lock()
		s.closing = true
		for connection := range s.connections {
			_ = connection.SetDeadline(time.Now())
			_ = connection.Close()
		}
		s.connectionMu.Unlock()
		s.connectionWG.Wait()
		unlockErr := unix.Flock(int(s.lockFile.Fd()), unix.LOCK_UN)
		lockCloseErr := s.lockFile.Close()
		if errors.Is(listenerErr, net.ErrClosed) {
			listenerErr = nil
		}
		s.closeErr = errors.Join(listenerErr, unlockErr, lockCloseErr)
	})
	return s.closeErr
}

func (s *Server) trackConnection(connection *net.UnixConn) bool {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	if s.closing {
		return false
	}
	s.connections[connection] = struct{}{}
	s.connectionWG.Add(1)
	return true
}

func (s *Server) untrackConnection(connection *net.UnixConn) {
	s.connectionMu.Lock()
	delete(s.connections, connection)
	s.connectionMu.Unlock()
	s.connectionWG.Done()
}

func (s *Server) handle(ctx context.Context, connection *net.UnixConn) error {
	_ = connection.SetDeadline(time.Now().Add(5 * time.Minute))
	uid, err := peerUID(connection)
	if err != nil || uid != currentUID() {
		return fmt.Errorf("snapshot server: peer UID rejected")
	}
	kind, payload, err := readFrame(connection)
	if err != nil {
		return err
	}
	if kind != frameRequest {
		return s.writeError(connection, "INVALID_REQUEST", "snapshot request frame is required")
	}
	var request Request
	if err := decodeStrict(payload, &request); err != nil {
		return s.writeError(connection, "INVALID_REQUEST", "snapshot request is invalid")
	}
	if err := validateRequest(request); err != nil ||
		request.ConfigSHA256 != s.configSHA256 ||
		request.CandidateRootBinding != s.rootBinding {
		return s.writeError(connection, "BINDING_MISMATCH", "snapshot request binding mismatch")
	}
	snapshotSetID, err := NewID()
	if err != nil {
		return s.writeError(connection, "SNAPSHOT_SET_FAILED", "snapshot set identity unavailable")
	}
	setStarted := s.now().UTC()
	hashes := make([]string, 0, len(s.owners))
	for _, owner := range s.owners {
		begin := ObjectBegin{
			RequestID: request.RequestID, SnapshotSetID: snapshotSetID,
			ObjectName: owner.Name, Kind: owner.Kind,
		}
		if err := writeJSONFrame(connection, frameBegin, begin); err != nil {
			return err
		}
		started := s.now().UTC()
		writer := &frameWriter{ctx: ctx, destination: connection, hash: sha256.New()}
		result, err := owner.Snapshot(ctx, writer)
		if err != nil {
			return s.writeError(connection, "OWNER_SNAPSHOT_FAILED", "owner snapshot failed")
		}
		actualHash := hex.EncodeToString(writer.hash.Sum(nil))
		resultHash := normalizeHash(result.SHA256)
		if result.Kind != owner.ResultKind || result.Size != writer.size ||
			!validHash(resultHash) || resultHash != actualHash {
			return s.writeError(connection, "OWNER_RECEIPT_MISMATCH", "owner snapshot metadata mismatch")
		}
		receipt := OwnerReceipt{
			Schema: ReceiptSchema, RequestID: request.RequestID, SnapshotSetID: snapshotSetID,
			ObjectName: owner.Name, Kind: owner.Kind, OwnerKind: result.Kind,
			ReleaseID: request.ReleaseID, ConfigSHA256: request.ConfigSHA256,
			CandidateRootBinding: request.CandidateRootBinding, SourceSHA256: HashSourcePath(owner.SourcePath),
			SHA256: actualHash, Size: writer.size, OwnerUID: currentUID(), SchemaVersion: result.SchemaVersion,
			StartedAt: started.Format(time.RFC3339Nano), CompletedAt: s.now().UTC().Format(time.RFC3339Nano),
		}
		if err := signReceipt(&receipt); err != nil {
			return err
		}
		if err := writeJSONFrame(connection, frameReceipt, receipt); err != nil {
			return err
		}
		hashes = append(hashes, receipt.Hash)
	}
	complete := SetReceipt{
		Schema: SetSchema, RequestID: request.RequestID, SnapshotSetID: snapshotSetID,
		ReleaseID: request.ReleaseID, ConfigSHA256: request.ConfigSHA256,
		CandidateRootBinding: request.CandidateRootBinding, ReceiptHashes: hashes,
		OwnerUID: currentUID(), StartedAt: setStarted.Format(time.RFC3339Nano),
		CompletedAt: s.now().UTC().Format(time.RFC3339Nano),
	}
	if err := signSetReceipt(&complete); err != nil {
		return err
	}
	return writeJSONFrame(connection, frameComplete, complete)
}

func (s *Server) writeError(connection io.Writer, code, message string) error {
	return writeJSONFrame(connection, frameError, errorFrame{Code: code, Message: message})
}

func validateOwners(root candidate.Root, values []Owner, expectedValues []ExpectedObject) ([]Owner, error) {
	if len(values) != len(fixedOwners) {
		return nil, fmt.Errorf("snapshot server: exactly four owners are required")
	}
	expected, err := validateExpected(root, expectedValues)
	if err != nil {
		return nil, err
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	byName := make(map[string]Owner, len(values))
	for _, owner := range values {
		kind, ok := FixedKind(owner.Name)
		if !ok || kind != owner.Kind || owner.ResultKind == "" || owner.Snapshot == nil ||
			owner.SourcePath == "" || byName[owner.Name].Name != "" {
			return nil, fmt.Errorf("snapshot server: invalid fixed owner")
		}
		if _, err := capability.Relative(owner.SourcePath); err != nil {
			return nil, fmt.Errorf("snapshot server: owner source is outside candidate root")
		}
		byName[owner.Name] = owner
	}
	owners := make([]Owner, 0, len(fixedOwners))
	for index, fixed := range fixedOwners {
		owner := byName[fixed.Name]
		if filepath.Clean(owner.SourcePath) != filepath.Clean(expected[index].SourcePath) {
			return nil, fmt.Errorf("snapshot server: owner source does not match trusted config")
		}
		owners = append(owners, owner)
	}
	return owners, nil
}

func removeStaleSocket(capability *candidate.Capability, socketPath string) error {
	info, err := os.Lstat(socketPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateSocketInfo(info); err != nil {
		return err
	}
	connection, dialErr := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("snapshot server: live socket already exists")
	}
	if err := capability.Unlink(SocketRelative); err != nil {
		return fmt.Errorf("snapshot server: remove stale socket: %w", err)
	}
	return capability.ValidateRoot()
}

func validateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateSocketInfo(info)
}

func validateSocketInfo(info os.FileInfo) error {
	if info == nil {
		return fmt.Errorf("snapshot socket must be a current-user 0600 Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 ||
		int(stat.Uid) != currentUID() || stat.Nlink != 1 {
		return fmt.Errorf("snapshot socket must be a current-user 0600 Unix socket")
	}
	return nil
}

type frameWriter struct {
	ctx         context.Context
	destination io.Writer
	hash        hash.Hash
	size        int64
}

func (w *frameWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if w.size+int64(len(data)) > MaxObjectBytes {
		return 0, fmt.Errorf("snapshot object exceeds %d-byte limit", MaxObjectBytes)
	}
	written := 0
	for len(data) > 0 {
		chunk := data
		if len(chunk) > maxDataFrame {
			chunk = chunk[:maxDataFrame]
		}
		if err := writeFrame(w.destination, frameData, chunk); err != nil {
			return written, err
		}
		_, _ = w.hash.Write(chunk)
		w.size += int64(len(chunk))
		written += len(chunk)
		data = data[len(chunk):]
	}
	return written, nil
}
