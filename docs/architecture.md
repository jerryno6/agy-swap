# Architecture

`agy-swap` is one Go binary. Almost all behavior lives in `internal/app`; a few pieces that have no UI or account logic live in small packages beside it.

## Layout

| Path | Responsibility |
|---|---|
| `cmd/agy-swap` | Entry point. Sets `version` and `buildID`, then runs `internal/app`. |
| `cmd/agy-swap-demo`, `cmd/agy-swap-demo-server`, `internal/demoserver` | Sample-account build of the TUI and the loopback WebSocket bridge used by `make live`. Not part of the public site. |
| `cmd/releasetool` | Version bumps, checksums, and release asset and metadata checks. |
| `internal/app` | CLI commands (`cli.go`, `extended.go`), TUI (`tui*.go`), account store (`store.go`, `model.go`), credentials and OS vaults (`credentials.go`, `vault*.go`, `keychain_darwin.go`, `credential_*.go`), OAuth (`oauth.go`), quota (`quota.go`, `display.go`), verified warm-up (`warmup.go`), history and logs, backups, doctor, statusline, metrics, targets, and self-update (`updater.go`). |
| `internal/store` | Private directories, atomic write-then-rename, and per-OS file locks (`flock` on Unix, `LockFileEx` on Windows). |
| `internal/config` | Paths, the private directory mode, and semantic version comparison, shared by `internal/app` and `internal/store`. |
| `internal/client` | Release tag normalization and checksum lookup used by the updater. |
| `site/` | Marketing and install site (React + Vite), served by one Nginx container. |

## Data and credentials

- Account metadata and settings are JSON files under the agy-swap config directory, written atomically under a file lock.
- Saved account tokens go to `vault.json` in the config directory (`0600`, not encrypted). `AGY_SWAP_VAULT=keychain` uses the OS credential store instead (macOS Keychain through Security.framework, Windows Credential Manager, Linux Secret Service through `secret-tool`); by default that store is read only for older tokens. `agy-swap doctor` reports the vault in use, accounts that still hold legacy plaintext tokens, and secrets it cannot read.
- Switching snapshots the shared local Antigravity session files, writes the new ones, and restores the snapshot if a step fails.
- History is local JSONL trimmed by size and age; backups are JSON, and `--include-secrets` exports are encrypted with a passphrase.

## Network

The CLI talks to Google for sign-in, account, and quota data, and to GitHub for release checks and updates. It sends no telemetry.

`QuotaService.Fetch` loads the project and calls `retrieveUserQuotaSummary` through the production `cloudcode-pa.googleapis.com` endpoint. Generation and model discovery continue using the existing daily endpoint. The production summary supplies Gemini weekly/5h buckets; daily summaries may contain only third-party buckets. HEALTH retains its weekly/5h percentage/reset format and details focus on Gemini 3.8 High within the shared Gemini group. Server `disabled` and description metadata are preserved: disabled windows show `off`/`Disabled` and are excluded from quota waits, auto-next inputs and warm-up eligibility. Ranking formulas and switching thresholds are unchanged. Fetch failures or missing Gemini windows preserve the previous snapshot; catalog quota is never used as a substitute. Legacy catalog snapshots remain readable, but bypass the cache TTL so the next refresh retrieves production timed windows.

## Warm-up

CLI `warmup` and TUI `w` call the same service in `warmup.go`. The service validates account credentials, resolves the Code Assist project and Gemini 3.8 Flash Low model from the account catalog, saves a fresh 5h quota baseline, and issues one generation request. The transport disables redirect replay and chooses the configured TLS mode before sending, so generation errors never trigger another prompt.

A completed text reply is required before verification. Up to three quota reads, spaced two seconds apart and bounded by a 15-second verification deadline, compare the live remaining fraction with the baseline. Only a decrease is verified; quota percentages and reset times are never synthesized. CLI JSON includes the requested and wire model IDs, `sent`, `verified`, before/after quota, and the latest successfully saved snapshot. Unverified or failed operations return exit code 1.

Refreshed credentials are written to a new vault reference before saving account metadata. The previous reference remains valid until that save succeeds. Store conflicts propagate without overwriting concurrent edits. TUI queues warm-up behind an in-flight refresh, prevents competing jobs and account mutations during warm-up, and reloads persisted results on the event loop after each account. When a completed reply still has a full 5h snapshot, persisted `warmup_quota_pending_until` makes the TUI show syncing instead of a stale 100% for that bucket until a live decrease is observed or the five-hour window expires. A forced quota refresh after the job bypasses cache without starting another warm-up cycle. Quota values are never synthesized.

After an automatic or manual TUI refresh (including the initial refresh, `r`, and the refresh command), the app takes P=1 and P=2 from `tuiAutoNextRanks`, after auto-next settles. It warms only those accounts whose quota was successfully fetched in that refresh and whose explicit Gemini 5h buckets are all exactly 100%. Cache hits and failed fetches cannot trigger warm-up; accounts below P=2 never substitute for a top account. Busy UI states, unresolved active identity, session changes, demo mode, and store/storage failures skip the cycle.

Automatic warm-ups run sequentially in one cancellable job and recheck a fresh baseline before sending. Every warm-up, including manual warm-up, persists `warmup_not_before` before generation. Automatic attempts require both the current time and a successful quota observation to be past that guard: at least five hours after reservation, or the baseline reset if later. This survives restarts and uncertain delivery, and avoids relying on quota synchronization or stable reset timestamps. Failed reservation/conflicting writes abort before generation; failures on one top account do not select a replacement or prevent checking the other. Manual warm-up remains an explicit action and can bypass the automatic guard. Invalid quota fractions are rejected rather than clamped into a full bucket.

## TUI

The TUI draws with ANSI escape sequences in raw terminal mode, without a UI framework. Layout switches between wide, stacked, and compact modes by terminal size (down to 28×12, checked by `make tui-smoke`). The website's terminal frames are rendered by the same code: `TestSiteStoryFrames` regenerates `site/src/generated/tui-story-frames.json`.
