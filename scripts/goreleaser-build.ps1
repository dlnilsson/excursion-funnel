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
    [string]$Image = 'ghcr.io/goreleaser/goreleaser-cross@sha256:3ce3506ee9179c4122ba0b5dc13ab564ff259fb65f45bfad005ddd5e4a3d326d'
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
$toolchainPath = Join-Path $tempRoot 'toolchain'
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

    # The image may ship an older Go than go.mod requires. Stage the matching
    # Linux toolchain and its checksum database entries so the container can
    # switch to it offline.
    Write-Host 'Preparing Linux Go toolchain...'
    Push-Location $repoRoot
    try {
        $goVersion = & $go.Source list -m -f '{{.GoVersion}}'
        if ($LASTEXITCODE -ne 0) {
            throw 'Could not read the Go version from go.mod.'
        }
        $toolchainVersion = "v0.0.1-go$goVersion.linux-amd64"
        $toolchain = & $go.Source mod download -json "golang.org/toolchain@$toolchainVersion" | Out-String | ConvertFrom-Json
        if ($LASTEXITCODE -ne 0) {
            throw "Could not download Go toolchain $toolchainVersion."
        }
        $sumdbSource = Join-Path (& $go.Source env GOMODCACHE) 'cache/download/sumdb/sum.golang.org'
    }
    finally {
        Pop-Location
    }
    $toolchainCache = Join-Path $toolchainPath 'cache/download/golang.org/toolchain/@v'
    $sumdbCache = Join-Path $toolchainPath 'cache/download/sumdb/sum.golang.org'
    $null = New-Item -ItemType Directory -Force -Path $toolchainCache, (Join-Path $sumdbCache 'lookup/golang.org')
    Copy-Item -LiteralPath $toolchain.Info, $toolchain.GoMod, $toolchain.Zip -Destination $toolchainCache
    Copy-Item -LiteralPath (Join-Path $sumdbSource "lookup/golang.org/toolchain@$toolchainVersion") -Destination (Join-Path $sumdbCache 'lookup/golang.org')
    Copy-Item -Recurse -LiteralPath (Join-Path $sumdbSource 'tile') -Destination $sumdbCache

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
        --mount "type=bind,source=$toolchainPath,target=/toolchain" `
        --workdir /workspace `
        --env GOMODCACHE=/toolchain `
        --env GOPROXY=off `
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
