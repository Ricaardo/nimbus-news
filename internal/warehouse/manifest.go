// Package warehouse validates and publishes immutable candidate warehouse snapshots.
// It does not serve warehouse queries or own the production warehouse service.
package warehouse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const SchemaVersion = "warehouse-staging/v1"

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	tablePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)
	semverPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:[-+][0-9A-Za-z.-]+)?$`)
	hashPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	typePattern       = regexp.MustCompile(`^(bool|u?int(8|16|32|64)|float(32|64)|string|binary|date32|time64us|timestamp_us(_utc)?|decimal\([1-9][0-9]*,[0-9]+\))$`)
)

type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

type File struct {
	SourceDB string  `json:"source_db"`
	Table    string  `json:"table"`
	Path     string  `json:"path"`
	ByteSize int64   `json:"byte_size"`
	RowCount int64   `json:"row_count"`
	SHA256   string  `json:"sha256"`
	Schema   []Field `json:"schema"`
	AsOf     string  `json:"as_of"`
}

type Manifest struct {
	SchemaVersion   string `json:"schema_version"`
	JobID           string `json:"job_id"`
	Producer        string `json:"producer"`
	ProducerVersion string `json:"producer_version"`
	CreatedAt       string `json:"created_at"`
	AsOf            string `json:"as_of"`
	Files           []File `json:"files"`
}

func ParseManifest(data []byte) (Manifest, error) {
	if len(data) == 0 || len(data) > 8<<20 {
		return Manifest{}, fmt.Errorf("warehouse manifest: invalid size")
	}
	if err := rejectDuplicateObjectKeys(data); err != nil {
		return Manifest{}, fmt.Errorf("warehouse manifest: %w", err)
	}
	if err := validateRequiredFields(data); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("warehouse manifest: decode: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("warehouse manifest: trailing JSON")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateRequiredFields(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return fmt.Errorf("warehouse manifest: root must be an object")
	}
	if err := requireFields("$", root, "schema_version", "job_id", "producer", "producer_version", "created_at", "as_of", "files"); err != nil {
		return err
	}
	var files []json.RawMessage
	if err := json.Unmarshal(root["files"], &files); err != nil {
		return fmt.Errorf("warehouse manifest: files must be an array")
	}
	for fileIndex, rawFile := range files {
		var file map[string]json.RawMessage
		if err := json.Unmarshal(rawFile, &file); err != nil || file == nil {
			return fmt.Errorf("warehouse manifest: files[%d] must be an object", fileIndex)
		}
		filePath := fmt.Sprintf("$.files[%d]", fileIndex)
		if err := requireFields(filePath, file, "source_db", "table", "path", "byte_size", "row_count", "sha256", "schema", "as_of"); err != nil {
			return err
		}
		var fields []json.RawMessage
		if err := json.Unmarshal(file["schema"], &fields); err != nil {
			return fmt.Errorf("warehouse manifest: %s.schema must be an array", filePath)
		}
		for fieldIndex, rawField := range fields {
			var field map[string]json.RawMessage
			if err := json.Unmarshal(rawField, &field); err != nil || field == nil {
				return fmt.Errorf("warehouse manifest: %s.schema[%d] must be an object", filePath, fieldIndex)
			}
			if err := requireFields(fmt.Sprintf("%s.schema[%d]", filePath, fieldIndex), field, "name", "type", "nullable"); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireFields(path string, object map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		value, ok := object[name]
		if !ok || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("warehouse manifest: required field %s.%s is missing or null", path, name)
		}
	}
	return nil
}

func rejectDuplicateObjectKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("trailing JSON value starting with %v", token)
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("object at %s has invalid closing token", path)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := walkJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("array at %s has invalid closing token", path)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("warehouse manifest: unsupported schema_version %q", manifest.SchemaVersion)
	}
	if _, err := uuid.Parse(manifest.JobID); err != nil {
		return fmt.Errorf("warehouse manifest: invalid job_id")
	}
	if manifest.Producer != "equity-screener" {
		return fmt.Errorf("warehouse manifest: unknown producer %q", manifest.Producer)
	}
	if !semverPattern.MatchString(manifest.ProducerVersion) {
		return fmt.Errorf("warehouse manifest: invalid producer_version")
	}
	createdAt, err := parseTimestamp(manifest.CreatedAt)
	if err != nil {
		return fmt.Errorf("warehouse manifest: invalid created_at: %w", err)
	}
	asOf, err := parseTimestamp(manifest.AsOf)
	if err != nil {
		return fmt.Errorf("warehouse manifest: invalid as_of: %w", err)
	}
	if createdAt.Before(asOf) {
		return fmt.Errorf("warehouse manifest: created_at precedes as_of")
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("warehouse manifest: files must not be empty")
	}

	keys := make([]string, 0, len(manifest.Files))
	seen := make(map[string]struct{}, len(manifest.Files))
	for index, file := range manifest.Files {
		if !identifierPattern.MatchString(file.SourceDB) || !tablePattern.MatchString(file.Table) {
			return fmt.Errorf("warehouse manifest: file %d has invalid source_db or table", index)
		}
		key := file.SourceDB + "\x00" + file.Table
		if _, ok := seen[key]; ok {
			return fmt.Errorf("warehouse manifest: duplicate source_db/table %s/%s", file.SourceDB, file.Table)
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		if err := validateRelativePath(file.Path); err != nil {
			return fmt.Errorf("warehouse manifest: file %d path: %w", index, err)
		}
		if file.ByteSize <= 0 || file.RowCount < 0 || !hashPattern.MatchString(file.SHA256) {
			return fmt.Errorf("warehouse manifest: file %d has invalid size, rows, or hash", index)
		}
		fileAsOf, err := parseTimestamp(file.AsOf)
		if err != nil || !fileAsOf.Equal(asOf) {
			return fmt.Errorf("warehouse manifest: file %d as_of mismatch", index)
		}
		if len(file.Schema) == 0 {
			return fmt.Errorf("warehouse manifest: file %d schema is empty", index)
		}
		fieldNames := make(map[string]struct{}, len(file.Schema))
		for _, field := range file.Schema {
			if strings.TrimSpace(field.Name) == "" || !typePattern.MatchString(field.Type) {
				return fmt.Errorf("warehouse manifest: file %d has invalid schema field", index)
			}
			if _, ok := fieldNames[field.Name]; ok {
				return fmt.Errorf("warehouse manifest: file %d has duplicate schema field %q", index, field.Name)
			}
			fieldNames[field.Name] = struct{}{}
		}
	}
	if !sort.StringsAreSorted(keys) {
		return fmt.Errorf("warehouse manifest: files are not sorted by source_db/table")
	}
	return nil
}

func parseTimestamp(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func validateRelativePath(value string) error {
	if value == "" || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return fmt.Errorf("must be a clean relative slash path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("dot or empty path segment is forbidden")
		}
	}
	return nil
}
