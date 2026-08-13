$ErrorActionPreference = "SilentlyContinue"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$PidFile = Join-Path $Root "gateway.pid"

Get-Process -Name "sa_2903-local" | Stop-Process
if (Test-Path $PidFile) {
    $GatewayPid = [int](Get-Content $PidFile -Raw)
    Stop-Process -Id $GatewayPid
    Remove-Item $PidFile
}
Write-Host "StoneAge client and packaged gateway have been stopped."

