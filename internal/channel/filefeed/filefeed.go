// Package filefeed 实现一个把推送消息追加为 JSONL 的本地文件渠道，
// 作为 news → nimbus 的数据桥（nimbus 的 news-bridge skill 读取该文件）。
package filefeed

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

const (
	SnapshotKindV1                 = "news_feed_jsonl_v1"
	fileFeedSnapshotMaxLineBytes   = 4 * 1024 * 1024
	fileFeedSnapshotMaxTotalBytes  = int64(256 * 1024 * 1024)
	fileFeedSnapshotTransferBuffer = 32 * 1024
)

func init() {
	channel.Register("filefeed", NewFileFeedChannel)
}

// FileFeedChannel 把每条推送消息以 JSON 行追加到文件
type FileFeedChannel struct {
	channel.BaseChannel
	path    string
	v2Path  string
	maxLine int // 文件最多保留多少行（滚动），0=不限制
	mu      sync.Mutex
}

type feedLine struct {
	TS      string   `json:"ts"`
	Epoch   int64    `json:"epoch"`
	Source  string   `json:"source"`
	Title   string   `json:"title,omitempty"`
	Zh      string   `json:"zh,omitempty"` // 中文译文/简评（AIComment）
	Tickers []string `json:"tickers,omitempty"`
	Impact  string   `json:"impact,omitempty"`
	Link    string   `json:"link,omitempty"`
}

type feedLineV2 struct {
	Version    int                    `json:"version"`
	EventID    string                 `json:"event_id"`
	TS         string                 `json:"ts"`
	Epoch      int64                  `json:"epoch,omitempty"`
	Source     string                 `json:"source"`
	SourceID   string                 `json:"source_id,omitempty"`
	Title      string                 `json:"title"`
	SummaryZh  string                 `json:"summary_zh,omitempty"`
	Symbols    []string               `json:"symbols,omitempty"`
	Impact     string                 `json:"impact,omitempty"`
	Link       string                 `json:"link,omitempty"`
	Provenance map[string]string      `json:"provenance,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// NewFileFeedChannel 创建文件 feed 渠道
func NewFileFeedChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModePush
	}
	c := &FileFeedChannel{
		BaseChannel: channel.NewBaseChannel(cfg.Name, "filefeed", mode),
		maxLine:     2000,
	}
	if cfg.Options != nil {
		if p, ok := cfg.Options["path"].(string); ok && p != "" {
			c.path = expandHome(p)
		}
		if p, ok := cfg.Options["v2_path"].(string); ok && p != "" {
			c.v2Path = expandHome(p)
		}
		if m, ok := cfg.Options["max_lines"].(int); ok && m > 0 {
			c.maxLine = m
		}
	}
	if c.path == "" {
		return nil, fmt.Errorf("filefeed: path is required")
	}
	return c, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (c *FileFeedChannel) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	if c.v2Path != "" {
		return os.MkdirAll(filepath.Dir(c.v2Path), 0o755)
	}
	return nil
}
func (c *FileFeedChannel) Stop() error { return nil }

// Send 追加一行 JSON
func (c *FileFeedChannel) Send(ctx context.Context, msg *model.Message) error {
	if !c.CanSend() {
		return fmt.Errorf("channel %s cannot send", c.Name())
	}
	src := msg.GetStringMetadata("display_source")
	if src == "" {
		src = msg.Source
	}
	impact := msg.GetStringMetadata("economic_impact")
	line := feedLine{
		TS:      msg.CreateTime.Format("2006-01-02T15:04:05Z07:00"),
		Epoch:   msg.CreateTime.Unix(),
		Source:  src,
		Title:   msg.Title,
		Zh:      msg.AIComment(),
		Tickers: msg.Tags,
		Impact:  impact,
		Link:    msg.Link,
	}
	if line.Epoch <= 0 {
		line.Epoch = time.Now().Unix()
		line.TS = time.Now().Format("2006-01-02T15:04:05Z07:00")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := appendJSONLine(c.path, line); err != nil {
		return err
	}
	if err := c.rollFile(c.path); err != nil {
		return fmt.Errorf("filefeed: roll v1: %w", err)
	}

	if c.v2Path != "" {
		lineV2 := buildV2Line(msg, line)
		if err := appendJSONLine(c.v2Path, lineV2); err != nil {
			return fmt.Errorf("filefeed: append v2: %w", err)
		}
		if err := c.rollFile(c.v2Path); err != nil {
			return fmt.Errorf("filefeed: roll v2: %w", err)
		}
	}
	return nil
}

// Snapshot captures the required legacy v1 feed under the append/roll lock,
// then validates and writes that immutable capture without holding the lock.
// The optional v2 feed is intentionally not part of this API.
func (c *FileFeedChannel) Snapshot(ctx context.Context, w io.Writer) (store.SnapshotResult, error) {
	if ctx == nil {
		return store.SnapshotResult{}, fmt.Errorf("filefeed snapshot: context is required")
	}
	if w == nil {
		return store.SnapshotResult{}, fmt.Errorf("filefeed snapshot: writer is required")
	}
	if err := ctx.Err(); err != nil {
		return store.SnapshotResult{}, err
	}

	file, size, err := c.captureSnapshotFile(ctx)
	if err != nil {
		return store.SnapshotResult{}, err
	}
	defer file.Close()
	if err := validateSnapshot(ctx, io.NewSectionReader(file, 0, size)); err != nil {
		return store.SnapshotResult{}, err
	}
	return writeSnapshot(ctx, w, io.NewSectionReader(file, 0, size), size)
}

func (c *FileFeedChannel) captureSnapshotFile(ctx context.Context) (*os.File, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	source, err := os.Open(c.path)
	if os.IsNotExist(err) {
		return nil, 0, fmt.Errorf("filefeed snapshot: required v1 feed does not exist")
	}
	if err != nil {
		return nil, 0, fmt.Errorf("filefeed snapshot: open v1: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("filefeed snapshot: stat v1: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("filefeed snapshot: required v1 feed is not regular")
	}
	size := info.Size()
	if size > fileFeedSnapshotMaxTotalBytes {
		return nil, 0, fmt.Errorf("filefeed snapshot: total exceeds %d bytes", fileFeedSnapshotMaxTotalBytes)
	}

	temp, err := os.CreateTemp(filepath.Dir(c.path), "."+filepath.Base(c.path)+".snapshot-*")
	if err != nil {
		return nil, 0, fmt.Errorf("filefeed snapshot: create capture: %w", err)
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return nil, 0, fmt.Errorf("filefeed snapshot: secure capture: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		temp.Close()
		return nil, 0, fmt.Errorf("filefeed snapshot: unlink capture: %w", err)
	}
	removeTemp = false
	if err := captureSnapshot(ctx, io.NewSectionReader(source, 0, size), temp, size); err != nil {
		temp.Close()
		return nil, 0, err
	}
	return temp, size, nil
}

func captureSnapshot(ctx context.Context, source io.Reader, destination io.Writer, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	written, err := io.CopyBuffer(destination, &fileFeedContextReader{ctx: ctx, reader: source}, make([]byte, fileFeedSnapshotTransferBuffer))
	if err != nil {
		return fmt.Errorf("filefeed snapshot: capture: %w", err)
	}
	if written != size {
		return fmt.Errorf("filefeed snapshot: capture: %w", io.ErrUnexpectedEOF)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateSnapshot(ctx context.Context, r io.Reader) error {
	reader := bufio.NewReaderSize(&fileFeedContextReader{ctx: ctx, reader: r}, fileFeedSnapshotMaxLineBytes+1)
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return fmt.Errorf("filefeed snapshot: v1 line %d exceeds %d bytes", lineNumber, fileFeedSnapshotMaxLineBytes)
		}
		if errors.Is(err, io.EOF) {
			if len(record) != 0 {
				return fmt.Errorf("filefeed snapshot: incomplete final record")
			}
			break
		}
		if err != nil {
			return fmt.Errorf("filefeed snapshot: read v1 line %d: %w", lineNumber, err)
		}
		line := record[:len(record)-1]
		if len(line) > fileFeedSnapshotMaxLineBytes {
			return fmt.Errorf("filefeed snapshot: v1 line %d exceeds %d bytes", lineNumber, fileFeedSnapshotMaxLineBytes)
		}
		var envelope *feedLine
		if len(line) == 0 || json.Unmarshal(line, &envelope) != nil || envelope == nil {
			return fmt.Errorf("filefeed snapshot: decode v1 object line %d", lineNumber)
		}
		if strings.TrimSpace(envelope.TS) == "" {
			return fmt.Errorf("filefeed snapshot: v1 line %d missing ts", lineNumber)
		}
		parsedTS, err := time.Parse(time.RFC3339, envelope.TS)
		if err != nil {
			return fmt.Errorf("filefeed snapshot: v1 line %d invalid ts", lineNumber)
		}
		if envelope.Epoch <= 0 {
			return fmt.Errorf("filefeed snapshot: v1 line %d invalid epoch", lineNumber)
		}
		if parsedTS.Unix() != envelope.Epoch {
			return fmt.Errorf("filefeed snapshot: v1 line %d timestamp mismatch", lineNumber)
		}
		if strings.TrimSpace(envelope.Source) == "" {
			return fmt.Errorf("filefeed snapshot: v1 line %d missing source", lineNumber)
		}
		if strings.TrimSpace(envelope.Title) == "" && strings.TrimSpace(envelope.Zh) == "" {
			return fmt.Errorf("filefeed snapshot: v1 line %d missing title or zh", lineNumber)
		}
	}
	return nil
}

func writeSnapshot(ctx context.Context, w io.Writer, r io.Reader, size int64) (store.SnapshotResult, error) {
	hash := sha256.New()
	written := int64(0)
	buffer := make([]byte, fileFeedSnapshotTransferBuffer)
	for written < size {
		if err := ctx.Err(); err != nil {
			return store.SnapshotResult{}, err
		}
		next := int64(len(buffer))
		if remaining := size - written; remaining < next {
			next = remaining
		}
		n, err := io.ReadFull(&fileFeedContextReader{ctx: ctx, reader: r}, buffer[:next])
		if err != nil {
			return store.SnapshotResult{}, fmt.Errorf("filefeed snapshot: read output: %w", err)
		}
		chunk := buffer[:n]
		n, err = w.Write(chunk)
		if n < 0 || n > len(chunk) {
			return store.SnapshotResult{}, fmt.Errorf("filefeed snapshot: invalid write count %d", n)
		}
		if n > 0 {
			_, _ = hash.Write(chunk[:n])
			written += int64(n)
		}
		if err != nil {
			return store.SnapshotResult{}, fmt.Errorf("filefeed snapshot: write: %w", err)
		}
		if n != len(chunk) {
			return store.SnapshotResult{}, fmt.Errorf("filefeed snapshot: write: %w", io.ErrShortWrite)
		}
	}
	if err := ctx.Err(); err != nil {
		return store.SnapshotResult{}, err
	}
	return store.SnapshotResult{Kind: SnapshotKindV1, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type fileFeedContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *fileFeedContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func appendJSONLine(path string, line interface{}) error {
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func buildV2Line(msg *model.Message, line feedLine) feedLineV2 {
	sourceID := msg.GetStringMetadata("source_id")
	if sourceID == "" {
		sourceID = msg.Link
	}
	provenance := map[string]string{}
	if msg.Link != "" {
		provenance["raw_url"] = msg.Link
	}
	provenance["retrieved_at"] = time.Now().Format("2006-01-02T15:04:05Z07:00")
	return feedLineV2{
		Version:    2,
		EventID:    makeEventID(line.Source, sourceID, line.Title, line.TS),
		TS:         line.TS,
		Epoch:      line.Epoch,
		Source:     line.Source,
		SourceID:   sourceID,
		Title:      line.Title,
		SummaryZh:  line.Zh,
		Symbols:    line.Tickers,
		Impact:     line.Impact,
		Link:       line.Link,
		Provenance: provenance,
	}
}

func makeEventID(source, sourceID, title, ts string) string {
	key := strings.Join([]string{source, sourceID, title, ts}, "|")
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("news:%s:%s", sanitizeIDPart(source), hex.EncodeToString(sum[:])[:16])
}

func sanitizeIDPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == '.' || r == ' ' || r == '/':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}

// rollFile 超出 maxLine 时裁到后半部分（保留最新）。
// WriteFile truncates the existing inode, preserving its mode, ACLs and xattrs.
func (c *FileFeedChannel) rollFile(path string) error {
	if c.maxLine <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= c.maxLine {
		return nil
	}
	keep := lines[len(lines)-c.maxLine:]
	return os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}

func (c *FileFeedChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	for _, m := range msgs {
		if err := c.Send(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// Reply 文件渠道不支持回复，no-op
func (c *FileFeedChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	return c.Send(ctx, reply)
}
