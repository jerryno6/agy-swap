# Developer Guide & Testing Workflows

This document guides contributors through setting up, testing, and verifying changes across `agy-swap`.

---

## 1. Prerequisites

- **Go**: Version 1.26 or newer (`go version`).
- **Make**: Standard POSIX make.
- **Xcode Command Line Tools** (macOS only): Required for compiling CGO bindings with Apple's `Security.framework`.
- **Bun** or **Node.js** (Optional): For developing the React site and native browser terminal in `site/`.

---

## 2. Common Development Commands

All standard workflows are codified in the [`Makefile`](../Makefile):

```bash
# Compile local binary to ./agy-swap
make build

# Install binary to ~/.local/bin/agy-swap and verify version
make install BUILD_ID=release

# Run unit tests across all packages
make test

# Run tests with the Go race detector enabled
make race

# Run Go static analysis
make vet

# Run memory allocation benchmarks
make benchmark

# Run terminal UI smoke test across multiple viewport dimensions
make tui-smoke

# Complete QA verification (formatting, test, race, vet, tui-smoke)
make qa
```

### Windows

The Makefile targets need a POSIX shell and build `agy-swap` without the `.exe` suffix, so `make install` cannot replace the installed `agy-swap.exe`. Use the PowerShell script instead:

```powershell
# Test, build with the Makefile VERSION, and install to
# %LOCALAPPDATA%\Programs\agy-swap\agy-swap.exe (same place as install.ps1)
pwsh -File scripts/install-windows.ps1

# Options: -SkipTests, -BuildId dev, -TargetDir <dir>
pwsh -File scripts/install-windows.ps1 -SkipTests
```

Keep a single install location. The previous binary is kept as `agy-swap.exe.<old-version>.bak`. The script warns about other `agy-swap` copies on PATH (for example an extensionless `agy-swap` from `make install`, which Git Bash runs first) but never deletes them. A running agy-swap TUI does not block the install (the old exe is renamed), but it keeps running the old version until restarted. Run the other checks directly with `go test ./...` and `go vet ./...`.

---

## 3. Testing Strategies

### 3.1 Unit & Regression Tests
Unit tests are co-located alongside the code they verify. All persistent operations use temporary directories (`t.TempDir()`) or isolated test environments.

### Warm-up verification

Do not add new `*_test.go` files for this project. Run `go test ./...` for existing regressions. Additional warm-up scenarios can use a temporary mock HTTP harness outside the repository, injected with Go's `-overlay` option.

Check direct Low and tiered model selection, missing model/project, token identity mismatch, completed/empty/blocked replies, quota decrease and delayed synchronization, unchanged quota, cancellation, vault failures, store conflicts, and JSON success/failure exit codes. Assert that each invocation issues at most one generation request, active-session credentials remain unchanged, and 5h reset times match the server. A remaining fraction of `0.999` must display as `99%`.

For a live smoke check, record the active account and live quota, send one `hi`, then compare the returned quota and active session. Stop after that generation request even if it fails or quota does not decrease; do not retry as part of the same smoke run.

Validation for 3.1.9: 23 temporary mock scenarios passed, as did existing Go tests and static analysis. The initial live request returned HTTP 404 before catalog-based model resolution and the Antigravity envelope were added. The final wire request has not been verified live; no second prompt was sent.

### 3.2 Race Detection
Always verify concurrent operations with the Go race detector:
```bash
make race
```
The race detector ensures that background quota refreshes and terminal rendering routines do not experience data races on shared state.

### 3.3 TUI Smoke Testing
The `./scripts/tui-smoke.sh` script verifies that the terminal interface initializes and renders correctly across common terminal dimensions:
- `28x12` (Ultra-compact / mobile)
- `40x20` (Compact)
- `80x24` (Standard ANSI terminal)
- `120x30` (Wide widescreen view)

### 3.4 Web Simulator Fixtures
AGY Live on [agy-swap.aklkbqx.com](https://agy-swap.aklkbqx.com) runs the native Go TUI under a PTY in an isolated demo container. Nginx forwards `/demo/ws` to the private gateway, while xterm.js renders the actual ANSI output. Use `go test ./internal/demoserver ./internal/app` to verify the demo path, and `cd site && bun run test && bun run build` for the browser UI.

---

## 4. Cross-Platform Builds

Release binaries are compiled for 6 target architectures using `./scripts/build-release.sh`:

| Platform | Architecture | CGO Enabled | Keystore with AGY_SWAP_VAULT=keychain |
| :--- | :--- | :--- | :--- |
| **macOS** | `arm64` / `amd64` | Yes (`CGO_ENABLED=1`) | Apple `Security.framework` |
| **Linux** | `arm64` / `amd64` | No (`CGO_ENABLED=0`) | Secret Service |
| **Windows**| `arm64` / `amd64` | No (`CGO_ENABLED=0`) | Windows Credential Manager |

By default every platform keeps saved tokens in `vault.json`; the keystore column applies only when `AGY_SWAP_VAULT=keychain` is set.

To test cross-compilation locally:
```bash
# Linux arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/agy-swap-linux ./cmd/agy-swap

# Windows amd64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/agy-swap-win.exe ./cmd/agy-swap
```
