package snapshotipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
)

type Client struct {
	Root         candidate.Root
	ConfigSHA256 string
	Expected     []ExpectedObject
}

type ObjectProducer func(io.Writer) (OwnerReceipt, error)
type ObjectConsumer func(ObjectBegin, ObjectProducer) error

func (c Client) SnapshotSet(ctx context.Context, releaseID string, consume ObjectConsumer) (SetReceipt, error) {
	if ctx == nil || releaseID == "" || consume == nil || !validHash(c.ConfigSHA256) {
		return SetReceipt{}, fmt.Errorf("snapshot client: invalid snapshot set call")
	}
	expected, err := validateExpected(c.Root, c.Expected)
	if err != nil {
		return SetReceipt{}, err
	}
	capability, err := c.Root.OpenCapability()
	if err != nil {
		return SetReceipt{}, err
	}
	socketPath, err := capability.Absolute(SocketRelative)
	if err != nil {
		capability.Close()
		return SetReceipt{}, err
	}
	rootBinding := capability.Binding()
	capability.Close()
	if err := validateSocket(socketPath); err != nil {
		return SetReceipt{}, err
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return SetReceipt{}, fmt.Errorf("snapshot client: dial: %w", err)
	}
	connection, ok := raw.(*net.UnixConn)
	if !ok {
		raw.Close()
		return SetReceipt{}, fmt.Errorf("snapshot client: Unix connection required")
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Minute))
	cancelWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-cancelWatch:
		}
	}()
	defer close(cancelWatch)
	uid, err := peerUID(connection)
	if err != nil || uid != currentUID() {
		return SetReceipt{}, fmt.Errorf("snapshot client: peer UID rejected")
	}
	requestID, err := NewID()
	if err != nil {
		return SetReceipt{}, err
	}
	request := Request{
		Schema: RequestSchema, RequestID: requestID, Operation: "snapshot_set",
		ReleaseID: releaseID, ConfigSHA256: c.ConfigSHA256,
		CandidateRootBinding: rootBinding,
	}
	if err := writeJSONFrame(connection, frameRequest, request); err != nil {
		return SetReceipt{}, err
	}

	var snapshotSetID string
	receiptHashes := make([]string, 0, len(expected))
	for _, object := range expected {
		frameKind, payload, err := readResponseFrame(connection)
		if err != nil {
			if ctx.Err() != nil {
				return SetReceipt{}, ctx.Err()
			}
			return SetReceipt{}, err
		}
		if frameKind != frameBegin {
			return SetReceipt{}, fmt.Errorf("snapshot client: object begin frame required")
		}
		var begin ObjectBegin
		if err := decodeStrict(payload, &begin); err != nil {
			return SetReceipt{}, err
		}
		if snapshotSetID == "" {
			snapshotSetID = begin.SnapshotSetID
		}
		if begin.RequestID != requestID || !validHash(begin.SnapshotSetID) ||
			begin.SnapshotSetID != snapshotSetID || begin.ObjectName != object.Name ||
			begin.Kind != object.Kind {
			return SetReceipt{}, fmt.Errorf("snapshot client: fixed object order mismatch")
		}
		called := false
		var receipt OwnerReceipt
		producer := func(destination io.Writer) (OwnerReceipt, error) {
			if called || destination == nil {
				return OwnerReceipt{}, fmt.Errorf("snapshot client: object producer must be called exactly once")
			}
			called = true
			value, err := c.receiveObject(ctx, connection, request, begin, object, destination)
			receipt = value
			return value, err
		}
		if err := consume(begin, producer); err != nil {
			return SetReceipt{}, err
		}
		if !called {
			return SetReceipt{}, fmt.Errorf("snapshot client: object producer was not consumed")
		}
		receiptHashes = append(receiptHashes, receipt.Hash)
	}
	frameKind, payload, err := readResponseFrame(connection)
	if err != nil {
		if ctx.Err() != nil {
			return SetReceipt{}, ctx.Err()
		}
		return SetReceipt{}, err
	}
	if frameKind != frameComplete {
		return SetReceipt{}, fmt.Errorf("snapshot client: set completion frame required")
	}
	var complete SetReceipt
	if err := decodeStrict(payload, &complete); err != nil {
		return SetReceipt{}, err
	}
	if err := VerifySetReceipt(complete); err != nil {
		return SetReceipt{}, err
	}
	if complete.RequestID != requestID || complete.SnapshotSetID != snapshotSetID ||
		complete.ReleaseID != releaseID || complete.ConfigSHA256 != c.ConfigSHA256 ||
		complete.CandidateRootBinding != rootBinding || complete.OwnerUID != currentUID() ||
		len(complete.ReceiptHashes) != len(receiptHashes) {
		return SetReceipt{}, fmt.Errorf("snapshot client: set receipt binding mismatch")
	}
	for index := range receiptHashes {
		if complete.ReceiptHashes[index] != receiptHashes[index] {
			return SetReceipt{}, fmt.Errorf("snapshot client: set owner receipt mismatch")
		}
	}
	return complete, nil
}

func (c Client) receiveObject(ctx context.Context, connection *net.UnixConn, request Request, begin ObjectBegin, expected ExpectedObject, destination io.Writer) (OwnerReceipt, error) {
	tracker := &trackingWriter{destination: destination, hash: sha256.New()}
	for {
		frameKind, payload, err := readResponseFrame(connection)
		if err != nil {
			if ctx.Err() != nil {
				return OwnerReceipt{}, ctx.Err()
			}
			return OwnerReceipt{}, err
		}
		switch frameKind {
		case frameData:
			if tracker.size+int64(len(payload)) > MaxObjectBytes {
				return OwnerReceipt{}, fmt.Errorf("snapshot client: object exceeds size limit")
			}
			if _, err := tracker.Write(payload); err != nil {
				return OwnerReceipt{}, err
			}
		case frameReceipt:
			var receipt OwnerReceipt
			if err := decodeStrict(payload, &receipt); err != nil {
				return OwnerReceipt{}, err
			}
			if err := VerifyOwnerReceipt(receipt); err != nil {
				return OwnerReceipt{}, err
			}
			if receipt.RequestID != request.RequestID || receipt.SnapshotSetID != begin.SnapshotSetID ||
				receipt.ObjectName != expected.Name || receipt.Kind != expected.Kind ||
				receipt.ReleaseID != request.ReleaseID || receipt.ConfigSHA256 != request.ConfigSHA256 ||
				receipt.CandidateRootBinding != request.CandidateRootBinding ||
				receipt.SourceSHA256 != HashSourcePath(expected.SourcePath) ||
				receipt.OwnerUID != currentUID() || receipt.Size != tracker.size ||
				receipt.SHA256 != hex.EncodeToString(tracker.hash.Sum(nil)) {
				return OwnerReceipt{}, fmt.Errorf("snapshot client: owner receipt binding mismatch")
			}
			return receipt, nil
		default:
			return OwnerReceipt{}, fmt.Errorf("snapshot client: unexpected object frame")
		}
	}
}

func readResponseFrame(connection io.Reader) (byte, []byte, error) {
	kind, payload, err := readFrame(connection)
	if err != nil {
		return 0, nil, err
	}
	if kind != frameError {
		return kind, payload, nil
	}
	var response errorFrame
	if err := decodeStrict(payload, &response); err != nil {
		return 0, nil, err
	}
	return 0, nil, fmt.Errorf("snapshot owner: %s: %s", response.Code, response.Message)
}

func validateExpected(root candidate.Root, values []ExpectedObject) ([]ExpectedObject, error) {
	if len(values) != len(fixedOwners) {
		return nil, fmt.Errorf("snapshot client: exactly four expected objects are required")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	byName := make(map[string]ExpectedObject, len(values))
	for _, value := range values {
		kind, ok := FixedKind(value.Name)
		if !ok || value.Kind != kind || value.SourcePath == "" || byName[value.Name].Name != "" {
			return nil, fmt.Errorf("snapshot client: invalid fixed object")
		}
		if _, err := capability.Relative(value.SourcePath); err != nil {
			return nil, fmt.Errorf("snapshot client: expected source is outside candidate root")
		}
		byName[value.Name] = value
	}
	ordered := make([]ExpectedObject, 0, len(fixedOwners))
	for _, fixed := range fixedOwners {
		ordered = append(ordered, byName[fixed.Name])
	}
	return ordered, nil
}

type trackingWriter struct {
	destination io.Writer
	hash        hash.Hash
	size        int64
}

func (w *trackingWriter) Write(data []byte) (int, error) {
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
