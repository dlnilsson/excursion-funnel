<#
.SYNOPSIS
Builds excursion-funnel for Linux x86-64 from Windows using WSL.

.EXAMPLE
.\scripts\build-linux-amd64.ps1

.EXAMPLE
.\scripts\build-linux-amd64.ps1 -Distro Ubuntu -Output artifacts\ef
#>
[CmdletBinding()]
param(
    [string]$Distro,
    [string]$Output = 'dist\ef-linux-amd64'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$wsl = Get-Command wsl.exe -ErrorAction SilentlyContinue
if ($null -eq $wsl) {
    throw 'wsl.exe was not found. Install WSL and an x86-64 Linux distribution first.'
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$outputPath = if ([IO.Path]::IsPathRooted($Output)) {
    [IO.Path]::GetFullPath($Output)
} else {
    [IO.Path]::GetFullPath((Join-Path $repoRoot $Output))
}

$outputDirectory = Split-Path -Parent $outputPath
$null = New-Item -ItemType Directory -Force -Path $outputDirectory

$wslOptions = @()
if (-not [string]::IsNullOrWhiteSpace($Distro)) {
    $wslOptions += @('--distribution', $Distro)
}

function Convert-ToWslPath {
    param([Parameter(Mandatory)][string]$WindowsPath)

    $arguments = @($wslOptions) + @('--', 'wslpath', '-a', $WindowsPath)
    $lines = & $wsl.Source @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Could not translate Windows path for WSL: $WindowsPath"
    }
    $translated = ($lines | Select-Object -First 1).Trim()
    if ([string]::IsNullOrWhiteSpace($translated)) {
        throw "WSL returned an empty path for: $WindowsPath"
    }
    return $translated
}

$wslRepoRoot = Convert-ToWslPath $repoRoot
$wslOutputPath = Convert-ToWslPath $outputPath

$buildScript = @'
set -eu

repo_root=$1
output_path=$2

case "${WSL_DISTRO_NAME:-}" in
    docker-desktop*)
        echo "error: Docker Desktop's internal WSL distribution is not a development environment" >&2
        echo "install Ubuntu with: wsl.exe --install --distribution Ubuntu" >&2
        echo "then rerun this script with: -Distro Ubuntu" >&2
        exit 1
        ;;
esac

if [ "$(uname -m)" != "x86_64" ]; then
    echo "error: this build requires an x86-64 WSL distribution; found $(uname -m)" >&2
    exit 1
fi

for tool in go gcc g++; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "error: $tool is not installed in WSL" >&2
        echo "install Go 1.25+ and run: sudo apt update && sudo apt install build-essential" >&2
        exit 127
    fi
done

cd "$repo_root"
CGO_ENABLED=1 \
GOOS=linux \
GOARCH=amd64 \
CC=gcc \
CXX=g++ \
go build -trimpath -o "$output_path" ./cmd/ef
'@

Write-Host "Building Linux amd64 binary with WSL..."
$arguments = @($wslOptions) + @(
    '--', 'sh', '-lc', $buildScript,
    'build-linux-amd64', $wslRepoRoot, $wslOutputPath
)
& $wsl.Source @arguments
if ($LASTEXITCODE -ne 0) {
    throw "Linux build failed with exit code $LASTEXITCODE."
}
if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
    throw "Build completed without producing the expected file: $outputPath"
}

Write-Host "Built: $outputPath"
