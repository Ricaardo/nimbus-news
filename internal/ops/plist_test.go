package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

func TestCandidatePlistIsDormantAndUsesNoRegisteredPort(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "com.nimbus.nimbusd-candidate.plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"<key>EnvironmentVariables</key>", "<true/>", "launchctl", "password", "api_key", "secret"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("plist contains %q", forbidden)
		}
	}
	cfg, err := systemconfig.Load(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := cfg.LoadPortsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	registry := snapshot.Registry
	pattern := regexp.MustCompile(`127\.0\.0\.1:(\d+)`)
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		port, _ := strconv.Atoi(match[1])
		if registry.Contains(port) {
			t.Fatalf("plist uses registered port %d", port)
		}
	}
	if tool, err := exec.LookPath("plutil"); err == nil {
		if output, err := exec.Command(tool, "-lint", path).CombinedOutput(); err != nil {
			t.Fatalf("plutil: %v %s", err, output)
		}
	}
	for _, required := range []string{
		"/Users/x/nimbus-os/news/config.platform.yaml",
		"/Users/x/nimbus-os/news/config/system.yaml",
		"<key>RunAtLoad</key>\n  <false/>",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("plist is missing %q", required)
		}
	}
	for _, configPath := range []string{"/Users/x/nimbus-os/news/config.platform.yaml", "/Users/x/nimbus-os/news/config/system.yaml"} {
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("plist config path %s: %v", configPath, err)
		}
	}
	root := cfg.CandidateRootPath()
	for _, match := range regexp.MustCompile(`<string>(/Users/x/nimbus-os/news/data/nimbusd-candidate[^<]*)</string>`).FindAllStringSubmatch(text, -1) {
		relative, err := filepath.Rel(root, match[1])
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("plist candidate path escapes root: %s", match[1])
		}
	}
	repo := filepath.Join("..", "..")
	help := exec.Command("go", "run", "./cmd/nimbusd", "-h")
	help.Dir = repo
	helpOutput, helpErr := help.CombinedOutput()
	if helpErr != nil {
		t.Fatalf("nimbusd parse smoke: %v %s", helpErr, helpOutput)
	}
	for _, flagName := range regexp.MustCompile(`<string>(--[^<]+)</string>`).FindAllStringSubmatch(text, -1) {
		if !strings.Contains(string(helpOutput), "-"+strings.TrimPrefix(flagName[1], "--")+" ") {
			t.Fatalf("nimbusd does not expose plist flag %s", flagName[1])
		}
	}
}
