<#
.SYNOPSIS
  Build agy-swap from this checkout and install it on Windows.

.DESCRIPTION
  Windows equivalent of `make install`. The Makefile targets assume a Unix shell
  (`mkdir -p`, `install -m 755`, `/tmp` GOCACHE) and produce `agy-swap` without
  the `.exe` suffix, so they cannot replace the installed `agy-swap.exe`.

  Steps:
    1. Read VERSION from the Makefile (single source; `make bump` / releasetool update it).
    2. Optionally run `go test ./...`.
    3. Build with the same ldflags as `make build` into a temp directory.
    4. Verify the built binary reports the expected version.
    5. Install to ONE location, %LOCALAPPDATA%\Programs\agy-swap by default (the
       same directory as the release installer install.ps1, so `agy-swap update`
       and this script manage the same binary). The installed exe is renamed to
       `agy-swap.exe.<old-version>.bak` (Windows allows renaming a running exe, so
       an open TUI does not block the install) and the new binary is copied in.
    6. Warn about any other agy-swap copy on PATH that would shadow the install
       (e.g. an extensionless `agy-swap` from `make install`, which Git Bash runs
       first). Those copies are reported, never deleted.

  Running agy-swap processes keep executing the old binary until restarted.

.EXAMPLE
  pwsh -File scripts/install-windows.ps1

.EXAMPLE
  pwsh -File scripts/install-windows.ps1 -SkipTests
#>
[CmdletBinding()]
param(
  [string]$BuildId = 'release',
  [string]$TargetDir = (Join-Path $env:LOCALAPPDATA 'Programs\agy-swap'),
  [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot

$versionLine = Select-String -Path (Join-Path $repoRoot 'Makefile') -Pattern '^VERSION\s*\?=\s*(\S+)' | Select-Object -First 1
if (-not $versionLine) { throw 'VERSION not found in Makefile' }
$version = $versionLine.Matches[0].Groups[1].Value

if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'Go toolchain not found in PATH' }

if (-not $SkipTests) {
  Write-Host '● Running go test ./...' -ForegroundColor Cyan
  go -C $repoRoot test ./...
  if ($LASTEXITCODE -ne 0) { throw 'go test failed; aborting install' }
}

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ('agy-swap-build-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
$built = Join-Path $tmpDir 'agy-swap.exe'

try {
  Write-Host "● Building agy-swap v$version (buildID=$BuildId)" -ForegroundColor Cyan
  $ldflags = "-s -w -X main.version=$version -X main.buildID=$BuildId"
  go -C $repoRoot build -trimpath -ldflags $ldflags -o $built ./cmd/agy-swap
  if ($LASTEXITCODE -ne 0) { throw 'go build failed' }

  $builtVersion = (& $built version) -join ' '
  if ($builtVersion -notmatch [regex]::Escape("v$version")) {
    throw "Built binary reports '$builtVersion', expected v$version"
  }

  New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
  $target = Join-Path $TargetDir 'agy-swap.exe'

  if (Test-Path $target) {
    $oldVersion = 'previous'
    try {
      $oldOutput = (& $target version 2>$null) -join ' '
      if ($oldOutput -match 'v(\d+\.\d+\.\d+)') { $oldVersion = $Matches[1] }
    } catch { }
    $backup = "$target.$oldVersion.bak"
    if (Test-Path $backup) { Remove-Item $backup -Force }
    Move-Item -Path $target -Destination $backup
    Write-Host "  ↳ Previous binary kept as $backup" -ForegroundColor Gray
  }

  Copy-Item -Path $built -Destination $target -Force
  Write-Host "✓ Installed $(& $target version) to $target" -ForegroundColor Green

  # Report other copies on PATH that a shell could run instead of this one.
  $targetFull = [System.IO.Path]::GetFullPath($TargetDir).TrimEnd('\')
  $pathDirs = @(
    [Environment]::GetEnvironmentVariable('Path', 'User') -split ';'
    [Environment]::GetEnvironmentVariable('Path', 'Machine') -split ';'
  ) | Where-Object { $_ } | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') } | Select-Object -Unique
  $shadows = foreach ($dir in $pathDirs) {
    if ($dir -ieq $targetFull) { continue }
    foreach ($name in 'agy-swap.exe', 'agy-swap') {
      $candidate = Join-Path $dir $name
      if (Test-Path $candidate -PathType Leaf) { $candidate }
    }
  }
  if ($shadows) {
    Write-Host '! Other agy-swap copies on PATH may run instead of this install; remove them to keep one location:' -ForegroundColor Yellow
    $shadows | ForEach-Object { Write-Host "    $_" -ForegroundColor Yellow }
  }

  $running = Get-Process -Name 'agy-swap' -ErrorAction SilentlyContinue
  if ($running) {
    Write-Host "! $($running.Count) running agy-swap process(es) still use the old binary; restart them (PID: $($running.Id -join ', '))." -ForegroundColor Yellow
  }
}
finally {
  Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}
