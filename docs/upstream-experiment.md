# Upstream improvements experiment

This branch evaluates selected upstream fixes while retaining the custom Gemini
quota source, HEALTH layout, A/R/P columns, priority scoring and warm-up behavior.
It does not merge the rewritten upstream history wholesale.

- Branch: `experiment/upstream-improvements`
- Custom base: `d24a7a1` (`custom-build`, version `3.1.18`)
- Upstream reviewed: `aklkbqx/agy-swap` at
  `ee3a0058f5fbba5eb2beceef91970c7c73d9b9ee` (upstream `v2.11.11` source)
- Experimental version: `3.1.19`; the test binary uses build ID `upstream-experiment`.

## Integration scope

| Area | Selected improvement | Custom behavior retained |
| --- | --- | --- |
| Sessions | Synchronize the standalone Jetski OAuth file in switch, login, logout and rollback paths | Authoritative secure-store detection, drift checks and verified switching |
| Manual rotation | Prefer another eligible account instead of selecting the active account again | Gemini-only ranking, reserve policies and priority scoring |
| Login | Hand the foreground terminal to agy on Unix and reclaim it afterwards | Existing Windows login behavior |
| Windows credentials | Reject trailing or malformed session JSON before credential writes | Compact representation, token identity and Credential Manager size limit |
| Cooldown | Bound each duration component before multiplication | Seven-day maximum and existing syntax |
| CLI quota | Compact pool labels and local reset timestamps | TUI HEALTH and account-detail layout |
| Installer | Require a successful binary version command and an exact version token | Existing Windows install location |
| Build | Patched stable Go/dependencies, vulnerability checks and build evidence | Custom module path and distinct experimental build identity |

Upstream Homebrew formula management is excluded because it targets the upstream
distribution. The upstream module-path migration, website split, historical
release records and bulk TUI changes are also excluded: they do not improve this
custom Windows test build and would replace or complicate existing custom behavior.

## Build and try

From PowerShell in the repository:

```powershell
pwsh -File scripts/build-experiment.ps1
& .\dist\experiments\agy-swap-upstream-improvements.exe
```

The script produces a separate executable and verification evidence under
`dist/experiments`. It does not install the binary or change PATH. The normal
installed `agy-swap` remains available.

The experimental executable uses the same account/configuration directory as the
normal application. Close the normal TUI before testing. Check HEALTH and A/R/P,
press `r` to refresh, and verify that Weekly and Five Hour remain first in the
Gemini detail pane. If using `balanced`, verify that `n` selects another eligible
account. Use your normal installed executable again to return to the stable build.

Automated session/rollback checks use temporary test directories; live testing
does not automatically switch accounts, log out, or send generation requests.

## Verification

- `go test ./...`: passed on Windows with Go 1.27.2.
- `go vet ./...`: passed.
- `govulncheck` v1.9.0: no vulnerabilities found in the Windows source scan.
- Linux amd64 cross-build: passed; Unix terminal handoff still needs interactive
  testing on a Unix host.
- The source for TUI rendering, A/R/P, automatic rotation scoring, warm-up and
  production Gemini quota retrieval is unchanged from `custom-build`.
