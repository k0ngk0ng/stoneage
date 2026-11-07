# Run only in a disposable GitHub-hosted runner; Scoop state stays in this checkout.
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
if ($env:GITHUB_ACTIONS -ne 'true') { throw 'Use a disposable CI runner' }
$root = $env:GITHUB_WORKSPACE
$env:SCOOP = [IO.Path]::GetFullPath("$root/.cache/scoop")
New-Item -ItemType Directory -Path "$root/.cache" -Force | Out-Null
Invoke-WebRequest https://get.scoop.sh -OutFile "$root/.cache/install-scoop.ps1"
& "$root/.cache/install-scoop.ps1" -RunAsAdmin -ScoopDir $env:SCOOP
if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) { throw 'Scoop bootstrap failed' }
$env:PATH = "$env:SCOOP/shims;$env:PATH"
& scoop install "$root/bucket/sactl.json"
if ($LASTEXITCODE -ne 0) { throw 'Scoop installation failed' }
$binary = "$env:SCOOP/apps/sactl/current/sactl.exe"
$lock = Get-Content "$root/packaging/sactl-catalog/release-lock.json" -Raw | ConvertFrom-Json
if ((& $binary version) -notmatch [regex]::Escape($lock.version)) { throw 'Incorrect installed sactl version' }
foreach ($command in @('ai', 'arena')) {
    $output = & $binary $command --help
    if ($LASTEXITCODE -ne 0 -or ($output -join "`n") -notmatch "sactl $command") { throw "Installed $command help failed" }
}
if (!(Test-Path "$env:SCOOP/apps/sactl/current/completions/sactl.bash")) { throw 'Missing completion' }
& scoop uninstall sactl
if ($LASTEXITCODE -ne 0) { throw 'Scoop uninstall failed' }
