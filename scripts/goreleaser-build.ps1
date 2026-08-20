<#
.SYNOPSIS
Builds Windows and Linux binaries with GoReleaser from Windows.

.DESCRIPTION
Uses the local UCRT64 GCC toolchain for Windows and an isolated Linux
GoReleaser Cross container for Linux. Docker Desktop must be running.

.EXAMPLE
.\scripts\goreleaser-build.ps1
#>
[CmdletBinding()]
param(
    [string]$Image = 'ghcr.io/goreleaser/goreleaser-cross@sha256:0cf2b7f757b40397d2bef5423adb88d0ac63899e88a9f0c4bbb370d3fb7b2fb5'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$goReleaser = Get-Command goreleaser -ErrorAction SilentlyContinue
if ($null -eq $goReleaser) {
    throw 'goreleaser was not found.'
}
$docker = Get-Command docker -ErrorAction SilentlyContinue
if ($null -eq $docker) {
    throw 'docker was not found. Install and start Docker Desktop first.'
}
$go = Get-Command go -ErrorAction SilentlyContinue
if ($null -eq $go) {
    throw 'go was not found.'
}
$npm = Get-Command npm -ErrorAction SilentlyContinue
if ($null -eq $npm) {
    throw 'npm was not found.'
}

& $docker.Source info --format '{{.ServerVersion}}' *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Desktop is not running.'
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$distPath = Join-Path $repoRoot 'dist'
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempRoot = [IO.Path]::GetFullPath((Join-Path $tempBase "excursion-funnel-goreleaser-$([guid]::NewGuid().ToString('N'))"))
$vendorPath = Join-Path $tempRoot 'vendor'
$originalGoProxy = [Environment]::GetEnvironmentVariable('GOPROXY', 'Process')

try {
    $null = New-Item -ItemType Directory -Force -Path $tempRoot

    Write-Host 'Building Windows amd64 with local UCRT64 GCC...'
    Push-Location $repoRoot
    try {
        & $goReleaser.Source build --snapshot --clean --single-target --output 'dist/ef-windows-amd64.exe'
        if ($LASTEXITCODE -ne 0) {
            throw "Windows GoReleaser build failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        Pop-Location
    }

    # Build a temporary vendor tree strictly from the local module cache. This
    # lets the Linux container compile with no network and read-only source.
    Write-Host 'Preparing Linux dependencies from the local Go module cache...'
    [Environment]::SetEnvironmentVariable('GOPROXY', 'off', 'Process')
    Push-Location $repoRoot
    try {
        & $go.Source mod vendor -o $vendorPath
        if ($LASTEXITCODE -ne 0) {
            throw 'Could not vendor dependencies offline. Run go mod download once, then retry.'
        }
    }
    finally {
        Pop-Location
    }

    $buildCommand = @'
set -eu
tar -C /source --exclude='./dist' --exclude='./vendor' --exclude='./node_modules' -cf - . | tar -C /workspace -xf -
cd /workspace
exec goreleaser build --snapshot --single-target --skip=before --output /output/ef-linux-amd64
'@

    Write-Host 'Building Linux amd64 with GoReleaser Cross...'
    & $docker.Source run --rm --network none --pull never `
        --mount "type=bind,source=$repoRoot,target=/source,readonly" `
        --mount 'type=volume,target=/workspace' `
        --mount "type=bind,source=$distPath,target=/output" `
        --mount "type=bind,source=$vendorPath,target=/workspace/vendor,readonly" `
        --workdir /workspace `
        --env GIT_CONFIG_COUNT=1 `
        --env GIT_CONFIG_KEY_0=safe.directory `
        --env GIT_CONFIG_VALUE_0=/workspace `
        --entrypoint sh `
        $Image -lc $buildCommand

    if ($LASTEXITCODE -ne 0) {
        throw "Linux GoReleaser build failed with exit code $LASTEXITCODE."
    }

    Write-Host 'Built:'
    Write-Host "  $(Join-Path $distPath 'ef-windows-amd64.exe')"
    Write-Host "  $(Join-Path $distPath 'ef-linux-amd64')"
}
finally {
    [Environment]::SetEnvironmentVariable('GOPROXY', $originalGoProxy, 'Process')
    if (Test-Path -LiteralPath $tempRoot) {
        if (-not $tempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected temporary path: $tempRoot"
        }
        Remove-Item -Recurse -Force -LiteralPath $tempRoot
    }
}
