param(
    [string]$Server = "127.0.0.1",
    [int]$Port = 9065,
    [switch]$LocalGateway,
    [string]$ServersFile,
    [ValidatePattern('^https?://')]
    [string]$ServersUrl
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$ClientRoot = Join-Path $Root "client"
$SourceExe = Join-Path $ClientRoot "sa_2903.exe"
$ClientExe = Join-Path $ClientRoot "sa_2903-local.exe"
$Patcher = Join-Path $Root "patch-legacy-client.py"
$DefaultServersFile = Join-Path $Root "client-servers.toml"
$GatewayExe = Join-Path $Root "bin\stoneage-gateway.exe"
$PidFile = Join-Path $Root "gateway.pid"
$LogFile = Join-Path $Root "gateway.log"

# A launcher invocation may select a different address or server list.  Build
# the runnable copy on demand from the immutable archived executable; never
# patch sa_2903.exe in place.
if (-not $ServersFile -and (Test-Path -LiteralPath $DefaultServersFile -PathType Leaf)) {
    $ServersFile = $DefaultServersFile
}
$NeedsPatch = (-not (Test-Path -LiteralPath $ClientExe -PathType Leaf)) -or
    ($Server -ne "127.0.0.1") -or ($Port -ne 9065) -or $ServersFile -or $ServersUrl
if ($NeedsPatch) {
    if (-not (Test-Path -LiteralPath $SourceExe -PathType Leaf)) {
        throw "Missing archived client: $SourceExe"
    }
    if (-not (Test-Path -LiteralPath $Patcher -PathType Leaf)) {
        throw "Missing client launcher patcher: $Patcher"
    }
    $Python = Get-Command python -ErrorAction SilentlyContinue
    if (-not $Python) {
        $Python = Get-Command python3 -ErrorAction SilentlyContinue
    }
    if (-not $Python) {
        throw "Python 3 is required to create the runnable client copy."
    }
    $PatchArguments = @(
        "--source", $SourceExe,
        "--output", $ClientExe,
        "--host", $Server,
        "--port", $Port,
        "--bypass-wgs"
    )
    if ($ServersFile) {
        if (-not (Test-Path -LiteralPath $ServersFile -PathType Leaf)) {
            throw "Server list config does not exist: $ServersFile"
        }
        $PatchArguments += @("--servers-file", (Resolve-Path -LiteralPath $ServersFile).Path)
    }
    if ($ServersUrl) {
        $PatchArguments += @("--servers-url", $ServersUrl)
    }
    & $Python.Source $Patcher @PatchArguments
    if ($LASTEXITCODE -ne 0) {
        throw "Client launcher preparation failed with exit code $LASTEXITCODE."
    }
}
if (-not (Test-Path -LiteralPath $ClientExe -PathType Leaf)) {
    throw "Missing runnable client: $ClientExe"
}

if ($LocalGateway) {
    if (-not (Test-Path $GatewayExe)) {
        throw "Missing gateway: $GatewayExe"
    }
    $Process = Start-Process -FilePath $GatewayExe `
        -ArgumentList @("-listen", "127.0.0.1:9065", "-upstream", "127.0.0.1:19065") `
        -WorkingDirectory $Root -RedirectStandardOutput $LogFile `
        -RedirectStandardError (Join-Path $Root "gateway-error.log") -PassThru
    Set-Content -Path $PidFile -Value $Process.Id -Encoding ascii
}

Start-Process -FilePath $ClientExe -WorkingDirectory $ClientRoot `
    -ArgumentList @("updated", "realbin:15", "adrnbin:15", "sprbin:4", "spradrnbin:5", "encode:108")
