package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// writeClipboard passes secrets on stdin, never through command arguments or
// captured command output. All helpers are bounded even if the desktop hangs.
func writeClipboard(ctx context.Context, value string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type helper struct {
		name string
		args []string
	}
	var helpers []helper
	switch runtime.GOOS {
	case "darwin":
		helpers = []helper{{name: "pbcopy"}}
	case "windows":
		// Explicit UTF-8 avoids Windows PowerShell's legacy stdin code page.
		const script = `[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); $ErrorActionPreference = 'Stop'; Set-Clipboard -Value ([Console]::In.ReadToEnd())`
		args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", script}
		helpers = []helper{{"powershell.exe", args}, {"pwsh.exe", args}}
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			helpers = append(helpers, helper{"wl-copy", []string{"--type", "text/plain;charset=utf-8"}})
		}
		if os.Getenv("DISPLAY") != "" {
			helpers = append(helpers, helper{"xclip", []string{"-selection", "clipboard"}}, helper{"xsel", []string{"--clipboard", "--input"}})
		}
	default:
		return errors.New("clipboard is not supported on this platform")
	}
	var available bool
	for _, h := range helpers {
		path, err := exec.LookPath(h.name)
		if err != nil {
			continue
		}
		available = true
		cmd := exec.CommandContext(ctx, path, h.args...)
		cmd.Stdin = strings.NewReader(value)
		cmd.WaitDelay = time.Second
		if cmd.Run() == nil {
			return nil
		}
		if ctx.Err() != nil {
			return errors.New("clipboard copy cancelled or timed out")
		}
	}
	if available {
		return errors.New("could not write to the system clipboard")
	}
	if runtime.GOOS == "linux" {
		return errors.New("clipboard unavailable: use a desktop session with wl-copy, xclip, or xsel installed")
	}
	return errors.New("clipboard helper unavailable")
}
