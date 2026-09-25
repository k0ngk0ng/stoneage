$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$testStage = Join-Path $root ('build\installer-' + [guid]::NewGuid())
$previousHome = $env:SACTL_INSTALL_HOME
$previousConfig = $env:XDG_CONFIG_HOME
$previousState = $env:XDG_STATE_HOME
try {
    $fixture = Join-Path $testStage 'cdn'
    $bundle = Join-Path $testStage 'stoneage-sactl-v0.1.99-windows-amd64'
    New-Item -ItemType Directory -Force $fixture, $bundle, (Join-Path $bundle 'skills') | Out-Null
    Set-Content (Join-Path $bundle 'sactl.exe') 'fixture binary'
    Copy-Item "$root\config\sactl\sactl.toml.example" $bundle
    Copy-Item -Recurse "$root\.agents\skills\sactl" (Join-Path $bundle 'skills')
    $archive = 'stoneage-sactl-v0.1.99-windows-amd64.zip'
    Compress-Archive $bundle (Join-Path $fixture $archive)
    $hash = (Get-FileHash (Join-Path $fixture $archive)).Hash.ToLowerInvariant()
    Set-Content (Join-Path $fixture 'SHA256SUMS') "$hash  ./$archive"
    function global:Invoke-WebRequest {
        param([switch]$UseBasicParsing, [Parameter(Position=0)][string]$Uri, [string]$OutFile)
        if (-not $Uri.StartsWith('https://cdn.example/game/downloads/sactl/v0.1.99/')) { throw 'Unexpected download URL' }
        $file = Join-Path $fixture ($Uri.Split('/')[-1])
        if ($OutFile) { Copy-Item $file $OutFile } else { return @{ Content = (Get-Content $file -Raw) } }
    }
    $env:SACTL_INSTALL_HOME = Join-Path $testStage 'user'
    $env:XDG_CONFIG_HOME = Join-Path $testStage 'config'
    $env:XDG_STATE_HOME = Join-Path $testStage 'state'
    $prefix = Join-Path $testStage 'bin'
    & "$PSScriptRoot\install-sactl.ps1" -Download -Version v0.1.99 -CdnBase https://cdn.example/game -Prefix $prefix
    foreach ($agent in @('.agents','.claude')) {
        if (-not (Test-Path "$env:SACTL_INSTALL_HOME\$agent\skills\sactl\SKILL.md")) { throw 'Missing installed skill' }
    }
    $config = Join-Path $env:XDG_CONFIG_HOME 'sactl\sactl.toml'
    if (-not (Test-Path $config)) { throw 'Missing discoverable config' }
    Set-Content $config 'user config'
    & "$PSScriptRoot\install-sactl.ps1" -Download -Version v0.1.99 -CdnBase https://cdn.example/game -Prefix $prefix
    if ((Get-Content $config -Raw).Trim() -ne 'user config') { throw 'Config overwritten' }
    Set-Content (Join-Path $fixture $archive) 'tampered archive'
    $rejected = $false
    try { & "$PSScriptRoot\install-sactl.ps1" -Download -Version v0.1.99 -CdnBase https://cdn.example/game -Prefix $prefix } catch { $rejected = $_.Exception.Message -like '*SHA-256 mismatch*' }
    if (-not $rejected) { throw 'Accepted tampered archive' }
    Write-Host 'Windows CDN install, skills, config preservation and checksum rejection passed'
} finally {
    Remove-Item Function:\Invoke-WebRequest -ErrorAction SilentlyContinue
    $env:SACTL_INSTALL_HOME = $previousHome
    $env:XDG_CONFIG_HOME = $previousConfig
    $env:XDG_STATE_HOME = $previousState
    if (Test-Path $testStage) { Remove-Item -Recurse -Force $testStage }
}
