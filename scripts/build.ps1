# Build and package bindmount for Windows.
# Run from the repository root or from any directory with -ProjectRoot.

[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path,
    [switch]$Clean,
    [switch]$Test,
    [switch]$Vet,
    [switch]$Release
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $ProjectRoot

$dist = Join-Path $ProjectRoot 'dist'
$releaseDir = Join-Path $ProjectRoot 'release'

function Invoke-Go([string[]]$Arguments) {
    & go @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

if ($Clean) {
    Remove-Item -LiteralPath $dist, $releaseDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $ProjectRoot 'bin') -Recurse -Force -ErrorAction SilentlyContinue
}

if ($Test) {
    if ($IsWindows) {
        Invoke-Go @('test', './...')
    } else {
        $oldGoOS = $env:GOOS
        $env:GOOS = 'windows'
        try { Invoke-Go @('test', '-exec=true', './...') } finally { $env:GOOS = $oldGoOS }
    }
}

if ($Vet) {
    Invoke-Go @('vet', './...')
}

if (-not $Test -and -not $Vet -or $Release) {
    New-Item -ItemType Directory -Path $dist -Force | Out-Null
    $oldGoOS = $env:GOOS
    $oldGoARCH = $env:GOARCH
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    try { Invoke-Go @('build', '-o', (Join-Path $dist 'bindmount.exe'), './cmd/bindmount') }
    finally {
        $env:GOOS = $oldGoOS
        $env:GOARCH = $oldGoARCH
    }
    Copy-Item -LiteralPath (Join-Path $ProjectRoot 'scripts/bindmount-gui.ps1') -Destination $dist -Force
}

if ($Release) {
    Remove-Item -LiteralPath $releaseDir -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Path $releaseDir -Force | Out-Null
    Compress-Archive -Path (Join-Path $dist '*') -DestinationPath (Join-Path $releaseDir 'bindmount-windows-amd64.zip') -Force
}
