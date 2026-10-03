package demoserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/creack/pty"
)

func TestStartSessionStartupFailureCleansTemporaryHome(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "missing-demo-binary")
	if _, err := startSession(context.Background(), binary, root); err == nil {
		t.Fatal("startSession unexpectedly accepted a missing binary")
	} else if runtime.GOOS == "windows" && !errors.Is(err, pty.ErrUnsupported) {
		t.Fatalf("Windows startup error = %v, want PTY unsupported", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("startup failure leaked temporary homes: %v", entries)
	}
}
