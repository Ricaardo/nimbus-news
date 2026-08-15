package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigManager 配置管理器（支持热更新）
type ConfigManager struct {
	mu           sync.RWMutex
	config       *PlatformConfig
	persisted    *PlatformConfig
	configPath   string
	subscribers  []ConfigSubscriber
	lastModified time.Time
	revision     uint64
	fingerprint  string
}

// ConfigSubscriber 配置变更订阅者
type ConfigSubscriber func(oldCfg, newCfg *PlatformConfig)

type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

type NotFoundError struct {
	Err error
}

func (e *NotFoundError) Error() string { return e.Err.Error() }
func (e *NotFoundError) Unwrap() error { return e.Err }

type ConflictError struct {
	Err error
}

func (e *ConflictError) Error() string { return e.Err.Error() }
func (e *ConflictError) Unwrap() error { return e.Err }

// SourceRuntimeMutationError reports a runtime source mutation failure and,
// when present, a failure to restore the exact pre-mutation configuration.
type SourceRuntimeMutationError struct {
	RuntimeErr  error
	RollbackErr error
}

func (e *SourceRuntimeMutationError) Error() string {
	runtimeErr := fmt.Errorf("runtime source mutation failed: %w", e.RuntimeErr)
	if e.RollbackErr == nil {
		return runtimeErr.Error()
	}
	return errors.Join(runtimeErr, fmt.Errorf("config rollback failed: %w", e.RollbackErr)).Error()
}

func (e *SourceRuntimeMutationError) Unwrap() error {
	return errors.Join(e.RuntimeErr, e.RollbackErr)
}

func IsValidationError(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

func IsNotFoundError(err error) bool {
	var target *NotFoundError
	return errors.As(err, &target)
}

func IsConflictError(err error) bool {
	var target *ConflictError
	return errors.As(err, &target)
}

// NewConfigManager 创建配置管理器
func NewConfigManager(path string) (*ConfigManager, error) {
	m := &ConfigManager{
		configPath:  path,
		subscribers: make([]ConfigSubscriber, 0),
	}

	// 首次加载配置
	if err := m.Reload(); err != nil {
		return nil, fmt.Errorf("initial config load failed: %w", err)
	}

	return m, nil
}

// Get 获取当前配置（线程安全）
func (m *ConfigManager) Get() *PlatformConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, _ := clonePlatformConfig(m.config)
	return cfg
}

// GetPath 获取配置文件路径
func (m *ConfigManager) GetPath() string {
	return m.configPath
}

// Reload 重新加载配置
func (m *ConfigManager) Reload() error {
	raw, cfg, fingerprint, _, err := readPlatformFile(m.configPath)
	if err != nil {
		return err
	}

	m.mu.Lock()
	current, err := currentFileFingerprint(m.configPath)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("verify config file: %w", err)
	}
	if current != fingerprint {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config file changed during reload")}
	}
	oldCfg := m.config
	m.config = cfg
	m.persisted = raw
	m.fingerprint = fingerprint
	m.config.FileFingerprint = fingerprint
	m.lastModified = time.Now()
	m.revision++
	m.mu.Unlock()

	// 通知订阅者
	m.notifySubscribers(oldCfg, cfg)

	return nil
}

// Update 更新配置并持久化
func (m *ConfigManager) Update(cfg *PlatformConfig) error {
	if cfg == nil {
		return &ValidationError{Err: fmt.Errorf("config is required")}
	}
	next, err := clonePlatformConfig(cfg)
	if err != nil {
		return err
	}
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	m.mu.Lock()
	oldCfg := m.config
	if err := m.persistLocked(next); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("save config failed: %w", err)
	}
	newCfg := m.config
	m.mu.Unlock()

	// 通知订阅者
	m.notifySubscribers(oldCfg, newCfg)

	return nil
}

// NormalizePlatformConfig applies defaults and validates a whole-config mutation.
func NormalizePlatformConfig(cfg *PlatformConfig) error {
	if cfg == nil {
		return &ValidationError{Err: fmt.Errorf("config is required")}
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return &ValidationError{Err: err}
	}
	return nil
}

// UpdateChannel 更新单个渠道配置
func (m *ConfigManager) UpdateChannel(name string, channelCfg ChannelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	// 找到并更新渠道
	found := false
	for i, ch := range next.Channels {
		if ch.Name == name {
			channelCfg.Name = name
			if channelCfg.Enabled == nil {
				channelCfg.Enabled = ch.Enabled
			}
			next.Channels[i] = channelCfg
			found = true
			break
		}
	}

	if !found {
		return &NotFoundError{Err: fmt.Errorf("channel not found: %s", name)}
	}
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// ToggleChannel 切换渠道启用状态
func (m *ConfigManager) ToggleChannel(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	// 找到并更新渠道
	found := false
	for i, ch := range next.Channels {
		if ch.Name == name {
			next.Channels[i].Enabled = &enabled
			found = true
			break
		}
	}

	if !found {
		return &NotFoundError{Err: fmt.Errorf("channel not found: %s", name)}
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// UpdateSource 更新单个数据源配置
func (m *ConfigManager) UpdateSource(name string, sourceCfg SourceConfig) error {
	var err error
	sourceCfg, err = NormalizeSourceConfig(sourceCfg)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	// 找到并更新数据源
	found := false
	for i, src := range next.Sources {
		if src.Name == name {
			sourceCfg.Name = name
			if sourceCfg.Enabled == nil {
				sourceCfg.Enabled = src.Enabled
			}
			next.Sources[i] = sourceCfg
			found = true
			break
		}
	}

	if !found {
		return &NotFoundError{Err: fmt.Errorf("source not found: %s", name)}
	}

	// 持久化
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// UpdateSourceWithRuntime persists a source update, applies the corresponding
// runtime mutation without holding m.mu, and restores the exact pre-update file
// if runtime activation fails and no intervening config change occurred.
func (m *ConfigManager) UpdateSourceWithRuntime(
	name string,
	sourceCfg SourceConfig,
	update func() error,
) error {
	var err error
	sourceCfg, err = NormalizeSourceConfig(sourceCfg)
	if err != nil {
		return err
	}

	m.mu.Lock()
	originalData, err := os.ReadFile(m.configPath)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("read config snapshot failed: %w", err)
	}
	originalFingerprint := fingerprintBytes(originalData)
	if originalFingerprint != m.fingerprint {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config file changed outside the process")}
	}
	originalRaw, originalLive, err := parsePlatformBytes(originalData)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("parse config snapshot failed: %w", err)
	}
	originalRevision := m.revision

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	found := false
	for i, src := range next.Sources {
		if src.Name == name {
			sourceCfg.Name = name
			if sourceCfg.Enabled == nil {
				sourceCfg.Enabled = src.Enabled
			}
			next.Sources[i] = sourceCfg
			found = true
			break
		}
	}
	if !found {
		m.mu.Unlock()
		return &NotFoundError{Err: fmt.Errorf("source not found: %s", name)}
	}
	if err := NormalizePlatformConfig(next); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.persistLocked(next); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("save config failed: %w", err)
	}
	updateRevision := m.revision
	updateFingerprint := m.fingerprint
	m.mu.Unlock()

	if update == nil {
		return nil
	}
	if runtimeErr := update(); runtimeErr != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if updateRevision != originalRevision+1 ||
			m.revision != updateRevision ||
			m.fingerprint != updateFingerprint {
			return &SourceRuntimeMutationError{
				RuntimeErr: runtimeErr,
				RollbackErr: &ConflictError{Err: fmt.Errorf(
					"config changed while runtime source update was in progress",
				)},
			}
		}
		rollbackErr := m.persistBytesLocked(originalData, originalRaw, originalLive)
		return &SourceRuntimeMutationError{RuntimeErr: runtimeErr, RollbackErr: rollbackErr}
	}
	return nil
}

// ToggleSource 切换数据源启用状态
func (m *ConfigManager) ToggleSource(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	// 找到并更新数据源
	found := false
	for i, src := range next.Sources {
		if src.Name == name {
			next.Sources[i].Enabled = &enabled
			found = true
			break
		}
	}

	if !found {
		return &NotFoundError{Err: fmt.Errorf("source not found: %s", name)}
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// Subscribe 订阅配置变更
func (m *ConfigManager) Subscribe(callback ConfigSubscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribers = append(m.subscribers, callback)
}

// notifySubscribers 通知所有订阅者
func (m *ConfigManager) notifySubscribers(oldCfg, newCfg *PlatformConfig) {
	m.mu.RLock()
	subscribers := append([]ConfigSubscriber(nil), m.subscribers...)
	m.mu.RUnlock()
	for _, sub := range subscribers {
		oldCopy, _ := clonePlatformConfig(oldCfg)
		newCopy, _ := clonePlatformConfig(newCfg)
		go sub(oldCopy, newCopy)
	}
}

// persistLocked validates, atomically persists, then swaps live state. The
// caller must hold m.mu. Unrelated edits preserve existing ${ENV} references.
func (m *ConfigManager) persistLocked(cfg *PlatformConfig) error {
	persisted, err := clonePlatformConfig(cfg)
	if err != nil {
		return err
	}
	preserveEnvironmentReferences(persisted, m.persisted, m.config)
	data, err := yaml.Marshal(persisted)
	if err != nil {
		return fmt.Errorf("marshal config failed: %w", err)
	}
	raw, live, err := parsePlatformBytes(data)
	if err != nil {
		return err
	}
	return m.persistBytesLocked(data, raw, live)
}

// persistBytesLocked atomically writes already-validated config bytes and swaps
// live state. The caller must hold m.mu.
func (m *ConfigManager) persistBytesLocked(data []byte, raw, live *PlatformConfig) error {
	mode := os.FileMode(0600)
	tmp, err := os.CreateTemp(filepath.Dir(m.configPath), "."+filepath.Base(m.configPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write temp file failed: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file failed: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp file failed: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file failed: %w", err)
	}
	current, err := currentFileFingerprint(m.configPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("verify config file: %w", err)
	}
	if current != m.fingerprint {
		_ = os.Remove(tmpPath)
		return &ConflictError{Err: fmt.Errorf("config file changed outside the process")}
	}
	if err := os.Rename(tmpPath, m.configPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename config file failed: %w", err)
	}

	fingerprint := fingerprintBytes(data)
	live.FileFingerprint = fingerprint
	m.config = live
	m.persisted = raw
	m.fingerprint = fingerprint
	m.lastModified = time.Now()
	m.revision++
	return nil
}

// LastModified 获取最后修改时间
func (m *ConfigManager) LastModified() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastModified
}

// GetChannelConfig 获取指定渠道配置
func (m *ConfigManager) GetChannelConfig(name string) (*ChannelConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, ch := range m.config.Channels {
		if ch.Name == name {
			detached, _ := cloneConfigValue(reflect.ValueOf(ch))
			cloned := detached.Interface().(ChannelConfig)
			return &cloned, true
		}
	}
	return nil, false
}

// GetSourceConfig 获取指定数据源配置
func (m *ConfigManager) GetSourceConfig(name string) (*SourceConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, src := range m.config.Sources {
		if src.Name == name {
			detached, _ := cloneConfigValue(reflect.ValueOf(src))
			cloned := detached.Interface().(SourceConfig)
			return &cloned, true
		}
	}
	return nil, false
}

// AddSource 新增数据源并持久化
func (m *ConfigManager) AddSource(srcCfg SourceConfig) error {
	var err error
	srcCfg, err = NormalizeSourceConfig(srcCfg)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, src := range m.config.Sources {
		if src.Name == srcCfg.Name {
			return &ConflictError{Err: fmt.Errorf("source %s already exists", srcCfg.Name)}
		}
	}

	if srcCfg.Interval <= 0 && len(srcCfg.Schedule) == 0 {
		srcCfg.Interval = 120
	}
	if srcCfg.Enabled == nil {
		enabled := true
		srcCfg.Enabled = &enabled
	}

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	next.Sources = append(next.Sources, srcCfg)

	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// NormalizeSourceConfig applies source defaults and validates API mutations.
func NormalizeSourceConfig(srcCfg SourceConfig) (SourceConfig, error) {
	srcCfg.Routing = srcCfg.Routing.DeepCopy()
	normalizeSourceRouting(srcCfg.Routing)
	if srcCfg.Routing == nil && srcCfg.DeliveryMode == "" {
		srcCfg.DeliveryMode = DeliveryDirect
	}
	if err := validateDelivery(srcCfg); err != nil {
		return SourceConfig{}, &ValidationError{Err: err}
	}
	if srcCfg.Interval <= 0 && len(srcCfg.Schedule) == 0 {
		srcCfg.Interval = 120
	}
	return srcCfg, nil
}

// RemoveSource 删除数据源并持久化
func (m *ConfigManager) RemoveSource(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	for i, src := range next.Sources {
		if src.Name == name {
			next.Sources = append(next.Sources[:i], next.Sources[i+1:]...)
			if err := m.persistLocked(next); err != nil {
				return fmt.Errorf("save config failed: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("source %s not found", name)
}

// RemoveSourceWithRuntime persists a source deletion, applies the corresponding
// runtime mutation without holding m.mu, and restores the exact pre-delete file
// if runtime removal fails and no intervening config change occurred.
func (m *ConfigManager) RemoveSourceWithRuntime(name string, remove func(string) error) error {
	m.mu.Lock()

	originalData, err := os.ReadFile(m.configPath)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("read config snapshot failed: %w", err)
	}
	if fingerprintBytes(originalData) != m.fingerprint {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config file changed outside the process")}
	}
	originalRaw, originalLive, err := parsePlatformBytes(originalData)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("parse config snapshot failed: %w", err)
	}

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	found := false
	for i, src := range next.Sources {
		if src.Name == name {
			next.Sources = append(next.Sources[:i], next.Sources[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		m.mu.Unlock()
		return &NotFoundError{Err: fmt.Errorf("source %s not found", name)}
	}
	if err := NormalizePlatformConfig(next); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.persistLocked(next); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("save config failed: %w", err)
	}
	deleteRevision := m.revision
	deleteFingerprint := m.fingerprint
	m.mu.Unlock()

	if remove == nil {
		return nil
	}
	if runtimeErr := remove(name); runtimeErr != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.revision != deleteRevision || m.fingerprint != deleteFingerprint {
			return &SourceRuntimeMutationError{
				RuntimeErr: runtimeErr,
				RollbackErr: &ConflictError{Err: fmt.Errorf(
					"config changed while runtime source removal was in progress",
				)},
			}
		}
		rollbackErr := m.persistBytesLocked(originalData, originalRaw, originalLive)
		return &SourceRuntimeMutationError{RuntimeErr: runtimeErr, RollbackErr: rollbackErr}
	}
	return nil
}

// PreviewReload validates the on-disk configuration without changing live state.
func (m *ConfigManager) PreviewReload() (*PlatformConfig, error) {
	_, live, fingerprint, _, err := readPlatformFile(m.configPath)
	if err != nil {
		return nil, err
	}
	live.FileFingerprint = fingerprint
	return live, nil
}

// Snapshot returns a detached config and its revision for transactional callers.
func (m *ConfigManager) Snapshot() (*PlatformConfig, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, err := clonePlatformConfig(m.config)
	if cfg != nil {
		cfg.FileFingerprint = m.fingerprint
	}
	return cfg, m.revision, err
}

// CommitPreview swaps a previously validated preview if no intervening mutation occurred.
func (m *ConfigManager) CommitPreview(cfg *PlatformConfig, revision uint64) error {
	if cfg == nil {
		return &ValidationError{Err: fmt.Errorf("config is required")}
	}
	m.mu.Lock()
	if m.revision != revision {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config changed during reload")}
	}
	current, err := currentFileFingerprint(m.configPath)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("verify config file: %w", err)
	}
	if cfg.FileFingerprint == "" || current != cfg.FileFingerprint {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config file changed after reload preview")}
	}
	raw, live, fingerprint, _, err := readPlatformFile(m.configPath)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if fingerprint != current {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config file changed during reload commit")}
	}
	latest, err := currentFileFingerprint(m.configPath)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("verify config file: %w", err)
	}
	if latest != fingerprint {
		m.mu.Unlock()
		return &ConflictError{Err: fmt.Errorf("config file changed during reload commit")}
	}
	old := m.config
	live.FileFingerprint = fingerprint
	m.config = live
	m.persisted = raw
	m.fingerprint = fingerprint
	m.lastModified = time.Now()
	m.revision++
	m.mu.Unlock()
	m.notifySubscribers(old, live)
	return nil
}

// UpdateKey 根据 key ID 更新单个密钥值并持久化
// ID 格式: "llm:api_key", "source:<name>:api_key", "channel:<name>:webhook", "channel:<name>:<opt_key>"
func (m *ConfigManager) UpdateKey(id, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 2 {
		return fmt.Errorf("invalid key id: %s", id)
	}

	switch parts[0] {
	case "llm":
		if parts[1] == "api_key" {
			next.LLM.APIKey = value
		} else {
			return fmt.Errorf("unknown llm key: %s", parts[1])
		}
	case "source":
		if len(parts) != 3 || parts[2] != "api_key" {
			return fmt.Errorf("invalid source key id: %s", id)
		}
		found := false
		for i, src := range next.Sources {
			if src.Name == parts[1] {
				if next.Sources[i].Options == nil {
					next.Sources[i].Options = make(map[string]interface{})
				}
				next.Sources[i].Options["api_key"] = value
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("source not found: %s", parts[1])
		}
	case "channel":
		if len(parts) < 2 {
			return fmt.Errorf("invalid channel key id: %s", id)
		}
		found := false
		for i, ch := range next.Channels {
			if ch.Name == parts[1] {
				if len(parts) == 2 || parts[2] == "webhook" {
					next.Channels[i].Webhook = value
				} else {
					if next.Channels[i].Options == nil {
						next.Channels[i].Options = make(map[string]interface{})
					}
					next.Channels[i].Options[parts[2]] = value
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("channel not found: %s", parts[1])
		}
	default:
		return fmt.Errorf("unknown key category: %s", parts[0])
	}

	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// UpdateLLM 更新 LLM 配置并持久化
func (m *ConfigManager) UpdateLLM(llmCfg LLMConfig) error {
	m.mu.Lock()
	next, err := clonePlatformConfig(m.config)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	oldCopy := m.config
	next.LLM = llmCfg
	if err := m.persistLocked(next); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("save config failed: %w", err)
	}
	newCopy := m.config
	m.mu.Unlock()

	m.notifySubscribers(oldCopy, newCopy)
	return nil
}

// AddChannel 新增渠道并持久化
func (m *ConfigManager) AddChannel(chCfg ChannelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ch := range m.config.Channels {
		if ch.Name == chCfg.Name {
			return fmt.Errorf("channel %s already exists", chCfg.Name)
		}
	}

	if chCfg.Mode == "" {
		chCfg.Mode = "push"
	}
	if chCfg.Enabled == nil {
		enabled := true
		chCfg.Enabled = &enabled
	}

	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	next.Channels = append(next.Channels, chCfg)
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// UpdateFilters 更新过滤器配置并持久化
func (m *ConfigManager) UpdateFilters(filters FiltersConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	next.Filters = filters
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// UpdateAlert 更新告警配置并持久化
func (m *ConfigManager) UpdateAlert(alert AlertConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	next.Alert = alert
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}

// UpdateSourceHealth 更新数据源健康监控配置并持久化
func (m *ConfigManager) UpdateSourceHealth(health SourceHealthConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := clonePlatformConfig(m.config)
	if err != nil {
		return err
	}
	next.SourceHealth = health
	if err := NormalizePlatformConfig(next); err != nil {
		return err
	}
	if err := m.persistLocked(next); err != nil {
		return fmt.Errorf("save config failed: %w", err)
	}
	return nil
}
