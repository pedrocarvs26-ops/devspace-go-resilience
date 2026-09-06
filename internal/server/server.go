package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog/log"
	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/locales"
	"github.com/snakex21/devspace-go/internal/logger"
	"github.com/snakex21/devspace-go/internal/store"
	"github.com/snakex21/devspace-go/internal/tools"
	"github.com/snakex21/devspace-go/internal/workspace"
)

// boolPtr returns a pointer to the given bool value (for ToolAnnotations pointer fields).
func boolPtr(b bool) *bool { return &b }

const Version = "2.2.0"

// Server represents the running Dev Space Go server.
type Server struct {
	jobsOnce   sync.Once
	jobs       *tools.JobManager
	cfg        *config.Config
	httpServer *http.Server
	tunnelStop context.CancelFunc
	registry   *workspace.Registry
	store      *store.Store
}

// New creates a new Dev Space Go server.
func New(cfg *config.Config) (*Server, error) {
	logger.Init(string(cfg.Logging.Level), string(cfg.Logging.Format))
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	s, err := store.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	registry := workspace.NewRegistry(cfg, s)

	return &Server{
		cfg:      cfg,
		registry: registry,
		store:    s,
	}, nil
}

// Start binds HTTP BEFORE starting a tunnel and owns all background lifetimes.
func (s *Server) Start() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() {
		s.jobManager().Close()
		if s.store != nil {
			_ = s.store.Close()
		}
	}()
	s.httpServer = s.newHTTPServer(s.handler())
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	tunnelCtx, cancelTunnel := context.WithCancel(ctx)
	s.tunnelStop = cancelTunnel
	tunnelDone := make(chan struct{})
	go func() { defer close(tunnelDone); s.superviseTunnels(tunnelCtx) }()
	defer func() { cancelTunnel(); <-tunnelDone }()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		log.Info().Msg(locales.T("server.shutdown"))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("server_shutdown_error")
			_ = s.httpServer.Close()
		}
	}()
	log.Info().Str("host", s.cfg.Host).Int("port", s.cfg.Port).Msg(locales.T("server.listening"))
	err = s.httpServer.Serve(listener)
	stop()
	<-shutdownDone
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, `{"ok":true,"name":"devspace-go"}`)
	})
	mux.Handle("/mcp", s.streamableMCPHandler())
	mux.Handle("/sse", mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		return s.createMcpServer()
	}, &mcp.SSEOptions{DisableLocalhostProtection: true}))
	return s.loggingMiddleware(mux)
}

func (s *Server) newHTTPServer(handler http.Handler) *http.Server {
	read := config.Duration(s.cfg.HTTPReadTimeout)
	header := 10 * time.Second
	if read > 0 {
		header = min(header, read)
	}
	return &http.Server{
		Addr: net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port)), Handler: handler,
		ReadTimeout: read, ReadHeaderTimeout: header,
		WriteTimeout:   config.Duration(s.cfg.HTTPWriteTimeout),
		IdleTimeout:    config.Duration(s.cfg.HTTPIdleTimeout),
		MaxHeaderBytes: 1 << 20,
	}
}

func (s *Server) jobManager() *tools.JobManager {
	s.jobsOnce.Do(func() { s.jobs = tools.NewJobManager(s.cfg) })
	return s.jobs
}

func (s *Server) streamableMCPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return s.createMcpServer()
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                  true,
			JSONResponse:               true,
			DisableLocalhostProtection: true,
		},
	)
}

func findCloudflaredExecutable() string {
	names := []string{"cloudflared"}
	if runtime.GOOS == "windows" {
		names = []string{"cloudflared.exe", "cloudflared"}
	}
	var dirs []string

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		for dir := exeDir; dir != ""; dir = filepath.Dir(dir) {
			dirs = append(dirs, filepath.Join(dir, "tools"), dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "tools"), wd)
	}

	seen := map[string]bool{}
	for _, dir := range dirs {
		cleanDir := filepath.Clean(dir)
		if seen[cleanDir] {
			continue
		}
		seen[cleanDir] = true
		for _, name := range names {
			candidate := filepath.Join(cleanDir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
				return candidate
			}
		}
	}

	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func printTunnelURL(url string) {
	mcpURL := url + "/mcp"
	sseURL := url + "/sse"
	lines := []string{
		"🌐 " + locales.T("tunnel.active"),
		mcpURL,
		sseURL,
		"",
		locales.T("tunnel.paste_chatgpt"),
		mcpURL,
		locales.T("tunnel.try_sse"),
	}
	width := 54
	for _, line := range lines {
		if lineWidth := utf8.RuneCountInString(line); lineWidth > width {
			width = lineWidth
		}
	}

	fmt.Println()
	fmt.Printf("╔%s╗\n", strings.Repeat("═", width+4))
	for _, line := range lines {
		padding := strings.Repeat(" ", width-utf8.RuneCountInString(line))
		fmt.Printf("║  %s%s  ║\n", line, padding)
	}
	fmt.Printf("╚%s╝\n", strings.Repeat("═", width+4))
	fmt.Println()
}

// createMcpServer creates a new MCP server with all tools registered.
func (s *Server) createMcpServer() *mcp.Server {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "devspace-go", Version: Version},
		&mcp.ServerOptions{
			Instructions: s.serverInstructions(),
		},
	)

	s.registerTools(mcpServer)
	return mcpServer
}

// registerTools registers all Dev Space Go tools on the MCP server.
func (s *Server) registerTools(server *mcp.Server) {
	names := s.toolNames()

	// open_workspace
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "open_workspace",
			Description: "Open a local project directory as a coding workspace. If path is empty or 'default', opens the first configured allowed root. Call this once per project folder or worktree before reading, editing, searching, writing, or running commands. Reuse the returned workspaceId for later calls in the same folder. If a remote client blocks local absolute paths, call open_default_workspace instead.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input OpenWorkspaceInput) (*mcp.CallToolResult, OpenWorkspaceOutput, error) {
			mode := workspace.ModeCheckout
			if input.Mode == "worktree" {
				mode = workspace.ModeWorktree
			}

			wsCtx, err := s.registry.OpenWorkspace(input.Path, mode, input.BaseRef)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(fmt.Errorf("failed to open workspace: %v", err))
				return result, OpenWorkspaceOutput{}, nil
			}

			var agentsFiles []AgentsFileOutput
			for _, f := range wsCtx.AgentsFiles {
				agentsFiles = append(agentsFiles, AgentsFileOutput{
					Path:    workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
					Content: f.Content,
				})
			}
			var availableAgentsFiles []AvailableAgentsFileOutput
			for _, f := range wsCtx.AvailableAgentsFiles {
				availableAgentsFiles = append(availableAgentsFiles, AvailableAgentsFileOutput{
					Path: workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
				})
			}

			instruction := "Use this workspaceId in all subsequent tool calls for this project. Do not call open_workspace again for this same folder unless this workspaceId stops working, the user asks to reopen, or you switch to a different folder/worktree."

			resultText := fmt.Sprintf("Opened workspace %s\nRoot: %s\nMode: %s\n%s",
				wsCtx.Workspace.ID, wsCtx.Workspace.Root, wsCtx.Workspace.Mode, instruction)

			return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: resultText}},
				}, OpenWorkspaceOutput{
					WorkspaceID:          wsCtx.Workspace.ID,
					Root:                 wsCtx.Workspace.Root,
					Mode:                 string(wsCtx.Workspace.Mode),
					AgentsFiles:          agentsFiles,
					AvailableAgentsFiles: availableAgentsFiles,
					Instruction:          instruction,
				}, nil
		},
	)

	// open_default_workspace avoids passing local absolute paths through clients
	// that may block filesystem-looking arguments before they reach Dev Space Go.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "open_default_workspace",
			Description: "Open the default configured workspace without sending a local path. Use this when open_workspace with an absolute Windows/macOS/Linux path is blocked by the MCP client. Returns a workspaceId for the first allowed root.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input OpenDefaultWorkspaceInput) (*mcp.CallToolResult, OpenWorkspaceOutput, error) {
			wsCtx, err := s.registry.OpenDefaultWorkspace()
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(fmt.Errorf("failed to open default workspace: %v", err))
				return result, OpenWorkspaceOutput{}, nil
			}

			var agentsFiles []AgentsFileOutput
			for _, f := range wsCtx.AgentsFiles {
				agentsFiles = append(agentsFiles, AgentsFileOutput{
					Path:    workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
					Content: f.Content,
				})
			}
			var availableAgentsFiles []AvailableAgentsFileOutput
			for _, f := range wsCtx.AvailableAgentsFiles {
				availableAgentsFiles = append(availableAgentsFiles, AvailableAgentsFileOutput{
					Path: workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
				})
			}

			instruction := "Use this workspaceId in all subsequent tool calls for this project. You may also pass workspaceId 'default' or 'latest' if the exact ID is stale after reconnecting."
			resultText := fmt.Sprintf("Opened default workspace %s\nRoot: %s\nMode: %s\n%s",
				wsCtx.Workspace.ID, wsCtx.Workspace.Root, wsCtx.Workspace.Mode, instruction)

			return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: resultText}},
				}, OpenWorkspaceOutput{
					WorkspaceID:          wsCtx.Workspace.ID,
					Root:                 wsCtx.Workspace.Root,
					Mode:                 string(wsCtx.Workspace.Mode),
					AgentsFiles:          agentsFiles,
					AvailableAgentsFiles: availableAgentsFiles,
					Instruction:          instruction,
				}, nil
		},
	)

	// read
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Read,
			Description: "Read a file inside an open workspace. Use this for file inspection instead of shell commands like cat. Call open_workspace first and pass workspaceId.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.ReadInput) (*mcp.CallToolResult, tools.ReadOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.ReadOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.ReadOutput{}, nil
			}

			return tools.ReadFile(ctx, req, input, ws.Root)
		},
	)

	// write
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Write,
			Description: fmt.Sprintf("Create or completely overwrite a file inside an open workspace. Prefer %s for targeted changes to existing files. Call open_workspace first and pass workspaceId.", names.Edit),
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.WriteInput) (*mcp.CallToolResult, tools.WriteOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.WriteOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.WriteOutput{}, nil
			}

			return tools.WriteFile(ctx, req, input, ws.Root)
		},
	)

	// mkdir
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Mkdir,
			Description: "Create a directory inside an open workspace, including missing parent directories. Use this instead of shell mkdir/New-Item. Call open_workspace first and pass workspaceId.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.MkdirInput) (*mcp.CallToolResult, tools.MkdirOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MkdirOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MkdirOutput{}, nil
			}

			return tools.MakeDirectory(ctx, req, input, ws.Root)
		},
	)

	// move
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Move,
			Description: "Move or rename a file/directory inside an open workspace. Creates missing parent directories for the destination. Use this instead of shell Move-Item/mv. Call open_workspace first and pass workspaceId.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.MoveInput) (*mcp.CallToolResult, tools.MoveOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MoveOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.SourcePath)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MoveOutput{}, nil
			}
			_, err = s.registry.ResolvePath(ws, input.TargetPath)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MoveOutput{}, nil
			}

			return tools.MovePath(ctx, req, input, ws.Root)
		},
	)

	// edit
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Edit,
			Description: fmt.Sprintf("Edit one file inside an open workspace by replacing exact text blocks. Prefer this over %s for targeted changes. Call open_workspace first and pass workspaceId.", names.Write),
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.EditInput) (*mcp.CallToolResult, tools.EditOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.EditOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.EditOutput{}, nil
			}

			return tools.EditFile(ctx, req, input, ws.Root)
		},
	)

	// Full mode tools: grep, glob, ls
	if s.cfg.ToolMode == config.ToolModeFull {
		// grep
		mcp.AddTool(server,
			&mcp.Tool{
				Name:        names.Grep,
				Description: "Search file contents inside an open workspace. Use this before broad reads when looking for symbols, text, or usage sites. Call open_workspace first and pass workspaceId.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			func(ctx context.Context, req *mcp.CallToolRequest, input tools.GrepInput) (*mcp.CallToolResult, tools.GrepOutput, error) {
				ws, err := s.registry.GetWorkspace(input.WorkspaceID)
				if err != nil {
					result := &mcp.CallToolResult{}
					result.SetError(err)
					return result, tools.GrepOutput{}, nil
				}
				return tools.GrepFiles(ctx, req, input, ws.Root)
			},
		)

		// glob
		mcp.AddTool(server,
			&mcp.Tool{
				Name:        names.Glob,
				Description: "Find files by glob pattern inside an open workspace. Use this to discover filenames or narrow file sets before reading. Call open_workspace first and pass workspaceId.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			func(ctx context.Context, req *mcp.CallToolRequest, input tools.GlobInput) (*mcp.CallToolResult, tools.GlobOutput, error) {
				ws, err := s.registry.GetWorkspace(input.WorkspaceID)
				if err != nil {
					result := &mcp.CallToolResult{}
					result.SetError(err)
					return result, tools.GlobOutput{}, nil
				}
				return tools.FindFiles(ctx, req, input, ws.Root)
			},
		)

		// ls
		mcp.AddTool(server,
			&mcp.Tool{
				Name:        names.Ls,
				Description: "List a directory inside an open workspace. Use this for directory inspection before reading files. Call open_workspace first and pass workspaceId.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			func(ctx context.Context, req *mcp.CallToolRequest, input tools.LsInput) (*mcp.CallToolResult, tools.LsOutput, error) {
				ws, err := s.registry.GetWorkspace(input.WorkspaceID)
				if err != nil {
					result := &mcp.CallToolResult{}
					result.SetError(err)
					return result, tools.LsOutput{}, nil
				}
				return tools.ListDirectory(ctx, req, input, ws.Root)
			},
		)
	}

	// bash (PowerShell on Windows, bash on Unix)
	bashDesc := fmt.Sprintf(
		"Run a shell command inside an open workspace. On Windows, uses PowerShell.exe. On Unix, uses bash. Use only for tests, builds, git inspection, and commands that are better executed by the shell. Do not use %s to create, move, rename, or modify files. Prefer %s for file inspection, %s for creating directories, %s for moves/renames, and %s/%s for file changes. Call open_workspace first and pass workspaceId.",
		names.Bash, names.Read, names.Mkdir, names.Move, names.Edit, names.Write,
	)
	if s.cfg.ToolMode == config.ToolModeMinimal {
		bashDesc = fmt.Sprintf(
			"Run a shell command inside an open workspace. On Windows, uses PowerShell.exe. On Unix, uses bash. In minimal tool mode, %s, %s, and %s are disabled; use shell commands for search and directory inspection. Do not use %s to create or modify files. Prefer %s for direct file reads. Call open_workspace first and pass workspaceId.",
			names.Grep, names.Glob, names.Ls, names.Bash, names.Read,
		)
	}

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Bash,
			Description: bashDesc + " Returns a job_id immediately. Poll bash_status with job_id and next_offset until a terminal state and has_more=false; use bash_cancel to stop. Jobs survive HTTP disconnects, not server restarts.",
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.BashInput) (*mcp.CallToolResult, tools.BashOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.BashOutput{}, nil
			}
			if err := ctx.Err(); err != nil {
				return tools.BashToolResult(tools.BashOutput{}, err)
			}
			input.WorkspaceID = ws.ID // Canonical ownership, not the aliases default/latest.
			out, err := s.jobManager().Submit(input, ws.Root)
			return tools.BashToolResult(out, err)
		},
	)
	// Stable names also in legacy/minimal modes: jobs outlive transport sessions.
	mcp.AddTool(server, &mcp.Tool{Name: "bash_status",
		Description: "Poll incremental stdout/stderr of a shell job. Reuse next_offset as offset. Terminal states: completed, failed, cancelled, timed_out. Drain while has_more. dropped_bytes reports output lost to bounded retention.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.BashStatusInput) (*mcp.CallToolResult, tools.BashOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				return tools.BashToolResult(tools.BashOutput{}, err)
			}
			input.WorkspaceID = ws.ID
			out, err := s.jobManager().Status(input)
			return tools.BashToolResult(out, err)
		})
	mcp.AddTool(server, &mcp.Tool{Name: "bash_cancel",
		Description: "Cancel a shell job and its process tree. Poll bash_status for final state; cancellation may take several seconds.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), IdempotentHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.BashCancelInput) (*mcp.CallToolResult, tools.BashOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				return tools.BashToolResult(tools.BashOutput{}, err)
			}
			input.WorkspaceID = ws.ID
			out, err := s.jobManager().Cancel(input)
			return tools.BashToolResult(out, err)
		})

}

// ToolNames holds the tool naming configuration.
type ToolNames struct {
	Read  string
	Write string
	Mkdir string
	Move  string
	Edit  string
	Grep  string
	Glob  string
	Ls    string
	Bash  string
}

func (s *Server) toolNames() ToolNames {
	if s.cfg.ToolNaming == config.NamingLegacy {
		return ToolNames{
			Read:  "read_file",
			Write: "write_file",
			Mkdir: "create_directory",
			Move:  "move_path",
			Edit:  "edit_file",
			Grep:  "grep_files",
			Glob:  "find_files",
			Ls:    "list_directory",
			Bash:  "run_shell",
		}
	}
	return ToolNames{
		Read:  "read",
		Write: "write",
		Mkdir: "mkdir",
		Move:  "move",
		Edit:  "edit",
		Grep:  "grep",
		Glob:  "glob",
		Ls:    "ls",
		Bash:  "bash",
	}
}

func (s *Server) serverInstructions() string {
	names := s.toolNames()

	inspection := fmt.Sprintf("Prefer %s, %s, %s, and %s for file inspection. ",
		names.Read, names.Grep, names.Glob, names.Ls)
	if s.cfg.ToolMode == config.ToolModeMinimal {
		inspection = fmt.Sprintf("In minimal tool mode, %s, %s, and %s are disabled; use %s with command-line tools such as grep, rg, find, ls, and tree for search and directory inspection. ",
			names.Grep, names.Glob, names.Ls, names.Bash)
	}

	agentsMd := "Follow instructions returned by open_workspace. Before working under a path listed in availableAgentsFiles, use read to inspect that instruction file and follow it. "

	return fmt.Sprintf(
		"Use Dev Space Go as a local coding workspace. Call open_workspace once per project folder or worktree to obtain a workspaceId; if local absolute paths are blocked by the client, call open_default_workspace instead. Reuse that same workspaceId for all later file, search, edit, write, mkdir, move, and shell tools in that folder. If the workspaceId becomes stale after reconnecting, pass workspaceId 'default' or 'latest' to use the most recent/default workspace. %s%sPrefer %s for targeted modifications, %s only for new files or complete rewrites, %s for directory creation, %s for moves/renames, and %s for tests, builds, git inspection, package scripts, and commands that are better executed by the shell. Do not create, move, rename, or modify files with %s. On Windows, %s uses PowerShell.exe; on Unix, bash. Shell calls return a job_id immediately: poll bash_status with workspaceId, job_id and next_offset as offset. Never resubmit a command merely because it is still running. Use bash_cancel to stop it.",
		agentsMd,
		inspection,
		names.Edit, names.Write, names.Mkdir, names.Move, names.Bash, names.Bash, names.Bash,
	)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)

		path := r.URL.Path
		if !s.cfg.Logging.Requests {
			return
		}
		if !s.cfg.Logging.Assets && strings.HasPrefix(path, "/mcp-app-assets") {
			return
		}

		log.Info().
			Str("method", r.Method).
			Str("path", path).
			Str("remote_addr", r.RemoteAddr).
			Dur("duration_ms", time.Since(start)).
			Msg("http_request")
	})
}

// --- types ---

type OpenWorkspaceInput struct {
	Path    string `json:"path"`
	Mode    string `json:"mode,omitempty"`
	BaseRef string `json:"baseRef,omitempty"`
}

type OpenDefaultWorkspaceInput struct{}

type OpenWorkspaceOutput struct {
	WorkspaceID          string                      `json:"workspaceId"`
	Root                 string                      `json:"root"`
	Mode                 string                      `json:"mode"`
	AgentsFiles          []AgentsFileOutput          `json:"agentsFiles"`
	AvailableAgentsFiles []AvailableAgentsFileOutput `json:"availableAgentsFiles"`
	Instruction          string                      `json:"instruction"`
}

type AgentsFileOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type AvailableAgentsFileOutput struct {
	Path string `json:"path"`
}
