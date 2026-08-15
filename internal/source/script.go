package source

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"os/exec"
	"strings"
	"time"
)

// defaultScriptTimeout 脚本执行默认超时时间，防止挂起的脚本永久阻塞抓取协程
const defaultScriptTimeout = 120 * time.Second

// maxEmbeddedOutput 错误信息中内嵌的脚本输出上限，避免日志炸弹
const maxEmbeddedOutput = 2 * 1024

func init() {
	Register("script", NewScriptSource)
}

// ScriptSource 脚本执行源，运行外部命令
type ScriptSource struct {
	name       string
	command    string
	args       []string
	workDir    string
	pushOutput bool
	timeout    time.Duration
}

// NewScriptSource 创建脚本源
func NewScriptSource(cfg Config) (Source, error) {
	command := ""
	if cmd, ok := cfg.Options["command"].(string); ok {
		command = cmd
	}
	if command == "" {
		return nil, fmt.Errorf("script: command is required")
	}

	// 解析命令和参数
	parts := strings.Fields(command)

	workDir := ""
	if dir, ok := cfg.Options["work_dir"].(string); ok {
		workDir = dir
	}

	pushOutput := true
	if v, ok := cfg.Options["push_output"].(bool); ok {
		pushOutput = v
	}

	timeout := defaultScriptTimeout
	if v, ok := cfg.Options["timeout"].(int); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}

	return &ScriptSource{
		name:       cfg.Name,
		command:    parts[0],
		args:       parts[1:],
		workDir:    workDir,
		pushOutput: pushOutput,
		timeout:    timeout,
	}, nil
}

func (s *ScriptSource) Name() string { return s.name }
func (s *ScriptSource) Type() string { return "script" }

// Fetch 执行脚本并返回结果
func (s *ScriptSource) Fetch() ([]*model.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.command, s.args...)
	if s.workDir != "" {
		cmd.Dir = s.workDir
	}

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)
	pushed := false
	defer func() {
		slog.Info("script executed", "source", s.name, "duration", duration, "output_bytes", len(output), "pushed", pushed)
	}()

	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("script %s timed out after %s: %w", s.name, s.timeout, ctx.Err())
		}
		return nil, fmt.Errorf("script %s failed: %w\noutput: %s", s.name, err, truncateOutput(output))
	}

	if !s.pushOutput {
		return nil, nil
	}

	outStr := strings.TrimSpace(string(output))
	if outStr == "" {
		return nil, nil
	}

	// CombinedOutput 会把 stderr 诊断行(如 "[blockbeats] AI failed, ...")混入输出流。
	// 跳过以 "[" 开头的诊断行,避免诊断文本成为报告标题。
	if strings.HasPrefix(outStr, "[") {
		if rawLines := strings.Split(outStr, "\n"); len(rawLines) > 0 {
			for len(rawLines) > 0 && strings.HasPrefix(strings.TrimSpace(rawLines[0]), "[") {
				rawLines = rawLines[1:]
			}
			outStr = strings.TrimSpace(strings.Join(rawLines, "\n"))
			if outStr == "" {
				return nil, nil
			}
		}
	}

	// 报告正文首行即标题(脚本均输出 "🏛 机构 13F …" 之类的中文表头)，
	// 余下为正文。避免渲染成丑陋的 "Script: edgar-13f" 英文占位标题。
	title, content := s.name, outStr
	if idx := strings.IndexByte(outStr, '\n'); idx >= 0 {
		if first := strings.TrimSpace(outStr[:idx]); first != "" {
			title = first
			content = strings.TrimSpace(outStr[idx+1:])
		}
	} else {
		title = outStr // 单行报告：整条即标题，正文留空
		content = ""
	}

	// ShortContent: 首行作为微信卡片摘要
	shortContent := title
	if len(content) > 80 {
		shortContent = content[:80]
	} else if content != "" {
		shortContent = title + " | " + content
	}

	msg := &model.Message{
		Type:         model.TypeNews,
		ID:           fmt.Sprintf("script_%s_%s", s.name, sha1Hex(outStr)[:12]),
		Title:        title,
		Content:      content,
		ShortContent: shortContent,
		Source:       s.name,
		SourceType:   "script",
		CreateTime:   time.Now(),
		FetchTime:    time.Now(),
		Tags:         []string{"script"},
	}

	pushed = true
	return []*model.Message{msg}, nil
}

// sha1Hex 返回字符串的十六进制 SHA-1 摘要，用于生成基于内容的稳定消息 ID
func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// truncateOutput 截断嵌入错误信息中的脚本输出，避免日志炸弹
func truncateOutput(output []byte) string {
	if len(output) <= maxEmbeddedOutput {
		return string(output)
	}
	return string(output[:maxEmbeddedOutput]) + "...(truncated)"
}
