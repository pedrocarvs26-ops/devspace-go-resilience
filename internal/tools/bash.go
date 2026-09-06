package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog/log"
	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/process"
)

type BashInput struct {
	WorkspaceID      string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Command          string `json:"command" jsonschema:"Shell command to start asynchronously."`
	WorkingDirectory string `json:"workingDirectory,omitempty" jsonschema:"Working directory relative to workspace root."`
	Timeout          int    `json:"timeout,omitempty" jsonschema:"Job deadline in seconds; zero uses bashJobs.timeout, capped at bashJobs.maxTimeout."`
}

type BashStatusInput struct {
	WorkspaceID string `json:"workspaceId"`
	JobID       string `json:"job_id"`
	Offset      int64  `json:"offset,omitempty" jsonschema:"Byte cursor from previous next_offset; zero starts at oldest retained output."`
	MaxBytes    int    `json:"maxBytes,omitempty" jsonschema:"Response size in raw output bytes, default 65536, maximum 131072."`
}

type BashCancelInput struct {
	WorkspaceID string `json:"workspaceId"`
	JobID       string `json:"job_id"`
}

type OutputChunk struct {
	Stream     string `json:"stream"`
	Offset     int64  `json:"offset"`
	Text       string `json:"text"`
	DataBase64 string `json:"data_base64"` // exact bytes, including partial UTF-8 and binary output
}

type BashOutput struct {
	Result       string        `json:"result"`
	JobID        string        `json:"job_id"`
	State        string        `json:"state"`
	ExitCode     *int          `json:"exit_code,omitempty"`
	Error        string        `json:"error,omitempty"`
	Chunks       []OutputChunk `json:"chunks"`
	NextOffset   int64         `json:"next_offset"`
	OldestOffset int64         `json:"oldest_offset"`
	DroppedBytes int64         `json:"dropped_bytes"`
	HasMore      bool          `json:"has_more"`
}

type storedChunk struct {
	stream string
	offset int64
	data   []byte
}
type bashJob struct {
	mu                                sync.Mutex
	id, workspaceID, state, errorText string
	cancel                            context.CancelFunc
	exitCode                          *int
	finished                          time.Time
	chunks                            []storedChunk
	bytes                             int
	end                               int64
	capacity                          int
}

type JobManager struct {
	mu          sync.Mutex
	jobs        map[string]*bashJob
	active      int
	closed      bool
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	janitorDone chan struct{}
	shell       string
	opts        config.BashJobs
	limits      config.BashResourceLimit
}

// One manager belongs to the SERVER, never to an HTTP request/MCP transport session.
func NewJobManager(cfg *config.Config) *JobManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &JobManager{jobs: make(map[string]*bashJob), ctx: ctx, cancel: cancel,
		shell: cfg.Shell, opts: cfg.BashJobs, limits: cfg.BashResourceLimit, janitorDone: make(chan struct{})}
	go func() {
		defer close(m.janitorDone)
		interval := min(time.Minute, config.Duration(m.opts.Retention))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				m.mu.Lock()
				m.pruneLocked(now)
				m.mu.Unlock()
			}
		}
	}()
	return m
}

func (m *JobManager) pruneLocked(now time.Time) {
	for id, j := range m.jobs {
		j.mu.Lock()
		expired := !j.finished.IsZero() && now.Sub(j.finished) >= config.Duration(m.opts.Retention)
		j.mu.Unlock()
		if expired {
			delete(m.jobs, id)
		}
	}
}

func (m *JobManager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
	<-m.janitorDone
}

func shellDirectory(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("workingDirectory must be relative")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("workingDirectory is outside workspace root")
	}
	info, err := os.Stat(realDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workingDirectory is not a directory")
	}
	return realDir, nil
}

func (m *JobManager) Submit(input BashInput, root string) (BashOutput, error) {
	if strings.TrimSpace(input.Command) == "" {
		return BashOutput{}, errors.New("command is empty")
	}
	if input.Timeout < 0 {
		return BashOutput{}, errors.New("timeout must be nonnegative")
	}
	cwd, err := shellDirectory(root, input.WorkingDirectory)
	if err != nil {
		return BashOutput{}, err
	}
	timeout := config.Duration(m.opts.Timeout)
	if input.Timeout > 0 {
		// Clamp before multiplying to avoid duration overflow.
		seconds := min(int64(input.Timeout), int64(config.Duration(m.opts.MaxTimeout)/time.Second))
		timeout = time.Duration(seconds) * time.Second
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return BashOutput{}, errors.New("server shutting down")
	}
	m.pruneLocked(time.Now())
	if m.active >= m.opts.MaxConcurrent {
		m.mu.Unlock()
		return BashOutput{}, errors.New("bash busy: maximum concurrent jobs reached; poll existing jobs before retrying")
	}
	// Do not silently evict unread results. Retention expiry reclaims the bounded store.
	if len(m.jobs) >= m.opts.MaxRetained {
		m.mu.Unlock()
		return BashOutput{}, errors.New("bash result store full; retry after retention expires")
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	j := &bashJob{id: uuid.NewString(), workspaceID: input.WorkspaceID, state: "queued", cancel: cancel, capacity: m.opts.OutputBytes}
	m.jobs[j.id] = j
	m.active++
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(ctx, j, cwd, input.Command)
	// No process start or wait on the HTTP request goroutine.
	return BashOutput{Result: "Job accepted. Poll bash_status with job_id and next_offset; do not resubmit the command.",
		JobID: j.id, State: "queued", Chunks: []OutputChunk{}}, nil
}

func (m *JobManager) run(ctx context.Context, j *bashJob, cwd, command string) {
	defer m.wg.Done()
	defer j.cancel()
	j.mu.Lock()
	j.state = "running"
	j.mu.Unlock()
	name, args := shellCommandForOS(m.shell, runtime.GOOS, command)
	cmd, cleanup, err := process.Start(ctx, cwd, name, args, m.limits,
		jobWriter{j, "stdout"}, jobWriter{j, "stderr"})
	exitCode := -1
	if err == nil {
		err = cmd.Wait()
		// Kill remaining descendants, including children that inherited stdout/stderr.
		cleanup()
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
	}
	state := "completed"
	if err != nil {
		state = "failed"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		state = "timed_out"
		err = ctx.Err()
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		state = "cancelled"
		err = ctx.Err()
	}
	m.mu.Lock()
	j.mu.Lock()
	j.state = state
	j.exitCode = &exitCode
	if err != nil {
		j.errorText = err.Error()
	}
	j.finished = time.Now()
	j.mu.Unlock()
	m.active--
	m.mu.Unlock()
	log.Info().Str("job_id", j.id).Str("state", state).Int("exit_code", exitCode).Err(err).Msg("bash_job_finished")
}

type jobWriter struct {
	job    *bashJob
	stream string
}

func (w jobWriter) Write(p []byte) (int, error) {
	n := len(p)
	j := w.job
	// Never hold the job mutex for an unbounded output burst.
	for len(p) > 0 {
		size := min(len(p), 4096)
		data := append([]byte(nil), p[:size]...)
		j.mu.Lock()
		j.chunks = append(j.chunks, storedChunk{w.stream, j.end, data})
		j.end += int64(size)
		j.bytes += size
		for j.bytes > j.capacity || len(j.chunks) > 512 {
			j.bytes -= len(j.chunks[0].data)
			j.chunks[0] = storedChunk{}
			j.chunks = j.chunks[1:]
		}
		j.mu.Unlock()
		p = p[size:]
	}
	return n, nil
}

func (m *JobManager) lookup(workspaceID, id string) (*bashJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.workspaceID != workspaceID {
		return nil, errors.New("job not found in this workspace (or retention expired/server restarted)")
	}
	return j, nil
}

func (m *JobManager) Status(input BashStatusInput) (BashOutput, error) {
	if input.Offset < 0 || input.MaxBytes < 0 {
		return BashOutput{}, errors.New("offset and maxBytes must be nonnegative")
	}
	j, err := m.lookup(input.WorkspaceID, input.JobID)
	if err != nil {
		return BashOutput{}, err
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = 65536
	}
	limit = min(limit, 131072)
	j.mu.Lock()
	defer j.mu.Unlock()
	if input.Offset > j.end {
		return BashOutput{}, errors.New("offset is past the end of output")
	}
	oldest := j.end
	if len(j.chunks) > 0 {
		oldest = j.chunks[0].offset
	}
	cursor := max(input.Offset, oldest)
	out := BashOutput{JobID: j.id, State: j.state, ExitCode: j.exitCode, Error: j.errorText,
		Chunks: []OutputChunk{}, OldestOffset: oldest, DroppedBytes: max(int64(0), oldest-input.Offset)}
	var text strings.Builder
	for _, chunk := range j.chunks {
		end := chunk.offset + int64(len(chunk.data))
		if cursor >= end {
			continue
		}
		data := chunk.data[cursor-chunk.offset:]
		if len(data) > limit {
			data = data[:limit]
		}
		safeText := strings.ToValidUTF8(string(data), "\uFFFD")
		out.Chunks = append(out.Chunks, OutputChunk{Stream: chunk.stream, Offset: cursor,
			Text: safeText, DataBase64: base64.StdEncoding.EncodeToString(data)})
		text.WriteString(safeText)
		cursor += int64(len(data))
		limit -= len(data)
		if limit == 0 {
			break
		}
	}
	out.Result = text.String()
	out.NextOffset = cursor
	out.HasMore = cursor < j.end
	return out, nil
}

func (m *JobManager) Cancel(input BashCancelInput) (BashOutput, error) {
	j, err := m.lookup(input.WorkspaceID, input.JobID)
	if err != nil {
		return BashOutput{}, err
	}
	j.cancel()
	return m.Status(BashStatusInput{WorkspaceID: input.WorkspaceID, JobID: input.JobID})
}

func BashToolResult(out BashOutput, err error) (*mcp.CallToolResult, BashOutput, error) {
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, BashOutput{}, nil
	}
	// Include the cursor/state in text as well as structuredContent for older clients.
	data, err := json.Marshal(out)
	if err != nil {
		return nil, BashOutput{}, fmt.Errorf("encode job status: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
}
