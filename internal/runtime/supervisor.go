package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type RequestHandler func(context.Context, string, json.RawMessage) (any, *RPCError)
type NotificationHandler func(string, json.RawMessage)

var errFrameTooLarge = errors.New("runtime frame too large")

type Config struct {
	Name                string
	Command             []string
	Env                 map[string]string
	EnvAllowlist        []string
	QueueSize           int
	CancelGrace         time.Duration
	KillGrace           time.Duration
	RequestTimeout      time.Duration
	BackoffBase         time.Duration
	BackoffMax          time.Duration
	CircuitFailures     int
	CircuitOpen         time.Duration
	StderrLimit         int
	StderrLineLimit     int
	SecretValues        []string
	RequestHandler      RequestHandler
	NotificationHandler NotificationHandler
}

type call struct {
	ctx    context.Context
	method string
	params any
	result chan callResult
	state  atomic.Uint32
}

const (
	callQueued uint32 = iota
	callExecuting
	callCancelled
	callFinished
)

type callResult struct {
	value json.RawMessage
	err   error
}

type childProcess struct {
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	done            chan error
	pending         map[string]chan callResult
	mu              sync.Mutex
	writeMu         sync.Mutex
	expected        atomic.Bool
	stopping        atomic.Bool
	generation      uint64
	lifecycle       context.Context
	cancelLifecycle context.CancelFunc
	activeParentID  string
	activeParentCtx context.Context
	reverseActive   atomic.Bool
	processGroupID  int
}

type Supervisor struct {
	config Config
	queue  chan *call
	stop   chan struct{}
	done   chan struct{}

	mu             sync.Mutex
	child          *childProcess
	shutdownTarget *childProcess
	failures       int
	nextStart      time.Time
	circuitOpen    time.Time
	stderr         []byte
	closed         bool
	nextID         atomic.Uint64
	nextGeneration atomic.Uint64
}

// ParseCommand accepts only a JSON string array. It never invokes a shell, so
// opt-in candidate flags cannot gain shell expansion or command substitution.
func ParseCommand(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var command []string
	if err := strictUnmarshal([]byte(value), &command); err != nil {
		return nil, fmt.Errorf("runtime command must be a JSON string array: %w", err)
	}
	if len(command) == 0 {
		return nil, errors.New("runtime command must not be empty")
	}
	for _, part := range command {
		if strings.TrimSpace(part) == "" || strings.IndexByte(part, 0) >= 0 {
			return nil, errors.New("runtime command contains an empty or invalid argument")
		}
	}
	return command, nil
}

func New(config Config) (*Supervisor, error) {
	if len(config.Command) == 0 || strings.TrimSpace(config.Command[0]) == "" {
		return nil, errors.New("runtime command is required")
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 8
	}
	if config.CancelGrace <= 0 {
		config.CancelGrace = 250 * time.Millisecond
	}
	if config.KillGrace <= 0 {
		config.KillGrace = 250 * time.Millisecond
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.BackoffBase <= 0 {
		config.BackoffBase = 100 * time.Millisecond
	}
	if config.BackoffMax <= 0 {
		config.BackoffMax = 5 * time.Second
	}
	if config.CircuitFailures <= 0 {
		config.CircuitFailures = 3
	}
	if config.CircuitOpen <= 0 {
		config.CircuitOpen = 10 * time.Second
	}
	if config.StderrLimit <= 0 {
		config.StderrLimit = 32 * 1024
	}
	if config.StderrLineLimit <= 0 {
		config.StderrLineLimit = 256 * 1024
	}
	allowedEnv := make(map[string]struct{}, len(config.EnvAllowlist))
	for _, key := range config.EnvAllowlist {
		allowedEnv[key] = struct{}{}
	}
	for key := range config.Env {
		if !validEnvName(key) || sensitiveEnvKey(key) {
			return nil, fmt.Errorf("runtime env key %q is forbidden", key)
		}
		if _, ok := allowedEnv[key]; !ok {
			return nil, fmt.Errorf("runtime env key %q is not explicitly allowlisted", key)
		}
	}
	s := &Supervisor{config: config, queue: make(chan *call, config.QueueSize), stop: make(chan struct{}), done: make(chan struct{})}
	go s.worker()
	return s, nil
}

// Call enqueues one request. Calls are serialized (max_in_flight=1); a full
// queue fails immediately with overloaded rather than growing without bound.
func (s *Supervisor) Call(ctx context.Context, method string, params any, target any) error {
	if ctx == nil {
		return NewError(DomainUnsafeRequest, false, "context is required")
	}
	request := &call{ctx: ctx, method: method, params: params, result: make(chan callResult, 1)}
	select {
	case <-s.stop:
		return NewError(DomainDependencyDown, true, "runtime is closed")
	case s.queue <- request:
	default:
		return NewError(DomainOverloaded, true, "runtime queue is full")
	}
	select {
	case result := <-request.result:
		if result.err != nil {
			return result.err
		}
		if target == nil {
			return nil
		}
		if err := strictUnmarshal(result.value, target); err != nil {
			return NewError(DomainProtocolMismatch, false, "decode result: "+err.Error())
		}
		return nil
	case <-ctx.Done():
		request.state.CompareAndSwap(callQueued, callCancelled)
		return contextDomainError(ctx.Err())
	case <-s.stop:
		return NewError(DomainDependencyDown, true, "runtime is closed")
	}
}

func (s *Supervisor) Health(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := s.Call(ctx, "health", map[string]any{}, &result); err != nil {
		return err
	}
	if result.Status != "ok" && result.Status != "degraded" {
		return NewError(DomainProtocolMismatch, false, "invalid health status")
	}
	return nil
}

func (s *Supervisor) Initialize(ctx context.Context) error {
	var result struct {
		Version      int             `json:"version"`
		Capabilities json.RawMessage `json:"capabilities,omitempty"`
	}
	if err := s.Call(ctx, "initialize", map[string]any{"version": ProtocolVersion, "client": "nimbusd"}, &result); err != nil {
		return err
	}
	if result.Version != ProtocolVersion {
		return NewError(DomainProtocolMismatch, false, fmt.Sprintf("child protocol version %d", result.Version))
	}
	return nil
}

func (s *Supervisor) Shutdown(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := s.Call(ctx, "shutdown", map[string]any{}, &result); err != nil {
		return err
	}
	if result.Status != "ok" {
		return NewError(DomainProtocolMismatch, false, "invalid shutdown status")
	}
	s.mu.Lock()
	child := s.shutdownTarget
	s.mu.Unlock()
	if child != nil {
		select {
		case <-child.done:
		case <-ctx.Done():
		}
		// A successful protocol response only proves that the direct child
		// acknowledged shutdown. Retain its PGID and clean the whole group even
		// when waitChild has already removed the direct child from s.child.
		s.terminate(child)
		s.mu.Lock()
		if s.shutdownTarget == child {
			s.shutdownTarget = nil
		}
		s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return contextDomainError(err)
		}
	}
	return nil
}

func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	child := s.child
	shutdownTarget := s.shutdownTarget
	s.mu.Unlock()
	if child != nil {
		s.terminate(child)
	}
	if shutdownTarget != nil && shutdownTarget != child {
		s.terminate(shutdownTarget)
	}
	<-s.done
	return nil
}

func (s *Supervisor) StderrTail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(append([]byte(nil), s.stderr...))
}

func (s *Supervisor) worker() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case request := <-s.queue:
			if !request.state.CompareAndSwap(callQueued, callExecuting) {
				request.result <- callResult{err: contextDomainError(request.ctx.Err())}
				continue
			}
			if err := request.ctx.Err(); err != nil {
				request.state.Store(callFinished)
				request.result <- callResult{err: contextDomainError(err)}
				continue
			}
			result := s.execute(request.ctx, request.method, request.params)
			request.state.Store(callFinished)
			request.result <- result
		}
	}
}

func (s *Supervisor) execute(ctx context.Context, method string, params any) callResult {
	if err := ctx.Err(); err != nil {
		return callResult{err: contextDomainError(err)}
	}
	child, err := s.ensureChild()
	if err != nil {
		return callResult{err: err}
	}
	if err := ctx.Err(); err != nil {
		return callResult{err: contextDomainError(err)}
	}
	if method == "shutdown" {
		child.expected.Store(true)
		s.mu.Lock()
		s.shutdownTarget = child
		s.mu.Unlock()
	}
	id := strconv.FormatUint(s.nextID.Add(1), 10)
	idJSON, _ := json.Marshal(id)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return callResult{err: NewError(DomainUnsafeRequest, false, err.Error())}
	}
	response := make(chan callResult, 1)
	child.mu.Lock()
	child.pending[id] = response
	callCtx, cancelCall := context.WithCancel(ctx)
	child.activeParentID = id
	child.activeParentCtx = callCtx
	child.mu.Unlock()
	defer func() {
		cancelCall()
		child.mu.Lock()
		delete(child.pending, id)
		if child.activeParentID == id {
			child.activeParentID = ""
			child.activeParentCtx = nil
		}
		child.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return callResult{err: contextDomainError(err)}
	}
	if err := s.writeFrame(child, frame{JSONRPC: "2.0", ID: idJSON, Method: method, Params: paramsJSON}); err != nil {
		if errors.Is(err, errFrameTooLarge) {
			return callResult{err: NewError(DomainUnsafeRequest, false, err.Error())}
		}
		return callResult{err: NewError(DomainChildCrashed, true, err.Error())}
	}
	select {
	case result := <-response:
		if result.err == nil {
			s.recordSuccess()
		}
		return result
	case <-ctx.Done():
		_ = s.writeFrame(child, frame{JSONRPC: "2.0", Method: "$/cancelRequest", Params: json.RawMessage(`{"id":` + string(idJSON) + `}`)})
		timer := time.NewTimer(s.config.CancelGrace)
		defer timer.Stop()
		select {
		case <-response:
		case <-timer.C:
			s.terminate(child)
		case <-child.done:
		}
		return callResult{err: contextDomainError(ctx.Err())}
	case <-child.done:
		return callResult{err: NewError(DomainChildCrashed, true, "runtime child exited")}
	case <-s.stop:
		return callResult{err: NewError(DomainDependencyDown, true, "runtime is closed")}
	}
}

func (s *Supervisor) ensureChild() (*childProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.closed {
		return nil, NewError(DomainDependencyDown, true, "runtime is closed")
	}
	if s.child != nil {
		select {
		case <-s.child.done:
			s.child = nil
		default:
			return s.child, nil
		}
	}
	if now.Before(s.circuitOpen) {
		return nil, NewError(DomainDependencyDown, true, "runtime circuit is open")
	}
	if now.Before(s.nextStart) {
		return nil, NewError(DomainDependencyDown, true, "runtime restart backoff is active")
	}
	cmd := exec.Command(s.config.Command[0], s.config.Command[1:]...)
	cmd.Env = childEnvironment(s.config.Env)
	configureProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, NewError(DomainDependencyDown, true, err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, NewError(DomainDependencyDown, true, err.Error())
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, NewError(DomainDependencyDown, true, err.Error())
	}
	if err := cmd.Start(); err != nil {
		return nil, NewError(DomainDependencyDown, true, err.Error())
	}
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	child := &childProcess{
		cmd: cmd, stdin: stdin, done: make(chan error, 1), pending: make(map[string]chan callResult),
		generation: s.nextGeneration.Add(1), lifecycle: lifecycle, cancelLifecycle: cancelLifecycle,
		processGroupID: processGroupID(cmd.Process),
	}
	s.child = child
	go s.readStdout(child, stdout)
	go s.readStderr(stderr)
	go s.waitChild(child)
	return child, nil
}

func (s *Supervisor) readStdout(child *childProcess, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), MaxFrameBytes+1)
	for scanner.Scan() {
		data := append([]byte(nil), scanner.Bytes()...)
		message, err := decodeFrame(data)
		if err != nil {
			s.failChild(child, NewError(DomainProtocolMismatch, false, err.Error()))
			return
		}
		if message.response() {
			s.deliverResponse(child, message)
			continue
		}
		if message.request() {
			s.dispatchChildRequest(child, message)
			continue
		}
		if s.config.NotificationHandler != nil {
			s.config.NotificationHandler(message.Method, message.Params)
		}
	}
	if err := scanner.Err(); err != nil {
		s.failChild(child, NewError(DomainProtocolMismatch, false, "stdout frame: "+err.Error()))
	}
}

func (s *Supervisor) deliverResponse(child *childProcess, message frame) {
	var id string
	if err := json.Unmarshal(message.ID, &id); err != nil || id == "" {
		s.failChild(child, NewError(DomainProtocolMismatch, false, "response id must be a string"))
		return
	}
	child.mu.Lock()
	pending := child.pending[id]
	child.mu.Unlock()
	if pending == nil {
		s.failChild(child, NewError(DomainProtocolMismatch, false, "response has unknown id"))
		return
	}
	if message.Error != nil {
		message.Error.Message = sanitizeDiagnostic(message.Error.Message, s.config.SecretValues)
		pending <- callResult{err: message.Error}
	} else {
		pending <- callResult{value: message.Result}
	}
}

func (s *Supervisor) dispatchChildRequest(child *childProcess, message frame) {
	var requestID string
	if err := json.Unmarshal(message.ID, &requestID); err != nil || requestID == "" {
		s.writeChildError(child, message.ID, NewError(DomainProtocolMismatch, false, "child request id must be a non-empty string"))
		return
	}
	var binding struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(message.Params, &binding); err != nil || binding.RequestID == "" {
		s.writeChildError(child, message.ID, NewError(DomainProtocolMismatch, false, "child request must bind request_id"))
		return
	}
	s.mu.Lock()
	currentGeneration := uint64(0)
	if s.child != nil {
		currentGeneration = s.child.generation
	}
	s.mu.Unlock()
	child.mu.Lock()
	parentID, parentCtx := child.activeParentID, child.activeParentCtx
	child.mu.Unlock()
	if child.generation != currentGeneration || parentCtx == nil || binding.RequestID != parentID {
		s.writeChildError(child, message.ID, NewError(DomainPermissionDenied, false, "child request is not bound to the active parent call"))
		return
	}
	if !child.reverseActive.CompareAndSwap(false, true) {
		s.writeChildError(child, message.ID, NewError(DomainOverloaded, true, "a child request is already active"))
		return
	}
	go s.handleChildRequest(child, message, parentID, parentCtx)
}

func (s *Supervisor) handleChildRequest(child *childProcess, message frame, parentID string, parentCtx context.Context) {
	defer child.reverseActive.Store(false)
	ctx, cancel := context.WithTimeout(parentCtx, s.config.RequestTimeout)
	stopLifecycle := context.AfterFunc(child.lifecycle, cancel)
	defer func() {
		stopLifecycle()
		cancel()
	}()
	var result any
	var rpcErr *RPCError
	if s.config.RequestHandler == nil {
		rpcErr = NewError(DomainPermissionDenied, false, "child requests are disabled")
	} else {
		result, rpcErr = s.config.RequestHandler(ctx, message.Method, message.Params)
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && parentCtx.Err() == nil && child.lifecycle.Err() == nil {
			rpcErr = NewError(DomainTimeout, true, "child request handler timed out")
			result = nil
		} else {
			return
		}
	}
	child.mu.Lock()
	stillActive := child.activeParentID == parentID && child.activeParentCtx != nil
	child.mu.Unlock()
	if !stillActive || child.lifecycle.Err() != nil {
		return
	}
	response := frame{JSONRPC: "2.0", ID: message.ID}
	if rpcErr != nil {
		response.Error = rpcErr
	} else {
		response.Result, _ = json.Marshal(result)
	}
	_ = s.writeFrame(child, response)
}

func (s *Supervisor) writeChildError(child *childProcess, id json.RawMessage, rpcErr *RPCError) {
	_ = s.writeFrame(child, frame{JSONRPC: "2.0", ID: id, Error: rpcErr})
}

func (s *Supervisor) writeFrame(child *childProcess, message frame) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameBytes {
		return fmt.Errorf("%w: outbound frame exceeds %d bytes", errFrameTooLarge, MaxFrameBytes)
	}
	child.writeMu.Lock()
	defer child.writeMu.Unlock()
	_, err = child.stdin.Write(append(data, '\n'))
	return err
}

func (s *Supervisor) failChild(child *childProcess, err error) {
	child.mu.Lock()
	for _, pending := range child.pending {
		select {
		case pending <- callResult{err: err}:
		default:
		}
	}
	child.mu.Unlock()
	if !child.expected.Load() && !child.stopping.Load() {
		s.recordFailure()
	}
	s.terminate(child)
}

func (s *Supervisor) waitChild(child *childProcess) {
	err := child.cmd.Wait()
	child.cancelLifecycle()
	s.mu.Lock()
	if s.child == child {
		s.child = nil
	}
	if !child.expected.Load() && !child.stopping.Load() {
		s.recordFailureLocked()
	}
	s.mu.Unlock()
	child.done <- err
	close(child.done)
}

func (s *Supervisor) terminate(child *childProcess) {
	if child == nil || !child.stopping.CompareAndSwap(false, true) {
		return
	}
	_ = child.stdin.Close()
	if child.cmd.Process == nil {
		return
	}
	groupID := child.processGroupID
	_ = signalProcessGroup(child.cmd.Process, groupID, terminateSignal)
	timer := time.NewTimer(s.config.KillGrace)
	defer timer.Stop()
	// Do not return when the direct child exits: descendants can keep the
	// process group alive after inheriting no protocol pipes at all.
	<-timer.C
	if !processGroupNeedsKill(groupID) {
		return
	}
	_ = signalProcessGroup(child.cmd.Process, groupID, killSignal)
	deadline := time.Now().Add(s.config.KillGrace)
	for processGroupExists(groupID) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if processGroupExists(groupID) {
		s.appendStderr([]byte("[runtime process group remained after SIGKILL grace]\n"))
	}
}

func (s *Supervisor) recordSuccess() {
	s.mu.Lock()
	s.failures = 0
	s.nextStart = time.Time{}
	s.circuitOpen = time.Time{}
	s.mu.Unlock()
}

func (s *Supervisor) recordFailure() {
	s.mu.Lock()
	s.recordFailureLocked()
	s.mu.Unlock()
}

func (s *Supervisor) recordFailureLocked() {
	s.failures++
	shift := min(s.failures-1, 20)
	delay := s.config.BackoffBase * time.Duration(1<<shift)
	if delay > s.config.BackoffMax {
		delay = s.config.BackoffMax
	}
	s.nextStart = time.Now().Add(delay)
	if s.failures >= s.config.CircuitFailures {
		s.circuitOpen = time.Now().Add(s.config.CircuitOpen)
	}
}

func (s *Supervisor) readStderr(reader io.Reader) {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	var line bytes.Buffer
	truncated := false
	flush := func() {
		if line.Len() > 0 {
			s.appendStderr([]byte(sanitizeDiagnostic(line.String(), s.config.SecretValues) + "\n"))
		}
		if truncated {
			s.appendStderr([]byte("[stderr line truncated]\n"))
		}
		line.Reset()
		truncated = false
	}
	for {
		chunk, err := buffered.ReadSlice('\n')
		if remaining := s.config.StderrLineLimit - line.Len(); remaining > 0 {
			if len(chunk) > remaining {
				line.Write(chunk[:remaining])
				truncated = true
			} else {
				line.Write(chunk)
			}
		} else if len(chunk) > 0 {
			truncated = true
		}
		if err == nil {
			flush()
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		flush()
		return
	}
}

func (s *Supervisor) appendStderr(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stderr = append(s.stderr, data...)
	if overflow := len(s.stderr) - s.config.StderrLimit; overflow > 0 {
		s.stderr = append([]byte(nil), s.stderr[overflow:]...)
	}
}

var (
	secretPattern        = regexp.MustCompile(`(?i)(bearer\s+|(?:api[_-]?key|token|password|secret|credential)\s*[:=]\s*)[^\s,;]+`)
	credentialURLPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s:]+:[^/@\s]+@`)
	envNamePattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func redact(value string, known []string) string {
	return sanitizeDiagnostic(value, known)
}

func sanitizeDiagnostic(value string, known []string) string {
	value = credentialURLPattern.ReplaceAllString(value, `${1}[REDACTED]@`)
	value = secretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	for _, secret := range append(known, environmentSecrets()...) {
		if secret != "" && len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	paths := []struct{ value, label string }{}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, struct{ value, label string }{cwd, "[WORKSPACE]"})
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, struct{ value, label string }{home, "[HOME]"})
	}
	paths = append(paths, struct{ value, label string }{os.TempDir(), "[TMP]"})
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i].value) > len(paths[j].value) })
	for _, path := range paths {
		if len(path.value) > 1 {
			value = strings.ReplaceAll(value, path.value, path.label)
		}
	}
	const maxDiagnosticRunes = 1024
	runes := []rune(value)
	if len(runes) > maxDiagnosticRunes {
		value = string(runes[:maxDiagnosticRunes]) + "…[truncated]"
	}
	return value
}

func environmentSecrets() []string {
	var values []string
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if ok && sensitiveEnvKey(upper) {
			values = append(values, value)
		}
	}
	return values
}

func childEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	baseAllowed := map[string]struct{}{
		"HOME": {}, "PATH": {}, "SHELL": {}, "USER": {}, "LOGNAME": {}, "TMPDIR": {},
		"CLAUDE_CONFIG_DIR": {}, "XDG_CONFIG_HOME": {},
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		_, allowed := baseAllowed[key]
		if ok && allowed && !sensitiveEnvKey(key) {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if !sensitiveEnvKey(key) {
			values[key] = value
		}
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func sensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") ||
		strings.Contains(upper, "BASE_URL") || strings.Contains(upper, "CREDENTIAL")
}

func validEnvName(key string) bool { return envNamePattern.MatchString(key) }

func contextDomainError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(DomainTimeout, true, err.Error())
	}
	return NewError(DomainCancelled, false, err.Error())
}

func strictUnmarshal(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}
