package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/tools"
	"github.com/snakex21/devspace-go/internal/workspace"
)

func jobOutput(t *testing.T, result *mcp.CallToolResult) tools.BashOutput {
	t.Helper()
	if result.IsError {
		t.Fatalf("MCP error: %+v", result.Content)
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			var out tools.BashOutput
			if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatal("missing job output")
	return tools.BashOutput{}
}

func TestMCPJobSurvivesRequestCancellationAndReconnect(t *testing.T) {
	for _, naming := range []config.ToolNaming{config.NamingShort, config.NamingLegacy} {
		t.Run(string(naming), func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.AllowedRoots = []string{t.TempDir()}
			cfg.ToolNaming = naming
			cfg.ToolMode = config.ToolModeMinimal
			s := &Server{cfg: cfg, registry: workspace.NewRegistry(cfg, nil)}
			defer s.jobManager().Close()
			httpServer := httptest.NewServer(s.handler())
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			connect := func() *mcp.ClientSession {
				client := mcp.NewClient(&mcp.Implementation{Name: "job-test", Version: "1"}, nil)
				session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				return session
			}
			session := connect()
			command := "printf first; sleep 2; printf last"
			if runtime.GOOS == "windows" {
				command = "Write-Output first; Start-Sleep -Seconds 2; Write-Output last"
			}
			requestCtx, requestCancel := context.WithCancel(ctx)
			result, err := session.CallTool(requestCtx, &mcp.CallToolParams{Name: s.toolNames().Bash, Arguments: map[string]any{"workspaceId": "default", "command": command}})
			requestCancel()
			if err != nil {
				session.Close()
				t.Fatal(err)
			}
			job := jobOutput(t, result)
			session.Close()
			session = connect()
			defer session.Close()
			for i := 0; i < 20; i++ {
				if _, err := session.ListTools(ctx, nil); err != nil {
					t.Fatal("MCP unresponsive during shell job:", err)
				}
			}
			cursor := int64(0)
			seen := ""
			for {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "bash_status", Arguments: map[string]any{"workspaceId": "default", "job_id": job.JobID, "offset": cursor}})
				if err != nil {
					t.Fatal(err)
				}
				out := jobOutput(t, result)
				cursor = out.NextOffset
				seen += out.Result
				if out.State != "running" && out.State != "queued" && !out.HasMore {
					if out.State != "completed" || out.ExitCode == nil || *out.ExitCode != 0 {
						t.Fatalf("job ended: %+v", out)
					}
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(25 * time.Millisecond):
				}
			}
			if seen == "" {
				t.Fatal("no output after reconnect")
			}
		})
	}
}
