package snapshotipc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
)

func TestSnapshotSetEndToEndUsesFixedOrderAndServerIdentity(t *testing.T) {
	root := snapshotTestRoot(t)
	configHash := strings.Repeat("c", 64)
	expected, owners, data := snapshotTestOwners(root)
	server := startSnapshotTestServer(t, root, configHash, expected, owners)

	var names []string
	var setIDs []string
	client := Client{Root: root, ConfigSHA256: configHash, Expected: expected}
	set, err := client.SnapshotSet(context.Background(), "release-1", func(begin ObjectBegin, producer ObjectProducer) error {
		var output bytes.Buffer
		receipt, err := producer(&output)
		if err != nil {
			return err
		}
		if !bytes.Equal(output.Bytes(), data[begin.ObjectName]) {
			return fmt.Errorf("unexpected %s content", begin.ObjectName)
		}
		names = append(names, begin.ObjectName)
		setIDs = append(setIDs, receipt.SnapshotSetID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"news", "control", "signals", "shadow_feed"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("owner order=%v want %v", names, wantNames)
	}
	if !validHash(set.SnapshotSetID) || len(set.ReceiptHashes) != 4 {
		t.Fatalf("invalid server set receipt: %+v", set)
	}
	for _, setID := range setIDs {
		if setID != set.SnapshotSetID {
			t.Fatalf("mixed snapshot set IDs: %v", setIDs)
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root.Path(), filepath.FromSlash(SocketRelative))); !os.IsNotExist(err) {
		t.Fatalf("socket remained after close: %v", err)
	}
}

func TestSnapshotSetOwnerFailureStopsSet(t *testing.T) {
	root := snapshotTestRoot(t)
	configHash := strings.Repeat("d", 64)
	expected, owners, _ := snapshotTestOwners(root)
	owners[2].Snapshot = func(_ context.Context, destination io.Writer) (OwnerResult, error) {
		_, _ = destination.Write([]byte("partial"))
		return OwnerResult{}, io.ErrUnexpectedEOF
	}
	server := startSnapshotTestServer(t, root, configHash, expected, owners)
	defer server.Close()

	client := Client{Root: root, ConfigSHA256: configHash, Expected: expected}
	_, err := client.SnapshotSet(context.Background(), "release-1", func(_ ObjectBegin, producer ObjectProducer) error {
		_, err := producer(io.Discard)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "OWNER_SNAPSHOT_FAILED") {
		t.Fatalf("owner failure did not abort set: %v", err)
	}
}

func TestSnapshotServerCloseInterruptsIdleAcceptedConnection(t *testing.T) {
	root := snapshotTestRoot(t)
	expected, owners, _ := snapshotTestOwners(root)
	server := startSnapshotTestServer(t, root, strings.Repeat("e", 64), expected, owners)
	socketPath := filepath.Join(root.Path(), filepath.FromSlash(SocketRelative))
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	time.Sleep(20 * time.Millisecond)

	closed := make(chan error, 1)
	go func() { closed <- server.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle accepted connection blocked server shutdown")
	}
}

func TestSnapshotClientRejectsMalformedOrMismatchedServer(t *testing.T) {
	t.Run("config binding", func(t *testing.T) {
		root := snapshotTestRoot(t)
		expected, owners, _ := snapshotTestOwners(root)
		server := startSnapshotTestServer(t, root, strings.Repeat("a", 64), expected, owners)
		defer server.Close()
		client := Client{Root: root, ConfigSHA256: strings.Repeat("b", 64), Expected: expected}
		if _, err := client.SnapshotSet(context.Background(), "release", discardSnapshotObject); err == nil ||
			!strings.Contains(err.Error(), "BINDING_MISMATCH") {
			t.Fatalf("wrong config binding accepted: %v", err)
		}
	})
	t.Run("canceled stalled server", func(t *testing.T) {
		root := snapshotTestRoot(t)
		expected, _, _ := snapshotTestOwners(root)
		release := make(chan struct{})
		startScriptedSnapshotSocket(t, root, func(connection *net.UnixConn) {
			_ = readScriptedRequest(connection)
			<-release
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		client := Client{Root: root, ConfigSHA256: strings.Repeat("a", 64), Expected: expected}
		_, err := client.SnapshotSet(ctx, "release", discardSnapshotObject)
		close(release)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
			t.Fatalf("stalled server cancellation: elapsed=%s err=%v", time.Since(started), err)
		}
	})

	for name, handler := range map[string]func(*net.UnixConn, []ExpectedObject){
		"reordered": func(connection *net.UnixConn, _ []ExpectedObject) {
			request := readScriptedRequest(connection)
			_ = writeJSONFrame(connection, frameBegin, ObjectBegin{
				RequestID: request.RequestID, SnapshotSetID: strings.Repeat("2", 64),
				ObjectName: "control", Kind: "sqlite",
			})
		},
		"truncated": func(connection *net.UnixConn, _ []ExpectedObject) {
			request := readScriptedRequest(connection)
			_ = writeJSONFrame(connection, frameBegin, ObjectBegin{
				RequestID: request.RequestID, SnapshotSetID: strings.Repeat("2", 64),
				ObjectName: "news", Kind: "bolt",
			})
			_ = writeFrame(connection, frameData, []byte("partial"))
		},
		"root receipt": func(connection *net.UnixConn, expected []ExpectedObject) {
			writeScriptedSet(connection, expected, true, false)
		},
		"completion receipt": func(connection *net.UnixConn, expected []ExpectedObject) {
			writeScriptedSet(connection, expected, false, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := snapshotTestRoot(t)
			expected, _, _ := snapshotTestOwners(root)
			startScriptedSnapshotSocket(t, root, func(connection *net.UnixConn) {
				handler(connection, expected)
			})
			client := Client{Root: root, ConfigSHA256: strings.Repeat("a", 64), Expected: expected}
			if _, err := client.SnapshotSet(context.Background(), "release", discardSnapshotObject); err == nil {
				t.Fatal("malformed scripted server accepted")
			}
		})
	}
}

func discardSnapshotObject(_ ObjectBegin, producer ObjectProducer) error {
	_, err := producer(io.Discard)
	return err
}

func TestSnapshotSocketRejectsLiveAndUnsafeEntriesAndReplacesStaleSocket(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		root := snapshotTestRoot(t)
		expected, owners, _ := snapshotTestOwners(root)
		first, err := NewServer(ServerConfig{
			Root: root, ConfigSHA256: strings.Repeat("a", 64), Expected: expected, Owners: owners,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		if _, err := NewServer(ServerConfig{
			Root: root, ConfigSHA256: strings.Repeat("a", 64), Expected: expected, Owners: owners,
		}); err == nil || !strings.Contains(err.Error(), "lifecycle lock") {
			t.Fatalf("second live server accepted: %v", err)
		}
	})

	t.Run("regular file", func(t *testing.T) {
		root := snapshotTestRoot(t)
		expected, owners, _ := snapshotTestOwners(root)
		run := filepath.Join(root.Path(), "run")
		if err := os.Mkdir(run, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(run, "snapshot-v1.sock"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewServer(ServerConfig{
			Root: root, ConfigSHA256: strings.Repeat("a", 64), Expected: expected, Owners: owners,
		}); err == nil {
			t.Fatal("regular file replaced as stale socket")
		}
	})

	t.Run("stale", func(t *testing.T) {
		root := snapshotTestRoot(t)
		expected, owners, _ := snapshotTestOwners(root)
		run := filepath.Join(root.Path(), "run")
		if err := os.Mkdir(run, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(run, "snapshot-v1.sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		server, err := NewServer(ServerConfig{
			Root: root, ConfigSHA256: strings.Repeat("a", 64), Expected: expected, Owners: owners,
		})
		if err != nil {
			t.Fatalf("stale socket was not replaced: %v", err)
		}
		defer server.Close()
	})
}

func startScriptedSnapshotSocket(t *testing.T, root candidate.Root, handler func(*net.UnixConn)) {
	t.Helper()
	capability, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	if err := capability.MkdirAll("run", 0o700); err != nil {
		capability.Close()
		t.Fatal(err)
	}
	path, err := capability.Absolute(SocketRelative)
	capability.Close()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.AcceptUnix()
		if err == nil {
			handler(connection)
			_ = connection.Close()
		}
		_ = listener.Close()
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
}

func readScriptedRequest(connection *net.UnixConn) Request {
	kind, payload, _ := readFrame(connection)
	if kind != frameRequest {
		return Request{}
	}
	var request Request
	_ = decodeStrict(payload, &request)
	return request
}

func writeScriptedSet(connection *net.UnixConn, expected []ExpectedObject, wrongRoot, tamperCompletion bool) {
	request := readScriptedRequest(connection)
	setID := strings.Repeat("2", 64)
	now := time.Now().UTC()
	hashes := make([]string, 0, len(expected))
	for _, object := range expected {
		_ = writeJSONFrame(connection, frameBegin, ObjectBegin{
			RequestID: request.RequestID, SnapshotSetID: setID,
			ObjectName: object.Name, Kind: object.Kind,
		})
		content := []byte(object.Name)
		_ = writeFrame(connection, frameData, content)
		sum := sha256.Sum256(content)
		metadata := fixedOwnerMetadata[object.Name]
		receipt := OwnerReceipt{
			Schema: ReceiptSchema, RequestID: request.RequestID, SnapshotSetID: setID,
			ObjectName: object.Name, Kind: object.Kind, OwnerKind: metadata.kind,
			ReleaseID: request.ReleaseID, ConfigSHA256: request.ConfigSHA256,
			CandidateRootBinding: request.CandidateRootBinding,
			SourceSHA256:         HashSourcePath(object.SourcePath), SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(content)), OwnerUID: currentUID(), SchemaVersion: metadata.schemaVersion,
			StartedAt: now.Format(time.RFC3339Nano), CompletedAt: now.Add(time.Millisecond).Format(time.RFC3339Nano),
		}
		if wrongRoot && object.Name == "news" {
			receipt.CandidateRootBinding = strings.Repeat("f", 64)
		}
		_ = signReceipt(&receipt)
		_ = writeJSONFrame(connection, frameReceipt, receipt)
		hashes = append(hashes, receipt.Hash)
	}
	complete := SetReceipt{
		Schema: SetSchema, RequestID: request.RequestID, SnapshotSetID: setID,
		ReleaseID: request.ReleaseID, ConfigSHA256: request.ConfigSHA256,
		CandidateRootBinding: request.CandidateRootBinding, ReceiptHashes: hashes,
		OwnerUID: currentUID(), StartedAt: now.Format(time.RFC3339Nano),
		CompletedAt: now.Add(2 * time.Millisecond).Format(time.RFC3339Nano),
	}
	_ = signSetReceipt(&complete)
	if tamperCompletion {
		complete.ReceiptHashes[0] = strings.Repeat("f", 64)
	}
	_ = writeJSONFrame(connection, frameComplete, complete)
}

func TestProtocolIsStrictBoundedAndHandlesShortWrites(t *testing.T) {
	request := []byte(`{"schema":"nimbus-owner-snapshot-request/v1","request_id":"` +
		strings.Repeat("1", 64) + `","operation":"snapshot_set","release_id":"r","config_sha256":"` +
		strings.Repeat("2", 64) + `","candidate_root_binding":"` + strings.Repeat("3", 64) +
		`","objects":["news"]}`)
	var decoded Request
	if err := decodeStrict(request, &decoded); err == nil {
		t.Fatal("request-controlled object list accepted")
	}
	if err := writeFrame(io.Discard, frameData, make([]byte, maxDataFrame+1)); err == nil {
		t.Fatal("oversized data frame accepted")
	}
	writer := &frameWriter{ctx: context.Background(), destination: io.Discard, hash: sha256.New(), size: MaxObjectBytes}
	if _, err := writer.Write([]byte{1}); err == nil {
		t.Fatal("oversized object accepted")
	}
	if err := writeFrame(shortWriter{}, frameRequest, []byte("payload")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write was not detected: %v", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return 1, nil
}

func snapshotTestRoot(t *testing.T) candidate.Root {
	t.Helper()
	temporary, err := os.MkdirTemp(realTempBase(t), "nimbus-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	path, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := candidate.InitRoot(path); err != nil {
		t.Fatal(err)
	}
	root, err := candidate.NewRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func realTempBase(t *testing.T) string {
	t.Helper()
	var best string
	for _, value := range []string{os.TempDir(), "/tmp"} {
		absolute, err := filepath.Abs(value)
		if err != nil {
			continue
		}
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			continue
		}
		if best == "" || len(canonical) < len(best) {
			best = canonical
		}
	}
	if best == "" {
		t.Fatal("no real temporary base directory")
	}
	return best
}

func snapshotTestOwners(root candidate.Root) ([]ExpectedObject, []Owner, map[string][]byte) {
	expected := []ExpectedObject{
		{Name: "news", Kind: "bolt", SourcePath: filepath.Join(root.Path(), "news.db")},
		{Name: "control", Kind: "sqlite", SourcePath: filepath.Join(root.Path(), "control.db")},
		{Name: "signals", Kind: "jsonl", SourcePath: filepath.Join(root.Path(), "signals.jsonl")},
		{Name: "shadow_feed", Kind: "rolling_jsonl", SourcePath: filepath.Join(root.Path(), "shadow.jsonl")},
	}
	data := map[string][]byte{
		"news":        []byte("bolt snapshot"),
		"control":     []byte("sqlite snapshot"),
		"signals":     []byte("{}\n"),
		"shadow_feed": []byte("{\"shadow\":true}\n"),
	}
	resultKinds := map[string]string{
		"news": "bolt", "control": "sqlite",
		"signals": "signals_jsonl", "shadow_feed": "news_feed_jsonl_v1",
	}
	owners := make([]Owner, 0, len(expected))
	for _, object := range expected {
		object := object
		owners = append(owners, Owner{
			Name: object.Name, Kind: object.Kind, ResultKind: resultKinds[object.Name],
			SourcePath: object.SourcePath,
			Snapshot: func(_ context.Context, destination io.Writer) (OwnerResult, error) {
				content := data[object.Name]
				if _, err := destination.Write(content); err != nil {
					return OwnerResult{}, err
				}
				sum := sha256.Sum256(content)
				hash := hex.EncodeToString(sum[:])
				if object.Name == "control" {
					hash = "sha256:" + hash
				}
				result := OwnerResult{Kind: resultKinds[object.Name], SHA256: hash, Size: int64(len(content))}
				if object.Name == "control" {
					result.SchemaVersion = ControlSchemaVersion
				}
				return result, nil
			},
		})
	}
	return expected, owners, data
}

func startSnapshotTestServer(t *testing.T, root candidate.Root, configHash string, expected []ExpectedObject, owners []Owner) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Root: root, ConfigSHA256: configHash, Expected: expected, Owners: owners, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("snapshot server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("snapshot server did not stop")
		}
	})
	return server
}
