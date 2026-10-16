<#
.SYNOPSIS
  Build a standalone experimental Windows binary and its SHA256/provenance manifest.
.DESCRIPTION
  Reads VERSION from Makefile. Does not install, modify PATH, or access accounts.
  The manifest records the actual worktree, including uncommitted changes.
#>
[CmdletBinding()]
param(
  [string]$BuildId = 'upstream-experiment',
  [string]$BaseRef = 'custom-build',
  [string]$UpstreamRef = 'ee3a0058f5fbba5eb2beceef91970c7c73d9b9ee',
  [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if ($BuildId -notmatch '^[a-zA-Z0-9._-]+$') { throw 'Invalid build ID' }

function Get-GitText {
  param([string[]]$GitArgs)
  $result = & git -C $repoRoot @GitArgs
  if ($LASTEXITCODE -ne 0) { throw "git $($GitArgs -join ' ') failed" }
  return ($result -join "`n").TrimEnd()
}

function Get-SourceDigest {
  $paths = @(git -C $repoRoot ls-files -co --exclude-standard)
  if ($LASTEXITCODE -ne 0) { throw 'Cannot enumerate source files' }
  # Executables and their backup copies are artifacts, not build inputs.
  [string[]]$paths = @($paths | Where-Object { $_ -notmatch '\.exe(?:\.[^/\\]*)?$' } | Select-Object -Unique)
  [Array]::Sort($paths, [StringComparer]::Ordinal)
  $hash = [Security.Cryptography.IncrementalHash]::CreateHash([Security.Cryptography.HashAlgorithmName]::SHA256)
  try {
    foreach ($relativePath in $paths) {
      $hash.AppendData([Text.Encoding]::UTF8.GetBytes($relativePath))
      $hash.AppendData([byte[]]@(0))
      $path = Join-Path $repoRoot $relativePath
      if (Test-Path -LiteralPath $path -PathType Leaf) {
        $hash.AppendData([byte[]]@(1))
        $hash.AppendData([IO.File]::ReadAllBytes($path))
      } else {
        $hash.AppendData([byte[]]@(0))
      }
      $hash.AppendData([byte[]]@(0))
    }
    return [Convert]::ToHexString($hash.GetHashAndReset()).ToLowerInvariant()
  } finally {
    $hash.Dispose()
  }
}

$versionLine = Select-String -LiteralPath (Join-Path $repoRoot 'Makefile') -Pattern '^VERSION\s*\?=\s*(\S+)' | Select-Object -First 1
if (-not $versionLine) { throw 'VERSION not found in Makefile' }
$version = $versionLine.Matches[0].Groups[1].Value
$baseCommit = Get-GitText -GitArgs @('rev-parse', '--verify', "$BaseRef^{commit}")
$upstreamCommit = Get-GitText -GitArgs @('rev-parse', '--verify', "$UpstreamRef^{commit}")
$sourceCommit = Get-GitText -GitArgs @('rev-parse', 'HEAD')
$branch = Get-GitText -GitArgs @('branch', '--show-current')
$sourceStatus = Get-GitText -GitArgs @('status', '--porcelain=v1', '--untracked-files=all')
$sourceDigest = Get-SourceDigest

if (-not $SkipTests) {
  go -C $repoRoot test ./...
  if ($LASTEXITCODE -ne 0) { throw 'Tests failed; aborting experimental build' }
}

$dist = Join-Path $repoRoot 'dist/experiments'
New-Item -ItemType Directory -Force -Path $dist | Out-Null
$binary = Join-Path $dist 'agy-swap-upstream-improvements.exe'
$manifestPath = Join-Path $dist 'agy-swap-upstream-improvements.manifest.json'
# A failed attempt must never leave a manifest that describes an older binary.
if (Test-Path -LiteralPath $manifestPath) { Remove-Item -LiteralPath $manifestPath }
$originalGoos = $env:GOOS
$originalGoarch = $env:GOARCH
try {
  $env:GOOS = 'windows'
  $env:GOARCH = 'amd64'
  $ldflags = "-s -w -X main.version=$version -X main.buildID=$BuildId"
  go -C $repoRoot build -buildvcs=true -trimpath -ldflags $ldflags -o $binary ./cmd/agy-swap
  if ($LASTEXITCODE -ne 0) { throw 'Experimental build failed' }
  go -C $repoRoot run ./cmd/releasetool verify-experiment $version $BuildId $binary $repoRoot
  if ($LASTEXITCODE -ne 0) { throw 'Experimental binary identity/provenance verification failed' }
} finally {
  $env:GOOS = $originalGoos
  $env:GOARCH = $originalGoarch
}

$finalStatus = Get-GitText -GitArgs @('status', '--porcelain=v1', '--untracked-files=all')
$finalDigest = Get-SourceDigest
$finalCommit = Get-GitText -GitArgs @('rev-parse', 'HEAD')
if ($finalCommit -ne $sourceCommit -or $finalStatus -cne $sourceStatus -or $finalDigest -cne $sourceDigest) {
  throw 'Source changed during the build; rerun after edits finish'
}
$buildInfo = & go -C $repoRoot version -m $binary
if ($LASTEXITCODE -ne 0) { throw 'Cannot read binary build information' }
$binaryVersion = (& $binary version) -join ' '
if ($LASTEXITCODE -ne 0) { throw 'Cannot read experimental binary version' }
$manifest = [ordered]@{
  artifact = [System.IO.Path]::GetFileName($binary)
  sha256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
  version = $version
  buildId = $BuildId
  binaryVersion = $binaryVersion
  source = [ordered]@{
    branch = $branch
    commit = $sourceCommit
    baseRef = $BaseRef
    baseCommit = $baseCommit
    upstreamRef = $UpstreamRef
    upstreamCommit = $upstreamCommit
    digestAlgorithm = 'SHA256: ordinal UTF-8 path, NUL, existence byte, content, NUL'
    digestExcluded = @('*.exe', '*.exe.*')
    digest = $sourceDigest
    dirty = ($sourceStatus.Length -gt 0)
    worktreeStatus = $sourceStatus
  }
  buildInfo = @($buildInfo)
}
$manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $manifestPath -Encoding utf8
Write-Host "Built $binaryVersion"
Write-Host "Binary: $binary"
Write-Host "Manifest: $manifestPath"
Write-Host "SHA256: $($manifest.sha256)"
