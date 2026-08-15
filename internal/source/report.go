package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
)

// ScriptSpec 单个 Python 脚本的执行说明
type ScriptSpec struct {
	Key        string        // 输出 map 里的 key，供 template 查询
	Cmd        string        // 通常是 "python3"
	Args       []string      // 脚本路径 + CLI 参数
	Timeout    time.Duration // 默认 60s
	Optional   bool          // 失败是否允许（默认 true — 不阻断整个报告）
	SourceName string        // 调用方源名 (供 metrics 使用, 可选)
}

// ScriptResult 单个脚本执行结果
type ScriptResult struct {
	Key     string
	Raw     []byte          // 原始 stdout
	Parsed  json.RawMessage // 尝试 JSON 解析后的结果
	Err     error           // 执行错误（超时/非零退出/JSON 解析失败等）
	Latency time.Duration
}

// RunScripts 并发执行多个 Python 脚本, 单个失败不阻断其他
// 返回 map[key]ScriptResult; 调用方负责根据 result.Err 判断是否可用
func RunScripts(ctx context.Context, specs []ScriptSpec) map[string]*ScriptResult {
	results := make(map[string]*ScriptResult, len(specs))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, spec := range specs {
		wg.Add(1)
		go func(s ScriptSpec) {
			defer wg.Done()

			timeout := s.Timeout
			if timeout <= 0 {
				timeout = 60 * time.Second
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := time.Now()
			cmd := exec.CommandContext(runCtx, s.Cmd, s.Args...)
			stdout, err := cmd.Output()
			latency := time.Since(start)

			res := &ScriptResult{Key: s.Key, Raw: stdout, Latency: latency}
			status := "success"
			if err != nil {
				res.Err = fmt.Errorf("exec %s: %w", s.Key, err)
				if runCtx.Err() == context.DeadlineExceeded {
					status = "timeout"
				} else {
					status = "error"
				}
			} else if len(stdout) > 0 {
				// 尝试 JSON 解析；若失败 Raw 仍可用
				var raw json.RawMessage
				if jerr := json.Unmarshal(stdout, &raw); jerr == nil {
					res.Parsed = raw
				} else {
					res.Err = fmt.Errorf("parse json %s: %w", s.Key, jerr)
					status = "error"
				}
			}

			// metrics
			srcName := s.SourceName
			if srcName == "" {
				srcName = s.Key
			}
			metrics.ReportScriptTotal.WithLabelValues(srcName, s.Key, status).Inc()
			metrics.ReportScriptDuration.WithLabelValues(srcName, s.Key).Observe(latency.Seconds())

			mu.Lock()
			results[s.Key] = res
			mu.Unlock()
		}(spec)
	}

	wg.Wait()
	return results
}

// GetJSON 从结果 map 取指定 key 的 JSON, 到目标结构; 失败返回 error
func GetJSON(results map[string]*ScriptResult, key string, dst interface{}) error {
	r, ok := results[key]
	if !ok {
		return fmt.Errorf("key %s not in results", key)
	}
	if r.Err != nil {
		return r.Err
	}
	if len(r.Parsed) == 0 {
		return fmt.Errorf("key %s: empty parsed", key)
	}
	return json.Unmarshal(r.Parsed, dst)
}

// HasOK 判断某脚本是否成功执行并产出数据
func HasOK(results map[string]*ScriptResult, key string) bool {
	r, ok := results[key]
	return ok && r.Err == nil && len(r.Parsed) > 0
}

// SetSpecsSourceName 批量给 specs 填 SourceName (用于 metrics label)
func SetSpecsSourceName(specs []ScriptSpec, sourceName string) []ScriptSpec {
	for i := range specs {
		specs[i].SourceName = sourceName
	}
	return specs
}
