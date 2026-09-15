package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeManagerFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "platform.yaml")
	data := `channels:
  - name: main
    type: discord
    enabled: true
    webhook: ${TEST_CONFIG_WEBHOOK}
    options:
      nested:
        tokens:
          - ${TEST_CONFIG_TOKEN}
sources:
  - name: feed
    type: rss
    url: https://example.com/feed
    sinks: [main]
    routing:
      ai_policy: required
      on_ai_failure: silent
      critical_keywords: [" sanctions ", "SANCTIONS"]
      direct: {min_score: 8.5, max_per_day: 4, priority: 80}
      default: silent
    options:
      headers:
        Authorization: ${TEST_CONFIG_TOKEN}
mirror_channels: [main]
llm:
  api_key: ${TEST_CONFIG_LLM_KEY}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigManagerAccessorsReturnDetachedCopies(t *testing.T) {
	t.Setenv("TEST_CONFIG_WEBHOOK", "https://secret.example/webhook")
	t.Setenv("TEST_CONFIG_TOKEN", "secret-token")
	t.Setenv("TEST_CONFIG_LLM_KEY", "secret-llm-key")
	manager, err := NewConfigManager(writeManagerFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	got := manager.Get()
	*got.Channels[0].Enabled = false
	got.Channels[0].Options["nested"].(map[string]interface{})["tokens"].([]interface{})[0] = "changed"
	got.Sources[0].Sinks[0] = "changed"
	*got.Sources[0].Routing.Direct.MinScore = 1
	got.Sources[0].Routing.CriticalKeywords[0] = "changed"
	got.Sources[0].Options["headers"].(map[string]interface{})["Authorization"] = "changed"
	got.MirrorChannels[0] = "changed"

	snapshot, _, err := manager.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Channels[0].Webhook = "changed"
	channel, ok := manager.GetChannelConfig("main")
	if !ok {
		t.Fatal("channel not found")
	}
	channel.Options["nested"].(map[string]interface{})["tokens"].([]interface{})[0] = "changed-again"
	source, ok := manager.GetSourceConfig("feed")
	if !ok {
		t.Fatal("source not found")
	}
	source.Sinks[0] = "changed-again"
	source.Routing.Direct.MaxPerDay = 1

	current := manager.Get()
	if !current.Channels[0].IsEnabled() {
		t.Fatal("Get returned a live enabled pointer")
	}
	if current.Channels[0].Webhook != "https://secret.example/webhook" {
		t.Fatalf("snapshot mutated live webhook: %q", current.Channels[0].Webhook)
	}
	if token := current.Channels[0].Options["nested"].(map[string]interface{})["tokens"].([]interface{})[0]; token != "secret-token" {
		t.Fatalf("nested channel option was not detached: %v", token)
	}
	if current.Sources[0].Sinks[0] != "main" ||
		current.Sources[0].Options["headers"].(map[string]interface{})["Authorization"] != "secret-token" {
		t.Fatalf("source accessor returned live state: %+v", current.Sources[0])
	}
	if *current.Sources[0].Routing.Direct.MinScore != 8.5 || current.Sources[0].Routing.Direct.MaxPerDay != 4 {
		t.Fatalf("routing accessor returned live state: %+v", current.Sources[0].Routing)
	}
	if !reflect.DeepEqual(current.Sources[0].Routing.CriticalKeywords, []string{"sanctions"}) {
		t.Fatalf("routing keywords were not normalized/detached: %v", current.Sources[0].Routing.CriticalKeywords)
	}
	if current.MirrorChannels[0] != "main" {
		t.Fatalf("mirror channels were not detached: %v", current.MirrorChannels)
	}
}

func TestConfigManagerSubscriberReceivesDetachedCopies(t *testing.T) {
	manager, err := NewConfigManager(writeManagerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	manager.Subscribe(func(_, next *PlatformConfig) {
		next.Sources[0].Name = "subscriber-mutated"
		next.Channels[0].Options["nested"].(map[string]interface{})["tokens"].([]interface{})[0] = "subscriber-mutated"
		called <- struct{}{}
	})
	next := manager.Get()
	next.Alert.Enabled = true
	if err := manager.Update(next); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber was not called")
	}
	current := manager.Get()
	if current.Sources[0].Name != "feed" {
		t.Fatal("subscriber mutated live source")
	}
	if token := current.Channels[0].Options["nested"].(map[string]interface{})["tokens"].([]interface{})[0]; token == "subscriber-mutated" {
		t.Fatal("subscriber mutated live nested options")
	}
}

func TestConfigMutationPreservesEnvironmentReferences(t *testing.T) {
	const secret = "must-not-be-written-to-config"
	t.Setenv("TEST_CONFIG_WEBHOOK", secret)
	t.Setenv("TEST_CONFIG_TOKEN", secret)
	t.Setenv("TEST_CONFIG_LLM_KEY", secret)
	path := writeManagerFixture(t)
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ToggleSource("feed", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("expanded secret leaked into persisted config")
	}
	for _, placeholder := range []string{"${TEST_CONFIG_WEBHOOK}", "${TEST_CONFIG_TOKEN}", "${TEST_CONFIG_LLM_KEY}"} {
		if !strings.Contains(string(data), placeholder) {
			t.Fatalf("environment reference %q was not preserved", placeholder)
		}
	}
	if !strings.Contains(string(data), "routing:") || !strings.Contains(string(data), "min_score: 8.5") ||
		!strings.Contains(string(data), "critical_keywords:") || !strings.Contains(string(data), "- sanctions") {
		t.Fatal("typed routing was lost while persisting an unrelated mutation")
	}
}

func TestConfigMutationFencesExternalFileChange(t *testing.T) {
	path := writeManagerFixture(t)
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	external := []byte("sources:\n  - name: external\n    type: rss\n")
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	err = manager.ToggleSource("feed", false)
	if !IsConflictError(err) {
		t.Fatalf("external edit error=%v", err)
	}
	if !manager.Get().Sources[0].IsEnabled() {
		t.Fatal("failed save mutated live config")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(external) {
		t.Fatal("external edit was overwritten")
	}
}

func TestReloadPreviewFingerprintFencesDiskAndRevision(t *testing.T) {
	path := writeManagerFixture(t)
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.PreviewReload()
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := manager.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if preview.FileFingerprint != hex.EncodeToString(sum[:]) {
		t.Fatalf("preview fingerprint=%q", preview.FileFingerprint)
	}
	if err := os.WriteFile(path, append(data, []byte("\n# external edit\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.CommitPreview(preview, revision); !IsConflictError(err) {
		t.Fatalf("disk-fenced commit error=%v", err)
	}
}

func TestProductionTopologyTemplateLoadsWithoutSecrets(t *testing.T) {
	t.Setenv("BLOCKBEATS_API_KEY", "blockbeats-test-canary")
	t.Setenv("DISCORD_PUSH_WEBHOOK", "https://example.invalid/discord-webhook-canary")
	cfg, err := LoadPlatform(filepath.Join("..", "..", "config.platform.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 19 || len(cfg.Channels) != 3 {
		t.Fatalf("template topology sources=%d channels=%d (want 19 sources and 3 channels after Feishu removal)", len(cfg.Sources), len(cfg.Channels))
	}
	assertTemplateRuntimeSafety(t, cfg, "blockbeats-test-canary")
	data, err := os.ReadFile(filepath.Join("..", "..", "config.platform.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must-not-be-written-to-config") {
		t.Fatal("test secret leaked into production template")
	}
	var raw interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	assertTemplateSecretsUseEnvironment(t, raw, "")
}

func TestProductionTopologyMatchesTemplateAndContainsNoInlineSecrets(t *testing.T) {
	livePath := filepath.Join("..", "..", "config.platform.yaml")
	templatePath := filepath.Join("..", "..", "config.platform.yaml.example")
	if _, err := os.Stat(livePath); os.IsNotExist(err) {
		t.Skip("ignored live production config is not present")
	} else if err != nil {
		t.Fatal(err)
	}
	live, err := LoadPlatform(livePath)
	if err != nil {
		t.Fatal(err)
	}
	template, err := LoadPlatform(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Sources) != len(template.Sources) || len(live.Channels) != len(template.Channels) {
		t.Fatalf("topology size live=%d/%d template=%d/%d", len(live.Sources), len(live.Channels), len(template.Sources), len(template.Channels))
	}
	for i := range live.Channels {
		if live.Channels[i].Name != template.Channels[i].Name || live.Channels[i].Type != template.Channels[i].Type {
			t.Fatalf("channel %d mismatch live=%s/%s template=%s/%s", i, live.Channels[i].Name, live.Channels[i].Type, template.Channels[i].Name, template.Channels[i].Type)
		}
	}
	for i := range live.Sources {
		got, want := live.Sources[i], template.Sources[i]
		gotOptions, wantOptions := got.Options, want.Options
		if got.Name != want.Name || got.Type != want.Type || got.URL != want.URL || got.Interval != want.Interval ||
			!reflect.DeepEqual(got.Schedule, want.Schedule) || !reflect.DeepEqual(got.Sinks, want.Sinks) ||
			!reflect.DeepEqual(got.Enabled, want.Enabled) || got.TradingDaysOnly != want.TradingDaysOnly ||
			!reflect.DeepEqual(gotOptions, wantOptions) {
			t.Fatalf("source %d topology mismatch live=%+v template=%+v", i, got, want)
		}
		if got.DeliveryMode != want.DeliveryMode || got.BriefingTarget != want.BriefingTarget ||
			!reflect.DeepEqual(got.Routing, want.Routing) {
			t.Fatalf("source %d delivery mismatch live=%+v template=%+v", i, got, want)
		}
	}
	for _, path := range []string{livePath, templatePath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		assertTemplateSecretsUseEnvironment(t, raw, "")
	}
}


func assertTemplateRuntimeSafety(t *testing.T, cfg *PlatformConfig, blockBeatsKey string) {
	t.Helper()
	for _, src := range cfg.Sources {
		// 任意源的 options 都不得内嵌凭据(含 BlockBeats key 哨兵);
		// BlockBeats 脚本由 news-aggregate-* 的 market-briefing 子进程调用,
		// 凭据一律走进程环境。
		assertCommandValuesDoNotContain(t, src.Options, "sources."+src.Name+".options", blockBeatsKey)
	}

	for _, channel := range cfg.Channels {
		if channel.Name != "discord-webhook" {
			continue
		}
		if channel.Webhook != "https://example.invalid/discord-webhook-canary" {
			t.Fatal("discord webhook was not expanded from DISCORD_PUSH_WEBHOOK")
		}
		if plain, ok := channel.Options["webhook_plain"].(bool); !ok || !plain {
			t.Fatalf("discord webhook_plain must be boolean true, got %T", channel.Options["webhook_plain"])
		}
		return
	}
	t.Fatal("discord-webhook channel missing")
}

func assertCommandValuesDoNotContain(t *testing.T, value interface{}, path, forbidden string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			nextPath := strings.TrimPrefix(path+"."+key, ".")
			lower := strings.ToLower(key)
			if lower == "command" || lower == "args" {
				assertStringsDoNotContain(t, item, nextPath, forbidden)
			}
			assertCommandValuesDoNotContain(t, item, nextPath, forbidden)
		}
	case []interface{}:
		for i, item := range typed {
			assertCommandValuesDoNotContain(t, item, fmt.Sprintf("%s[%d]", path, i), forbidden)
		}
	}
}

func assertStringsDoNotContain(t *testing.T, value interface{}, path, forbidden string) {
	t.Helper()
	switch typed := value.(type) {
	case string:
		if forbidden != "" && strings.Contains(typed, forbidden) {
			t.Errorf("runtime config %s contains expanded credential material", path)
		}
	case []interface{}:
		for i, item := range typed {
			assertStringsDoNotContain(t, item, fmt.Sprintf("%s[%d]", path, i), forbidden)
		}
	case map[string]interface{}:
		for key, item := range typed {
			assertStringsDoNotContain(t, item, path+"."+key, forbidden)
		}
	}
}

var (
	knownCredentialPattern = regexp.MustCompile(`(?i)\b(?:bbp_[a-z0-9]{24,}|sk-[a-z0-9_-]{20,}|gh[pousr]_[a-z0-9]{20,}|AKIA[A-Z0-9]{16})\b`)
	discordWebhookPattern  = regexp.MustCompile(`(?i)https://(?:discord(?:app)?\.com)/api/webhooks/[0-9]+/[a-z0-9._-]+`)
	credentialArgPattern   = regexp.MustCompile(`(?i)(?:--?(?:api[-_]?key|access[-_]?token|token|secret|password|webhook)[[:space:]]+|[?&](?:api[-_]?key|access[-_]?token|token|secret|password|webhook)=|(?:^|[[:space:],;])(?:api[-_]?key|access[-_]?token|token|secret|password|webhook)[[:space:]]*[:=][[:space:]]*)["']?([^[:space:]&"']+)`)
)

func assertTemplateSecretsUseEnvironment(t *testing.T, value interface{}, path string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			nextPath := strings.TrimPrefix(path+"."+key, ".")
			lower := strings.ToLower(key)
			sensitive := strings.Contains(lower, "api_key") ||
				strings.Contains(lower, "secret") ||
				strings.Contains(lower, "token") ||
				strings.Contains(lower, "password") ||
				strings.Contains(lower, "webhook")
			if sensitive {
				values, ok := item.([]interface{})
				if !ok {
					values = []interface{}{item}
				}
				for _, candidate := range values {
					if text, ok := candidate.(string); ok && text != "" && !strings.HasPrefix(text, "${") {
						t.Errorf("template secret %s must use an environment placeholder", nextPath)
					}
				}
			}
			assertTemplateSecretsUseEnvironment(t, item, nextPath)
		}
	case []interface{}:
		for i, item := range typed {
			assertTemplateSecretsUseEnvironment(t, item, fmt.Sprintf("%s[%d]", path, i))
		}
	case string:
		if reason := templateStringSecretReason(typed); reason != "" {
			t.Errorf("template string %s contains %s", path, reason)
		}
	}
}

func templateStringSecretReason(value string) string {
	if knownCredentialPattern.MatchString(value) {
		return "a credential-like token"
	}
	if discordWebhookPattern.MatchString(value) {
		return "a credential-bearing webhook URL"
	}
	for _, match := range credentialArgPattern.FindAllStringSubmatch(value, -1) {
		if len(match) > 1 && !strings.HasPrefix(match[1], "${") {
			return "a non-placeholder credential argument"
		}
	}
	return ""
}

func TestTemplateStringSecretDetection(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  string
		secret bool
	}{
		{name: "blockbeats token", value: "command --api-key bbp_" + strings.Repeat("a", 40), secret: true},
		{name: "command flag", value: "command --token embedded-value", secret: true},
		{name: "query credential", value: "https://example.com/feed?api_key=embedded-value", secret: true},
		{name: "error credential", value: "request failed; token=embedded-value", secret: true},
		{name: "discord webhook", value: "https://discord.com/api/webhooks/123456/credential", secret: true},
		{name: "environment flag", value: "command --api-key ${BLOCKBEATS_API_KEY}"},
		{name: "public URL", value: "https://example.com/news?category=markets&keynote=speech"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := templateStringSecretReason(test.value) != ""
			if got != test.secret {
				t.Fatalf("secret=%v, want %v", got, test.secret)
			}
		})
	}
}
