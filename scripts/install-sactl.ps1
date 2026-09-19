# Install the sactl client on Windows.
#
#   powershell -ExecutionPolicy Bypass -File install-sactl.ps1
#   powershell -ExecutionPolicy Bypass -File install-sactl.ps1 -AddToPath
#
# Windows marks files that came from the internet with the Mark-of-the-Web and
# then warns on first run. This script unblocks the binary it installs, which
# is the Windows equivalent of clearing com.apple.quarantine on macOS.
param(
    [string]$Prefix = "$env:LOCALAPPDATA\Programs\sactl",
    [switch]$AddToPath
)

$ErrorActionPreference = 'Stop'
$source = Join-Path $PSScriptRoot 'sactl.exe'
if (-not (Test-Path $source)) {
    throw "sactl.exe not found next to this script; run it from the extracted release archive"
}

New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
$target = Join-Path $Prefix 'sactl.exe'
Copy-Item -Force $source $target
Unblock-File -Path $target

$configDir = Join-Path $env:APPDATA 'sactl'
$stateDir = Join-Path $env:LOCALAPPDATA 'sactl'
New-Item -ItemType Directory -Force -Path $configDir, $stateDir | Out-Null
$configPath = Join-Path $configDir 'sactl.toml'
$template = Join-Path $PSScriptRoot 'sactl.toml.example'
if ((Test-Path $template) -and -not (Test-Path $configPath)) {
    (Get-Content $template -Raw).
        Replace('runtime/sactl/sactl.sock', ($stateDir -replace '\\', '/') + '/sactl.sock') |
        Set-Content -Path $configPath -Encoding utf8
}

if ($AddToPath) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($userPath -notlike "*$Prefix*") {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$Prefix", 'User')
        Write-Host "added $Prefix to the user PATH (open a new terminal to pick it up)"
    }
}

Write-Host "installed: $target"
Write-Host "config:    $configPath"
Write-Host "state:     $stateDir"
Write-Host ''
Write-Host 'next:'
Write-Host '  1. edit the config: gateway address, account, password, character, and'
Write-Host '     map_directory (a copy of the 2.5 server data directory)'
Write-Host '  2. check the client:  sactl version'
Write-Host '  3. start the session holder:  sactl serve'
Write-Host '  4. drive it from another terminal:  sactl status / observe / goto ...'
