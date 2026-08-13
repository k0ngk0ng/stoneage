param(
    [string]$Server = "127.0.0.1",
    [int]$Port = 9065,
    [switch]$LocalGateway
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$ClientRoot = Join-Path $Root "client"
$ClientExe = Join-Path $ClientRoot "sa_2903-local.exe"
$GatewayExe = Join-Path $Root "bin\stoneage-gateway.exe"
$PidFile = Join-Path $Root "gateway.pid"
$LogFile = Join-Path $Root "gateway.log"

if (-not (Test-Path $ClientExe)) {
    throw "Missing client: $ClientExe"
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

if ($Server -ne "127.0.0.1" -or $Port -ne 9065) {
    Write-Host "This prepatched client targets 127.0.0.1:9065."
    Write-Host "For another server, use the host-specific client supplied by the server owner."
    exit 2
}

Start-Process -FilePath $ClientExe -WorkingDirectory $ClientRoot `
    -ArgumentList @("updated", "realbin:15", "adrnbin:15", "sprbin:4", "spradrnbin:5", "encode:108")

