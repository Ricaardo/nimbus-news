package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeConfig(t *testing.T) Config {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	script, err := filepath.Abs(filepath.Join("testdata", "fake_child.py"))
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Name: "fake", Command: []string{python, script}, QueueSize: 2,
		CancelGrace: 50 * time.Millisecond, KillGrace: 50 * time.Millisecond,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		CircuitFailures: 2, CircuitOpen: 100 * time.Millisecond, StderrLimit: 1024,
	}
}

func newFake(t *testing.T, mutate func(*Config)) *Supervisor {
	t.Helper()
	config := fakeConfig(t)
	if mutate != nil {
		mutate(&config)
	}
	supervisor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return supervisor
}

func TestSupervisorInitializeHealthAndNormalCall(t *testing.T) {
	supervisor := newFake(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := supervisor.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Health(ctx); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Value string `json:"value"`
	}
	if err := supervisor.Call(ctx, "echo", map[string]string{"value": "ok"}, &result); err != nil || result.Value != "ok" {
		t.Fatalf("echo = %+v, %v", result, err)
	}
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	supervisor.mu.Lock()
	child, failures, nextStart := supervisor.child, supervisor.failures, supervisor.nextStart
	supervisor.mu.Unlock()
	if child != nil || failures != 0 || !nextStart.IsZero() {
		t.Fatalf("graceful shutdown recorded crash/backoff: child=%v failures=%d next=%v", child != nil, failures, nextStart)
	}
}

func TestProductionShutdownThenCloseReapsShutdownGrandchildWithoutBackoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process group semantics")
	}
	pidFile := filepath.Join(t.TempDir(), "shutdown-grandchild.pid")
	supervisor := newFake(t, func(config *Config) {
		config.Env = map[string]string{
			"SHUTDOWN_GRANDCHILD": "ignore-term",
			"TEST_PID_FILE":       pidFile,
		}
		config.EnvAllowlist = []string{"SHUTDOWN_GRANDCHILD", "TEST_PID_FILE"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := supervisor.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	waitProcessGone(t, pid)
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	supervisor.mu.Lock()
	child, shutdownTarget := supervisor.child, supervisor.shutdownTarget
	failures, nextStart := supervisor.failures, supervisor.nextStart
	supervisor.mu.Unlock()
	if child != nil || shutdownTarget != nil || failures != 0 || !nextStart.IsZero() {
		t.Fatalf(
			"shutdown cleanup recorded crash/backoff: child=%v target=%v failures=%d next=%v",
			child != nil, shutdownTarget != nil, failures, nextStart,
		)
	}
}

func TestSupervisorBidirectionalChildRequest(t *testing.T) {
	supervisor := newFake(t, func(config *Config) {
		config.RequestHandler = func(_ context.Context, method string, params json.RawMessage) (any, *RPCError) {
			if method != "tool.approval" || !strings.Contains(string(params), "publish") {
				return nil, NewError(DomainProtocolMismatch, false, "unexpected child request")
			}
			return map[string]bool{"approved": true}, nil
		}
	})
	var result struct {
		Approved bool `json:"approved"`
	}
	if err := supervisor.Call(context.Background(), "bidirectional", map[string]any{}, &result); err != nil || !result.Approved {
		t.Fatalf("bidirectional = %+v, %v", result, err)
	}
}

func TestSupervisorTimeoutCancelsThenTerminatesHungChild(t *testing.T) {
	supervisor := newFake(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := supervisor.Call(ctx, "hang", map[string]any{}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainTimeout || !rpcErr.Data.Retryable {
		t.Fatalf("timeout error = %#v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("hung cancellation took too long: %s", time.Since(started))
	}
}

func TestSupervisorRejectsMalformedAndOversizedChildFrames(t *testing.T) {
	for _, method := range []string{"malformed", "oversize"} {
		t.Run(method, func(t *testing.T) {
			supervisor := newFake(t, nil)
			err := supervisor.Call(context.Background(), method, map[string]any{}, nil)
			var rpcErr *RPCError
			if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainProtocolMismatch {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestSupervisorRejectsOversizedOutboundRequestWithoutKillingChild(t *testing.T) {
	supervisor := newFake(t, nil)
	err := supervisor.Call(context.Background(), "echo", map[string]string{"value": strings.Repeat("x", MaxFrameBytes)}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainUnsafeRequest || rpcErr.Data.Retryable {
		t.Fatalf("outbound size error = %#v", err)
	}
	var result struct {
		Value string `json:"value"`
	}
	if err := supervisor.Call(context.Background(), "echo", map[string]string{"value": "still-alive"}, &result); err != nil || result.Value != "still-alive" {
		t.Fatalf("child after rejected request = %+v, %v", result, err)
	}
}

func TestSupervisorCrashIsNotReplayedAndBacksOff(t *testing.T) {
	supervisor := newFake(t, nil)
	err := supervisor.Call(context.Background(), "crash", map[string]any{}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainChildCrashed {
		t.Fatalf("crash error = %#v", err)
	}
	err = supervisor.Call(context.Background(), "echo", map[string]any{}, nil)
	if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainDependencyDown {
		t.Fatalf("expected explicit backoff, got %#v", err)
	}
}

func TestSupervisorCircuitOpensAfterRepeatedCrashes(t *testing.T) {
	supervisor := newFake(t, nil)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(15 * time.Millisecond)
		}
		err := supervisor.Call(context.Background(), "crash", map[string]any{}, nil)
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainChildCrashed {
			t.Fatalf("crash %d = %#v", attempt, err)
		}
	}
	time.Sleep(25 * time.Millisecond)
	err := supervisor.Call(context.Background(), "echo", map[string]any{}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainDependencyDown || !strings.Contains(err.Error(), "circuit") {
		t.Fatalf("circuit error = %#v", err)
	}
}

func TestSupervisorSlowNotificationsDoNotCorruptResponse(t *testing.T) {
	var mu sync.Mutex
	var deltas []string
	supervisor := newFake(t, func(config *Config) {
		config.NotificationHandler = func(method string, params json.RawMessage) {
			if method == "agent.text_delta" {
				var value struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal(params, &value)
				mu.Lock()
				deltas = append(deltas, value.Delta)
				mu.Unlock()
			}
		}
	})
	var result struct {
		Done bool `json:"done"`
	}
	if err := supervisor.Call(context.Background(), "slow", map[string]any{}, &result); err != nil || !result.Done {
		t.Fatalf("slow = %+v, %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(deltas, "") != "abc" {
		t.Fatalf("deltas = %q", deltas)
	}
}

func TestSupervisorQueueOverload(t *testing.T) {
	supervisor := newFake(t, func(config *Config) { config.QueueSize = 1 })
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelFirst()
	defer cancelSecond()
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- supervisor.Call(firstCtx, "hang", map[string]any{}, nil) }()
	time.Sleep(30 * time.Millisecond)
	go func() { secondDone <- supervisor.Call(secondCtx, "echo", map[string]any{}, nil) }()
	time.Sleep(20 * time.Millisecond)
	err := supervisor.Call(context.Background(), "echo", map[string]any{}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Data.Domain != DomainOverloaded || !rpcErr.Data.Retryable {
		t.Fatalf("overload error = %#v", err)
	}
	cancelFirst()
	cancelSecond()
	<-firstDone
	<-secondDone
}

func TestCancelledQueuedCallNeverStartsOrWritesToChild(t *testing.T) {
	received := filepath.Join(t.TempDir(), "received.log")
	supervisor := newFake(t, func(config *Config) {
		config.Env = map[string]string{"RECEIVED_FILE": received}
		config.EnvAllowlist = []string{"RECEIVED_FILE"}
	})
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- supervisor.Call(firstCtx, "hang", map[string]any{}, nil) }()
	waitForFileContains(t, received, "hang")

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedDone := make(chan error, 1)
	go func() { queuedDone <- supervisor.Call(queuedCtx, "must_not_run", map[string]any{}, nil) }()
	time.Sleep(10 * time.Millisecond)
	cancelQueued()
	if err := <-queuedDone; err == nil {
		t.Fatal("cancelled queued call unexpectedly succeeded")
	}
	cancelFirst()
	<-firstDone
	time.Sleep(20 * time.Millisecond)
	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must_not_run") {
		t.Fatalf("cancelled queued call reached child: %s", data)
	}
}

func TestChildRequestsAreBoundLimitedAndCancelledWithParent(t *testing.T) {
	t.Run("binding", func(t *testing.T) {
		supervisor := newFake(t, func(config *Config) {
			config.RequestHandler = func(context.Context, string, json.RawMessage) (any, *RPCError) {
				return map[string]bool{"approved": true}, nil
			}
		})
		var result struct {
			Domain string `json:"domain"`
		}
		if err := supervisor.Call(context.Background(), "bad_binding", map[string]any{}, &result); err != nil {
			t.Fatal(err)
		}
		if result.Domain != string(DomainPermissionDenied) {
			t.Fatalf("bad binding domain = %q", result.Domain)
		}
	})

	t.Run("flood", func(t *testing.T) {
		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		supervisor := newFake(t, func(config *Config) {
			config.RequestHandler = func(ctx context.Context, _ string, _ json.RawMessage) (any, *RPCError) {
				entered <- struct{}{}
				select {
				case <-release:
					return map[string]bool{"approved": true}, nil
				case <-ctx.Done():
					return nil, NewError(DomainCancelled, false, "cancelled")
				}
			}
		})
		done := make(chan struct {
			result struct {
				Domains []string `json:"domains"`
			}
			err error
		}, 1)
		go func() {
			var result struct {
				Domains []string `json:"domains"`
			}
			err := supervisor.Call(context.Background(), "reverse_flood", map[string]any{}, &result)
			done <- struct {
				result struct {
					Domains []string `json:"domains"`
				}
				err error
			}{result, err}
		}()
		<-entered
		close(release)
		out := <-done
		if out.err != nil || len(out.result.Domains) != 2 || !contains(out.result.Domains, string(DomainOverloaded)) {
			t.Fatalf("flood = %+v, %v", out.result, out.err)
		}
	})

	t.Run("parent cancellation", func(t *testing.T) {
		var active atomic.Int32
		entered := make(chan struct{}, 1)
		supervisor := newFake(t, func(config *Config) {
			config.RequestHandler = func(ctx context.Context, _ string, _ json.RawMessage) (any, *RPCError) {
				active.Add(1)
				defer active.Add(-1)
				entered <- struct{}{}
				<-ctx.Done()
				return nil, NewError(DomainCancelled, false, "cancelled")
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- supervisor.Call(ctx, "reverse_hang", map[string]any{}, nil) }()
		<-entered
		cancel()
		<-done
		deadline := time.Now().Add(time.Second)
		for active.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if active.Load() != 0 {
			t.Fatal("child request handler remained after parent cancellation")
		}
	})

	t.Run("handler timeout", func(t *testing.T) {
		var timedOut atomic.Bool
		supervisor := newFake(t, func(config *Config) {
			config.RequestTimeout = 20 * time.Millisecond
			config.RequestHandler = func(ctx context.Context, _ string, _ json.RawMessage) (any, *RPCError) {
				<-ctx.Done()
				timedOut.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
				return nil, NewError(DomainTimeout, true, "timeout")
			}
		})
		var result struct {
			Approved bool `json:"approved"`
		}
		if err := supervisor.Call(context.Background(), "bidirectional", map[string]any{}, &result); err != nil {
			t.Fatal(err)
		}
		if !timedOut.Load() || result.Approved {
			t.Fatalf("handler timeout not enforced: timedOut=%v result=%+v", timedOut.Load(), result)
		}
	})
}

func TestCloseTerminatesEntireProcessGroupAndEscalates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process group semantics")
	}
	t.Run("grandchild", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
		supervisor := newFake(t, func(config *Config) {
			config.Env = map[string]string{"TEST_PID_FILE": pidFile}
			config.EnvAllowlist = []string{"TEST_PID_FILE"}
		})
		var result struct {
			PID int `json:"pid"`
		}
		if err := supervisor.Call(context.Background(), "spawn_grandchild", map[string]any{}, &result); err != nil {
			t.Fatal(err)
		}
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
		waitProcessGone(t, result.PID)
	})

	t.Run("direct child exits on TERM but grandchild ignores it", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "grandchild-ignore-term.pid")
		supervisor := newFake(t, func(config *Config) {
			config.Env = map[string]string{"TEST_PID_FILE": pidFile}
			config.EnvAllowlist = []string{"TEST_PID_FILE"}
		})
		var result struct {
			PID int `json:"pid"`
		}
		if err := supervisor.Call(context.Background(), "spawn_grandchild_ignore_term", map[string]any{}, &result); err != nil {
			t.Fatal(err)
		}
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
		waitProcessGone(t, result.PID)
	})

	t.Run("ignored TERM escalates to KILL", func(t *testing.T) {
		termFile := filepath.Join(t.TempDir(), "term.marker")
		supervisor := newFake(t, func(config *Config) {
			config.Env = map[string]string{"IGNORE_TERM": "1", "TERM_FILE": termFile}
			config.EnvAllowlist = []string{"IGNORE_TERM", "TERM_FILE"}
		})
		if err := supervisor.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		supervisor.mu.Lock()
		pid := supervisor.child.cmd.Process.Pid
		supervisor.mu.Unlock()
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(termFile); err != nil {
			t.Fatalf("SIGTERM handler was not reached before escalation: %v", err)
		}
		waitProcessGone(t, pid)
	})
}

func TestSupervisorRedactsStderrAndSanitizesChildEnvironment(t *testing.T) {
	for _, item := range []struct{ key, value string }{
		{"ANTHROPIC_API_KEY", "anthropic-canary"}, {"SERVICE_TOKEN", "token-canary"},
		{"DB_PASSWORD", "password-canary"}, {"CLIENT_SECRET", "secret-canary"},
		{"SERVICE_BASE_URL", "https://private.invalid"}, {"AWS_CREDENTIAL", "credential-canary"},
	} {
		t.Setenv(item.key, item.value)
	}
	supervisor := newFake(t, func(config *Config) {
		config.SecretValues = []string{"known-canary"}
		config.Env = map[string]string{"SAFE_RUNTIME_MODE": "candidate"}
		config.EnvAllowlist = []string{"SAFE_RUNTIME_MODE"}
	})
	var stderrResult struct {
		Done bool `json:"done"`
	}
	if err := supervisor.Call(context.Background(), "stderr", map[string]any{}, &stderrResult); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(supervisor.StderrTail(), "[REDACTED]") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	tail := supervisor.StderrTail()
	for _, secret := range []string{"canary-bearer", "canary-api", "known-canary", "must-not-pass"} {
		if strings.Contains(tail, secret) {
			t.Fatalf("stderr leaked %q: %s", secret, tail)
		}
	}
	var envResult struct {
		Sensitive   []string `json:"sensitive"`
		HomePresent bool     `json:"home_present"`
		PathPresent bool     `json:"path_present"`
		SafeValue   string   `json:"safe_value"`
	}
	if err := supervisor.Call(context.Background(), "env", map[string]any{}, &envResult); err != nil {
		t.Fatal(err)
	}
	if len(envResult.Sensitive) != 0 || !envResult.HomePresent || !envResult.PathPresent || envResult.SafeValue != "candidate" {
		t.Fatalf("child environment = %+v", envResult)
	}
	config := fakeConfig(t)
	config.Env = map[string]string{"OTHER_TOKEN": "forbidden"}
	config.EnvAllowlist = []string{"OTHER_TOKEN"}
	if _, err := New(config); err == nil {
		t.Fatal("explicit sensitive runtime env was accepted")
	}
}

func TestStderrDrainAndProtocolErrorsAreRedactedAndBounded(t *testing.T) {
	supervisor := newFake(t, nil)
	var result struct {
		Done bool `json:"done"`
	}
	if err := supervisor.Call(context.Background(), "stderr_huge", map[string]any{}, &result); err != nil || !result.Done {
		t.Fatalf("huge stderr = %+v, %v", result, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(supervisor.StderrTail(), "after-drain-canary") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tail := supervisor.StderrTail(); !strings.Contains(tail, "after-drain-canary") || len(tail) > supervisor.config.StderrLimit {
		t.Fatalf("stderr tail not drained/bounded: len=%d tail=%q", len(tail), tail)
	}
	err := supervisor.Call(context.Background(), "error_canary", map[string]any{}, nil)
	if err == nil {
		t.Fatal("canary error unexpectedly succeeded")
	}
	message := err.Error()
	for _, secret := range []string{"token-canary", "alice:password", os.Getenv("HOME")} {
		if secret != "" && strings.Contains(message, secret) {
			t.Fatalf("protocol error leaked %q: %s", secret, message)
		}
	}
	if len([]rune(message)) > 1100 {
		t.Fatalf("protocol error not bounded: %d", len([]rune(message)))
	}
}

func waitForFileContains(t *testing.T, path, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), needle) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not contain %q", path, needle)
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %s still exists", strconv.Itoa(pid))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
