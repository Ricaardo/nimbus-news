package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"

	"golang.org/x/sys/unix"
)

func writeCAS(root candidate.Root, directory, suffix string, content []byte) (string, error) {
	capability, err := root.OpenCapability()
	if err != nil {
		return "", err
	}
	defer capability.Close()
	return capability.WriteCAS(directory, suffix, content)
}

// mkdiratAll is retained for the candidate-only cutover compatibility layer.
// It walks one descriptor at a time and never follows a path component.
func mkdiratAll(rootFD int, relative string, perm uint32) error {
	if relative == "." || relative == "" {
		return nil
	}
	fd, err := unix.Dup(rootFD)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, component := range strings.Split(strings.ReplaceAll(relative, "\\", "/"), "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("unsafe directory component")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if err := unix.Mkdirat(fd, component, perm); err != nil && !errors.Is(err, unix.EEXIST) {
				return err
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return openErr
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func safeName(value string) bool {
	return value != "" && !strings.Contains(value, "/") && !strings.Contains(value, "\\") && value != "." && value != ".."
}
