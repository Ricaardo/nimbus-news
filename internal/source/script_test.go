package source

import (
	"testing"
	"time"
)

// TestScriptSourceSkipsStderrDiagnosticLines 验证 CombinedOutput 混入的
// stderr 诊断行(以 "[" 开头)不会成为报告标题(如 blockbeats AI 失败提示)。
func TestScriptSourceSkipsStderrDiagnosticLines(t *testing.T) {
	src := &ScriptSource{
		name:       "test-script",
		command:    "sh",
		args:       []string{"-c", `printf "[blockbeats] AI failed, falling back to raw format\n"; printf "🏛 测试报告标题\n"; printf "正文行一\n正文行二\n"`},
		timeout:    10 * time.Second,
		pushOutput: true,
	}
	msgs, err := src.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	got := msgs[0]
	if got.Title != "🏛 测试报告标题" {
		t.Errorf("Title = %q, want %q", got.Title, "🏛 测试报告标题")
	}
	if got.Content != "正文行一\n正文行二" {
		t.Errorf("Content = %q", got.Content)
	}
}

// TestScriptSourceNormalTitle 正常输出(无诊断行)标题提取不受影响。
func TestScriptSourceNormalTitle(t *testing.T) {
	src := &ScriptSource{
		name:       "test-script",
		command:    "sh",
		args:       []string{"-c", `printf "🌆 晚报标题\n"; printf "内容\n"`},
		timeout:    10 * time.Second,
		pushOutput: true,
	}
	msgs, err := src.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Title != "🌆 晚报标题" {
		t.Fatalf("Title = %q, want %q", msgs[0].Title, "🌆 晚报标题")
	}
}

// TestScriptSourceDiagnosticOnly 只有诊断行无正文 → 不发消息。
func TestScriptSourceDiagnosticOnly(t *testing.T) {
	src := &ScriptSource{
		name:       "test-script",
		command:    "sh",
		args:       []string{"-c", `printf "[blockbeats] LLM not configured\n"`},
		timeout:    10 * time.Second,
		pushOutput: true,
	}
	msgs, err := src.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("messages = %d, want 0", len(msgs))
	}
}
