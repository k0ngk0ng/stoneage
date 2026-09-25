# Install the Windows client and both standard Agent skills. No Python required.
param(
    [string]$Prefix = "$env:LOCALAPPDATA\Programs\sactl",
    [switch]$AddToPath,
    [switch]$Download,
    [string]$Version = '',
    [string]$CdnBase = '',
    [switch]$NoSkills
)
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$bundle = $PSScriptRoot
$temporary = $null
try {
    if ($Download) {
        if ($CdnBase -and $CdnBase -notmatch '^https://') { throw 'CDN URL must use HTTPS' }
        $CdnBase = $CdnBase.TrimEnd('/')
        if (-not $Version) {
            if ($CdnBase) { $Version = (Invoke-RestMethod "$CdnBase/downloads/sactl/latest.json").version }
            else { $Version = (Invoke-RestMethod 'https://api.github.com/repos/k0ngk0ng/stoneage/releases/latest').tag_name }
        }
        if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$') { throw 'Invalid release version' }
        if (-not [Environment]::Is64BitOperatingSystem) { throw '64-bit Windows required' }
        # The published Windows x64 binary also runs under Windows ARM64 emulation.
        $archive = "stoneage-sactl-$Version-windows-amd64.zip"
        $base = "https://github.com/k0ngk0ng/stoneage/releases/download/$Version"
        if ($CdnBase) { $base = "$CdnBase/downloads/sactl/$Version" }
        $temporary = Join-Path ([IO.Path]::GetTempPath()) ('sactl-' + [guid]::NewGuid())
        New-Item -ItemType Directory $temporary | Out-Null
        $zip = Join-Path $temporary $archive
        Invoke-WebRequest -UseBasicParsing "$base/$archive" -OutFile $zip
        $sums = (Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS").Content
        $expected = @($sums -split "`n" | ForEach-Object {
            if ($_ -match '^([a-f0-9]{64})\s+(.+?)\s*$' -and $Matches[2].Replace('./','') -eq $archive) { $Matches[1] }
        })
        if ($expected.Count -ne 1 -or (Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected[0]) { throw 'Archive SHA-256 mismatch; nothing installed' }
        Expand-Archive -Path $zip -DestinationPath $temporary
        $bundle = Join-Path $temporary "stoneage-sactl-$Version-windows-amd64"
    }
    $source = Join-Path $bundle 'sactl.exe'
    $template = Join-Path $bundle 'sactl.toml.example'
    $skill = Join-Path $bundle 'skills\sactl'
    if (-not (Test-Path $source) -or -not (Test-Path $template)) { throw 'Incomplete package; use -Download or run from a release archive' }
    if (-not $NoSkills -and -not (Test-Path (Join-Path $skill 'SKILL.md'))) { throw 'Package has no skill; use a newer release or -NoSkills' }
    $userHome = if ($env:SACTL_INSTALL_HOME) { $env:SACTL_INSTALL_HOME } else { [Environment]::GetFolderPath('UserProfile') }
    $skillTargets = @((Join-Path $userHome '.agents\skills\sactl'), (Join-Path $userHome '.claude\skills\sactl'))
    if (-not $NoSkills) {
        foreach ($destination in $skillTargets) {
            $check = $destination
            while ($check) {
                if ((Test-Path $check) -and ((Get-Item -Force $check).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "Refusing linked destination: $check" }
                $check = Split-Path $check -Parent
            }
            if ((Test-Path $destination) -and (Get-ChildItem -Force -Recurse $destination | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint })) { throw "Refusing linked skill files: $destination" }
        }
    }
    New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
    $target = Join-Path $Prefix 'sactl.exe'
    Copy-Item -Force $source $target
    Unblock-File -Path $target
    # Match sactl.ConfigSearchPaths and StatePath, including explicit XDG overrides.
    $configBase = if ($env:XDG_CONFIG_HOME) { $env:XDG_CONFIG_HOME } else { Join-Path $userHome '.config' }
    $stateBase = if ($env:XDG_STATE_HOME) { $env:XDG_STATE_HOME } else { Join-Path $userHome '.local\state' }
    $configDir = Join-Path $configBase 'sactl'
    $stateDir = Join-Path $stateBase 'sactl'
    New-Item -ItemType Directory -Force -Path $configDir, $stateDir | Out-Null
    $configPath = Join-Path $configDir 'sactl.toml'
    if (-not (Test-Path $configPath)) {
        $socket = (Join-Path $stateDir 'sactl.sock').Replace('\','/').Replace('"','\"')
        $config = (Get-Content $template -Raw).Replace('runtime/sactl/sactl.sock', $socket)
        [IO.File]::WriteAllText($configPath, $config, (New-Object Text.UTF8Encoding $false))
    }
    if (-not $NoSkills) {
        foreach ($destination in $skillTargets) {
            New-Item -ItemType Directory -Force $destination | Out-Null
            Copy-Item -Recurse -Force (Join-Path $skill '*') $destination
            Write-Host "Skill installed: $destination"
        }
    }
    if ($AddToPath) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if ($Prefix -notin ($userPath -split ';')) { [Environment]::SetEnvironmentVariable('Path', "$userPath;$Prefix", 'User') }
        Write-Host 'Open a new terminal to use the updated PATH.'
    }
    Write-Host "Installed: $target"
    Write-Host "Config: $configPath"
    Write-Host 'Configure the game address/account/character, then run sactl serve. No game login was started.'
} finally {
    if ($temporary -and (Test-Path $temporary)) { Remove-Item -Recurse -Force $temporary }
}
