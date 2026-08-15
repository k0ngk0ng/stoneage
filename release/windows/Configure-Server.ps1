param(
    [ValidatePattern('^(?:\d{1,3}\.){3}\d{1,3}$')]
    [string]$IPv4 = "127.0.0.1",

    [ValidateRange(1, 65535)]
    [int]$Port = 9065,

    [string]$ServersFile,

    [ValidatePattern('^https?://')]
    [string]$ServersUrl
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$Python = Get-Command python -ErrorAction SilentlyContinue
if (-not $Python) {
    $Python = Get-Command py -ErrorAction SilentlyContinue
}
if (-not $Python) {
    throw "Python 3 is required to configure a remote server address."
}

$Source = Join-Path $Root "client\sa_2903.exe"
$Output = Join-Path $Root "client\sa_2903-local.exe"
$Patcher = Join-Path $Root "patch-legacy-client.py"

$Arguments = @(
    "--source", $Source,
    "--output", $Output,
    "--host", $IPv4,
    "--port", $Port,
    "--bypass-wgs"
)
if ($ServersFile) {
    if (-not (Test-Path -LiteralPath $ServersFile -PathType Leaf)) {
        throw "Server list config does not exist: $ServersFile"
    }
    $Arguments += @("--servers-file", (Resolve-Path -LiteralPath $ServersFile).Path)
}
if ($ServersUrl) {
    $Arguments += @("--servers-url", $ServersUrl)
}

& $Python.Source $Patcher @Arguments
if ($LASTEXITCODE -ne 0) {
    throw "Client patching failed with exit code $LASTEXITCODE."
}
if ($ServersFile -or $ServersUrl) {
    if ($ServersUrl) {
        Write-Host "Configured client from server list $ServersUrl."
    } else {
        Write-Host "Configured client from server list $ServersFile."
    }
} else {
    Write-Host "Configured client for ${IPv4}:${Port}."
}
