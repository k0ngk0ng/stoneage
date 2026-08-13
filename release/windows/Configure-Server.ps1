param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^(?:\d{1,3}\.){3}\d{1,3}$')]
    [string]$IPv4,

    [ValidateRange(1, 65535)]
    [int]$Port = 9065
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

& $Python.Source $Patcher --source $Source --output $Output `
    --host $IPv4 --port $Port --bypass-wgs
if ($LASTEXITCODE -ne 0) {
    throw "Client patching failed with exit code $LASTEXITCODE."
}
Write-Host "Configured client for ${IPv4}:${Port}."

