package snapshotipc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SocketRelative = "run/snapshot-v1.sock"

	RequestSchema = "nimbus-owner-snapshot-request/v1"
	ReceiptSchema = "nimbus-owner-snapshot-receipt/v1"
	SetSchema     = "nimbus-owner-snapshot-set/v1"

	ControlSchemaVersion = 2

	maxControlFrame = 64 << 10
	maxDataFrame    = 64 << 10
	MaxObjectBytes  = 256 << 20

	frameRequest  byte = 1
	frameBegin    byte = 2
	frameData     byte = 3
	frameReceipt  byte = 4
	frameComplete byte = 5
	frameError    byte = 6
)

var fixedOwners = []ExpectedObject{
	{Name: "news", Kind: "bolt"},
	{Name: "control", Kind: "sqlite"},
	{Name: "signals", Kind: "jsonl"},
	{Name: "shadow_feed", Kind: "rolling_jsonl"},
}

var fixedOwnerMetadata = map[string]struct {
	kind          string
	schemaVersion int
}{
	"news":        {kind: "bolt"},
	"control":     {kind: "sqlite", schemaVersion: ControlSchemaVersion},
	"signals":     {kind: "signals_jsonl"},
	"shadow_feed": {kind: "news_feed_jsonl_v1"},
}

type Request struct {
	Schema               string `json:"schema"`
	RequestID            string `json:"request_id"`
	Operation            string `json:"operation"`
	ReleaseID            string `json:"release_id"`
	ConfigSHA256         string `json:"config_sha256"`
	CandidateRootBinding string `json:"candidate_root_binding"`
}

type ObjectBegin struct {
	RequestID     string `json:"request_id"`
	SnapshotSetID string `json:"snapshot_set_id"`
	ObjectName    string `json:"object_name"`
	Kind          string `json:"kind"`
}

type OwnerReceipt struct {
	Schema               string `json:"schema"`
	Hash                 string `json:"hash"`
	RequestID            string `json:"request_id"`
	SnapshotSetID        string `json:"snapshot_set_id"`
	ObjectName           string `json:"object_name"`
	Kind                 string `json:"kind"`
	OwnerKind            string `json:"owner_kind"`
	ReleaseID            string `json:"release_id"`
	ConfigSHA256         string `json:"config_sha256"`
	CandidateRootBinding string `json:"candidate_root_binding"`
	SourceSHA256         string `json:"source_sha256"`
	SHA256               string `json:"sha256"`
	Size                 int64  `json:"size"`
	OwnerUID             int    `json:"owner_uid"`
	SchemaVersion        int    `json:"schema_version,omitempty"`
	StartedAt            string `json:"started_at"`
	CompletedAt          string `json:"completed_at"`
}

type SetReceipt struct {
	Schema               string   `json:"schema"`
	Hash                 string   `json:"hash"`
	RequestID            string   `json:"request_id"`
	SnapshotSetID        string   `json:"snapshot_set_id"`
	ReleaseID            string   `json:"release_id"`
	ConfigSHA256         string   `json:"config_sha256"`
	CandidateRootBinding string   `json:"candidate_root_binding"`
	ReceiptHashes        []string `json:"receipt_hashes"`
	OwnerUID             int      `json:"owner_uid"`
	StartedAt            string   `json:"started_at"`
	CompletedAt          string   `json:"completed_at"`
}

type OwnerResult struct {
	Kind          string
	SHA256        string
	Size          int64
	SchemaVersion int
}

type SnapshotFunc func(context.Context, io.Writer) (OwnerResult, error)

type Owner struct {
	Name       string
	Kind       string
	ResultKind string
	SourcePath string
	Snapshot   SnapshotFunc
}

type ExpectedObject struct {
	Name       string
	Kind       string
	SourcePath string
}

type errorFrame struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewID() (string, error) {
	var value [32]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func FixedKind(name string) (string, bool) {
	for _, object := range fixedOwners {
		if object.Name == name {
			return object.Kind, true
		}
	}
	return "", false
}

func FixedObjects() []ExpectedObject {
	return append([]ExpectedObject(nil), fixedOwners...)
}

func HashSourcePath(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(sum[:])
}

func VerifyOwnerReceipt(receipt OwnerReceipt) error {
	hash := receipt.Hash
	receipt.Hash = ""
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if receipt.Schema != ReceiptSchema || !validHash(hash) || hashBytes(payload) != hash ||
		!validHash(receipt.RequestID) || !validHash(receipt.SnapshotSetID) ||
		!validHash(receipt.ConfigSHA256) || !validHash(receipt.CandidateRootBinding) ||
		!validHash(receipt.SourceSHA256) || !validHash(receipt.SHA256) ||
		receipt.ReleaseID == "" || receipt.Size < 0 || receipt.OwnerUID < 0 {
		return fmt.Errorf("snapshot receipt: invalid binding")
	}
	kind, ok := FixedKind(receipt.ObjectName)
	metadata, metadataOK := fixedOwnerMetadata[receipt.ObjectName]
	if !ok || !metadataOK || receipt.Kind != kind || receipt.OwnerKind != metadata.kind ||
		receipt.SchemaVersion != metadata.schemaVersion {
		return fmt.Errorf("snapshot receipt: invalid owner")
	}
	started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	if startErr != nil || completeErr != nil || completed.Before(started) {
		return fmt.Errorf("snapshot receipt: invalid time")
	}
	return nil
}

func VerifySetReceipt(receipt SetReceipt) error {
	hash := receipt.Hash
	receipt.Hash = ""
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if receipt.Schema != SetSchema || !validHash(hash) || hashBytes(payload) != hash ||
		!validHash(receipt.RequestID) || !validHash(receipt.SnapshotSetID) ||
		!validHash(receipt.ConfigSHA256) || !validHash(receipt.CandidateRootBinding) ||
		receipt.ReleaseID == "" || receipt.OwnerUID < 0 ||
		len(receipt.ReceiptHashes) != len(fixedOwners) {
		return fmt.Errorf("snapshot set receipt: invalid binding")
	}
	for _, value := range receipt.ReceiptHashes {
		if !validHash(value) {
			return fmt.Errorf("snapshot set receipt: invalid owner receipt hash")
		}
	}
	started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	if startErr != nil || completeErr != nil || completed.Before(started) {
		return fmt.Errorf("snapshot set receipt: invalid time")
	}
	return nil
}

func signReceipt(receipt *OwnerReceipt) error {
	receipt.Hash = ""
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receipt.Hash = hashBytes(payload)
	return nil
}

func signSetReceipt(receipt *SetReceipt) error {
	receipt.Hash = ""
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receipt.Hash = hashBytes(payload)
	return nil
}

func validateRequest(request Request) error {
	if request.Schema != RequestSchema || !validHash(request.RequestID) ||
		request.Operation != "snapshot_set" || !validHash(request.ConfigSHA256) ||
		!validHash(request.CandidateRootBinding) || request.ReleaseID == "" {
		return fmt.Errorf("snapshot request: invalid binding")
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func normalizeHash(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func writeJSONFrame(w io.Writer, kind byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxControlFrame {
		return fmt.Errorf("snapshot protocol: control frame too large")
	}
	return writeFrame(w, kind, data)
}

func writeFrame(w io.Writer, kind byte, data []byte) error {
	limit := maxControlFrame
	if kind == frameData {
		limit = maxDataFrame
	}
	if len(data) > limit {
		return fmt.Errorf("snapshot protocol: frame too large")
	}
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := io.Copy(w, bytes.NewReader(header[:])); err != nil {
		return err
	}
	_, err := io.Copy(w, bytes.NewReader(data))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	size := int(binary.BigEndian.Uint32(header[1:]))
	limit := maxControlFrame
	if header[0] == frameData {
		limit = maxDataFrame
	}
	if size < 0 || size > limit {
		return 0, nil, fmt.Errorf("snapshot protocol: frame too large")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	return header[0], data, nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("snapshot protocol: extra JSON value")
	}
	return nil
}

func currentUID() int { return os.Geteuid() }
