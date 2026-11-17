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

`QuotaService.Fetch` resolves the Code Assist project and reads daily `retrieveUserQuotaSummary` with the agy-compatible `antigravity/cli/1.4.0` request identity. It takes both Gemini weekly and five-hour buckets directly from `groups[].buckets[]`, matching the primary quota path consumed by agy CLI 1.5.9; unrelated model families are ignored before validation. Bucket IDs identify pools: gemini-weekly/gemini-5h form the primary gemini pool, while gemini-argon-wtu-weekly belongs to gemini_argon_wtu. Auxiliary pools retain independent windows and never supply the primary weekly percentage or affect primary account selection. Details render each auxiliary pool separately. The quota_contract marker gemini-pools-v1 invalidates pre-split caches for automation and TTL reuse; the primary extractor also selects exact bucket IDs when displaying legacy caches. `fetchAvailableModels` is only used to resolve generation models, never as a substitute quota source. Valid remaining fractions and server reset times are required for both windows before publishing a snapshot. Five-hour buckets persist `source: daily.retrieveUserQuotaSummary.cli` without `model_id`, and the snapshot persists the same `weekly_source`. Legacy catalog-backed five-hour and unmarked weekly snapshots bypass cache TTL and cannot authorize automatic decisions. Persistence retains legacy metadata for display when refresh fails. Cache, recommendations, auto-next and auto-warmup share provenance checks. Server-disabled metadata is preserved. HEALTH and details consume the same canonical snapshot, with weekly before five-hour. Fetch or save failures retain the previous persisted snapshot. Ranking formulas, switching thresholds and generation endpoints are unchanged.

## Warm-up

CLI `warmup` and TUI `w` call the same service in `warmup.go`. The service validates account credentials, resolves the Code Assist project and Gemini 3.8 Flash Low model from the account catalog, saves a fresh 5h quota baseline, and issues one generation request. The transport disables redirect replay and chooses the configured TLS mode before sending, so generation errors never trigger another prompt.

A completed text reply is required before verification. Up to three quota reads, spaced two seconds apart and bounded by a 15-second verification deadline, compare the live remaining fraction with the baseline. Only a decrease is verified; quota percentages and reset times are never synthesized. CLI JSON includes the requested and wire model IDs, `sent`, `verified`, before/after quota, and the latest successfully saved snapshot. Unverified or failed operations return exit code 1.

Refreshed credentials are written to a new vault reference before saving account metadata. The previous reference remains valid until that save succeeds. Store conflicts propagate without overwriting concurrent edits. TUI queues warm-up behind an in-flight refresh, prevents competing jobs and account mutations during warm-up, and reloads persisted results on the event loop after each account. When a completed reply still has a full 5h snapshot, persisted `warmup_quota_pending_until` makes the TUI show syncing instead of a stale 100% for that bucket until a live decrease is observed or the five-hour window expires. A forced quota refresh after the job bypasses cache without starting another warm-up cycle. Quota values are never synthesized.

After an automatic or manual TUI refresh (including the initial refresh, `r`, and the refresh command), the app takes P=1 and P=2 from `tuiAutoNextRanks`, after auto-next settles. It warms only those accounts whose quota was successfully fetched in that refresh and whose explicit Gemini 5h buckets are all exactly 100%. Cache hits and failed fetches cannot trigger warm-up; accounts below P=2 never substitute for a top account. Busy UI states, unresolved active identity, session changes, demo mode, and store/storage failures skip the cycle.

Automatic warm-ups run sequentially in one cancellable job and recheck a fresh baseline before sending. Every warm-up, including manual warm-up, persists `warmup_not_before` before generation. Automatic attempts require both the current time and a successful quota observation to be past that guard: at least five hours after reservation, or the baseline reset if later. This survives restarts and uncertain delivery, and avoids relying on quota synchronization or stable reset timestamps. Failed reservation/conflicting writes abort before generation; failures on one top account do not select a replacement or prevent checking the other. Manual warm-up remains an explicit action and can bypass the automatic guard. Invalid quota fractions are rejected rather than clamped into a full bucket.

## TUI

The TUI draws with ANSI escape sequences in raw terminal mode, without a UI framework. Layout switches between wide, stacked, and compact modes by terminal size (down to 28×12, checked by `make tui-smoke`). The website's terminal frames are rendered by the same code: `TestSiteStoryFrames` regenerates `site/src/generated/tui-story-frames.json`.
