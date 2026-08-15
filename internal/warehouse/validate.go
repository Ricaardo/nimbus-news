package warehouse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"golang.org/x/sys/unix"
)

type Validated struct {
	Manifest     Manifest
	ManifestPath string
	ManifestHash string
	ManifestData []byte
	TotalBytes   uint64
	rootInfo     os.FileInfo
}

func ValidateStaging(manifestPath string) (*Validated, error) {
	return validateStagingWithHook(manifestPath, nil)
}

func validateStagingWithHook(manifestPath string, afterRootOpen func() error) (*Validated, error) {
	absoluteManifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("warehouse validate: manifest path: %w", err)
	}
	root, err := openDirectoryNoSymlink(filepath.Dir(absoluteManifest))
	if err != nil {
		return nil, fmt.Errorf("warehouse validate: manifest root: %w", err)
	}
	defer root.Close()
	rootInfo, err := root.Stat()
	if err != nil {
		return nil, fmt.Errorf("warehouse validate: manifest root: %w", err)
	}
	if afterRootOpen != nil {
		if err := afterRootOpen(); err != nil {
			return nil, err
		}
	}
	return validateStagingAt(root, rootInfo, filepath.Base(absoluteManifest), absoluteManifest)
}

func validateStagingAt(root *os.File, rootInfo os.FileInfo, manifestName, manifestPath string) (*Validated, error) {
	data, err := readRegularAt(root, manifestName, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("warehouse validate: read manifest: %w", err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	var total uint64
	for _, entry := range manifest.Files {
		handle, info, err := openRegularAt(root, entry.Path)
		if err != nil {
			return nil, fmt.Errorf("warehouse validate: %s: %w", entry.Path, err)
		}
		if err := validateOpenedFile(handle, info, entry, nil); err != nil {
			return nil, fmt.Errorf("warehouse validate: %s: %w", entry.Path, err)
		}
		total += uint64(entry.ByteSize)
	}
	digest := sha256.Sum256(data)
	return &Validated{
		Manifest: manifest, ManifestPath: manifestPath, ManifestHash: hex.EncodeToString(digest[:]),
		ManifestData: append([]byte(nil), data...), TotalBytes: total, rootInfo: rootInfo,
	}, nil
}

func validateFile(path string, expected File) error {
	return validateFileWithHook(path, expected, nil)
}

func validateFileWithHook(path string, expected File, afterOpen func() error) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	root, err := openDirectoryNoSymlink(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	defer root.Close()
	handle, info, err := openRegularAt(root, filepath.Base(absolute))
	if err != nil {
		return err
	}
	return validateOpenedFile(handle, info, expected, afterOpen)
}

func validateOpenedFile(handle *os.File, info os.FileInfo, expected File, afterOpen func() error) error {
	closeHandle := true
	defer func() {
		if closeHandle {
			_ = handle.Close()
		}
	}()
	if info.Size() != expected.ByteSize {
		return fmt.Errorf("byte_size mismatch: got %d want %d", info.Size(), expected.ByteSize)
	}
	if afterOpen != nil {
		if err := afterOpen(); err != nil {
			return err
		}
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("sha256 mismatch")
	}
	if _, err := handle.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader, err := file.NewParquetReader(handle)
	if err != nil {
		return fmt.Errorf("parquet footer: %w", err)
	}
	closeHandle = false // parquet reader owns and closes the same descriptor.
	defer reader.Close()
	if reader.MetaData().NumRows != expected.RowCount {
		return fmt.Errorf("row_count mismatch: got %d want %d", reader.MetaData().NumRows, expected.RowCount)
	}
	arrowReader, err := pqarrow.NewFileReader(reader, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		return fmt.Errorf("parquet schema: %w", err)
	}
	schema, err := arrowReader.Schema()
	if err != nil {
		return fmt.Errorf("parquet schema: %w", err)
	}
	actual, err := canonicalSchema(schema)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected.Schema) {
		return fmt.Errorf("schema mismatch: got %+v want %+v", actual, expected.Schema)
	}
	return nil
}

func readRegularAt(root *os.File, relative string, maxBytes int64) ([]byte, error) {
	handle, info, err := openRegularAt(root, relative)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	if info.Size() <= 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("invalid file size %d", info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(handle, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("file changed while reading")
	}
	return data, nil
}

func openDirectoryNoSymlink(path string) (*os.File, error) {
	root, parent, _, err := openDirectoryNoSymlinkWithParent(path)
	if parent != nil {
		_ = parent.Close()
	}
	return root, err
}

func openDirectoryNoSymlinkWithParent(path string) (*os.File, *os.File, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, "", err
	}
	absolute = filepath.Clean(absolute)
	if absolute == string(filepath.Separator) {
		return nil, nil, "", fmt.Errorf("filesystem root is not a trusted staging directory")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, "", err
	}
	components := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return nil, nil, "", fmt.Errorf("directory component %q must be a real directory without symlinks: %w", component, openErr)
		}
		if index == len(components)-1 {
			parent := os.NewFile(uintptr(fd), filepath.Dir(absolute))
			root := os.NewFile(uintptr(next), absolute)
			if parent == nil || root == nil {
				if parent != nil {
					_ = parent.Close()
				} else {
					_ = unix.Close(fd)
				}
				if root != nil {
					_ = root.Close()
				} else {
					_ = unix.Close(next)
				}
				return nil, nil, "", fmt.Errorf("invalid directory descriptor")
			}
			info, statErr := root.Stat()
			if statErr != nil || !info.IsDir() {
				_ = root.Close()
				_ = parent.Close()
				if statErr != nil {
					return nil, nil, "", statErr
				}
				return nil, nil, "", fmt.Errorf("not a directory")
			}
			return root, parent, component, nil
		}
		_ = unix.Close(fd)
		fd = next
	}
	_ = unix.Close(fd)
	return nil, nil, "", fmt.Errorf("invalid empty directory path")
}

func openDirectoryAt(root *os.File, relative string) (*os.File, os.FileInfo, error) {
	if root == nil {
		return nil, nil, fmt.Errorf("trusted root descriptor is required")
	}
	if err := validateRelativePath(relative); err != nil {
		return nil, nil, err
	}
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, nil, err
	}
	for _, component := range strings.Split(relative, "/") {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, nil, fmt.Errorf("path component %q must be a real directory without symlinks: %w", component, openErr)
		}
		fd = next
	}
	handle := os.NewFile(uintptr(fd), relative)
	if handle == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("invalid directory descriptor")
	}
	info, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, nil, err
	}
	if !info.IsDir() {
		_ = handle.Close()
		return nil, nil, fmt.Errorf("not a directory")
	}
	return handle, info, nil
}

func openRegularAt(root *os.File, relative string) (*os.File, os.FileInfo, error) {
	if root == nil {
		return nil, nil, fmt.Errorf("trusted root descriptor is required")
	}
	if err := validateRelativePath(relative); err != nil {
		return nil, nil, err
	}
	parts := strings.Split(relative, "/")
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, nil, err
	}
	for _, component := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, nil, fmt.Errorf("path component %q must be a real directory without symlinks: %w", component, openErr)
		}
		fd = next
	}
	fileFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, nil, fmt.Errorf("open regular file without symlinks: %w", err)
	}
	handle := os.NewFile(uintptr(fileFD), relative)
	if handle == nil {
		_ = unix.Close(fileFD)
		return nil, nil, fmt.Errorf("invalid file descriptor")
	}
	info, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = handle.Close()
		return nil, nil, fmt.Errorf("not a regular file")
	}
	return handle, info, nil
}

func canonicalSchema(schema *arrow.Schema) ([]Field, error) {
	fields := make([]Field, 0, len(schema.Fields()))
	for _, field := range schema.Fields() {
		typeName, err := canonicalType(field.Type)
		if err != nil {
			return nil, fmt.Errorf("unsupported parquet field %q: %w", field.Name, err)
		}
		fields = append(fields, Field{Name: field.Name, Type: typeName, Nullable: field.Nullable})
	}
	return fields, nil
}

func canonicalType(dataType arrow.DataType) (string, error) {
	switch dataType.ID() {
	case arrow.BOOL:
		return "bool", nil
	case arrow.INT8, arrow.INT16, arrow.INT32, arrow.INT64,
		arrow.UINT8, arrow.UINT16, arrow.UINT32, arrow.UINT64,
		arrow.FLOAT32, arrow.FLOAT64, arrow.BINARY, arrow.DATE32:
		return dataType.String(), nil
	case arrow.STRING:
		return "string", nil
	case arrow.TIME64:
		value := dataType.(*arrow.Time64Type)
		if value.Unit != arrow.Microsecond {
			return "", fmt.Errorf("time unit must be microseconds")
		}
		return "time64us", nil
	case arrow.TIMESTAMP:
		value := dataType.(*arrow.TimestampType)
		if value.Unit != arrow.Microsecond {
			return "", fmt.Errorf("timestamp unit must be microseconds")
		}
		if value.TimeZone != "" {
			if !allowedParquetUTCZone(value.TimeZone) {
				return "", fmt.Errorf("timestamp timezone %q is not an allowed UTC identifier", value.TimeZone)
			}
			return "timestamp_us_utc", nil
		}
		return "timestamp_us", nil
	case arrow.DECIMAL32, arrow.DECIMAL64, arrow.DECIMAL128, arrow.DECIMAL256:
		value := dataType.(arrow.DecimalType)
		return fmt.Sprintf("decimal(%d,%d)", value.GetPrecision(), value.GetScale()), nil
	default:
		return "", fmt.Errorf("type %s is not in warehouse-staging/v1", dataType)
	}
}

func allowedParquetUTCZone(value string) bool {
	switch value {
	case "UTC", "Etc/UTC", "Z", "+00:00", "-00:00":
		return true
	default:
		return false
	}
}
