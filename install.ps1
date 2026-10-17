# Installs net-merge on Windows. In PowerShell:
#
#   irm https://raw.githubusercontent.com/rohit267/net-merge/main/install.ps1 | iex
#
# Downloads the newest installer, runs it silently (Windows asks once for admin rights), then starts
# the tray. Set $env:NET_MERGE_NO_START = '1' first to leave the tray off.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # the progress bar makes downloads very slow in Windows PowerShell
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$url = 'https://github.com/rohit267/net-merge/releases/latest/download/net-merge-setup.exe'
$setup = Join-Path $env:TEMP 'net-merge-setup.exe'
Write-Host '==> Downloading net-merge'
Invoke-WebRequest $url -OutFile $setup -UseBasicParsing
Write-Host '==> Installing'
$p = Start-Process $setup -ArgumentList '/SILENT', '/SUPPRESSMSGBOXES', '/NORESTART' -Verb RunAs -Wait -PassThru
Remove-Item $setup -ErrorAction SilentlyContinue
if ($p.ExitCode -ne 0) { throw "the installer failed (exit code $($p.ExitCode))" }

$dir = Join-Path $env:ProgramFiles 'net-merge'
Write-Host "==> Installed $(& (Join-Path $dir 'net-merge.exe') version)"
if ($env:NET_MERGE_NO_START -eq '1') {
    Write-Host '==> Start it from the Start menu: net-merge'
} else {
    Start-Process (Join-Path $dir 'net-merge-tray.exe')
    Write-Host '==> Started: the icon is in the tray (maybe behind the ^ arrow next to the clock)'
}
