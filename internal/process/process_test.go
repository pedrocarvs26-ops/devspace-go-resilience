package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
)

func TestProcessHelper(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "devspace-process-helper" {
			fmt.Fprint(os.Stdout, "stdout-ready")
			fmt.Fprint(os.Stderr, "stderr-ready")
			time.Sleep(30 * time.Second)
			os.Exit(0)
		}
	}
}

func TestProcessCancellationAndOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd, cleanup, err := Start(ctx, t.TempDir(), os.Args[0], []string{"-test.run=^TestProcessHelper$", "--", "devspace-process-helper"}, config.DefaultBashResourceLimit(), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancellation did not stop process")
	}
	if ctx.Err() == nil {
		t.Fatal("child exited before deadline")
	}
}
