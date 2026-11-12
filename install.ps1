# Installs the latest prowl release on Windows:
#   irm https://raw.githubusercontent.com/tmstorm/prowl/main/install.ps1 | iex
# It goes in %LOCALAPPDATA%\Programs\prowl, or $env:PROWL_INSTALL_DIR, which is
# added to your PATH.
$ErrorActionPreference = "Stop"

$repo = "tmstorm/prowl"
$dir = if ($env:PROWL_INSTALL_DIR) { $env:PROWL_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\prowl" }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$archive = "prowl_windows_$arch.zip"
$base = "https://github.com/$repo/releases/latest/download"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null

$exe = Join-Path $dir "prowl.exe"
$service = Get-ScheduledTask -TaskName "prowl-watch" -ErrorAction SilentlyContinue

try {
    $zip = Join-Path $tmp $archive
    Invoke-WebRequest "$base/$archive" -OutFile $zip -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt") -UseBasicParsing
    $want = Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -like "*  $archive" } | ForEach-Object { ($_ -split "\s+")[0] }
    $got = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want -ne $got) { throw "prowl: the download doesnt match its checksum; not installing" }

    Expand-Archive $zip -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    # A running prowl.exe can't be replaced: stop the background prowl watch
    # first, and start it again after.
    if ($service) {
        Stop-ScheduledTask -TaskName "prowl-watch"
        Get-CimInstance Win32_Process -Filter "Name = 'prowl.exe'" |
            Where-Object { $_.ExecutablePath -eq $exe -and $_.CommandLine -like "* watch*" } |
            ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
        Start-Sleep -Seconds 1
    }
    try {
        Copy-Item (Join-Path $tmp "prowl.exe") $dir -Force
    } catch {
        throw "prowl: $exe is in use; close prowl and run this again"
    }
} finally {
    Remove-Item $tmp -Recurse -Force
}

$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (($path -split ";") -notcontains $dir) {
    [Environment]::SetEnvironmentVariable("Path", "$path;$dir", "User")
    Write-Host "Added $dir to your PATH; open a new terminal to use it"
}
if ($service) {
    Start-ScheduledTask -TaskName "prowl-watch"
    Write-Host "Restarted background notifications"
}
Write-Host "prowl installed to $dir\prowl.exe"
& (Join-Path $dir "prowl.exe") version
