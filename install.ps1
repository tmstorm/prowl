# Installs the latest prowl release on Windows:
#   irm https://raw.githubusercontent.com/tmstorm/prowl/main/install.ps1 | iex
# It checks for gh and git first and offers to install what's missing with
# winget, then offers a Nerd Font. prowl goes in %LOCALAPPDATA%\Programs\prowl,
# or $env:PROWL_INSTALL_DIR, which is added to your PATH. $env:PROWL_YES = 1
# answers yes to every question.
$ErrorActionPreference = "Stop"

$repo = "tmstorm/prowl"
$dir = if ($env:PROWL_INSTALL_DIR) { $env:PROWL_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\prowl" }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

# prowl's default theme, Dracula, when the console shows colors.
$e = [char]27
$color = $Host.UI.SupportsVirtualTerminal -and -not $env:NO_COLOR
function Paint($code, $text) { if ($color) { "$e[${code}m$text$e[0m" } else { $text } }
$accent = "1;38;2;189;147;249"; $muted = "38;2;98;114;164"; $eye = "1;38;2;241;250;140"
$talon = "38;2;255;184;108"; $green = "38;2;80;250;123"; $red = "38;2;255;85;85"

function Step($text) { Write-Host ""; Write-Host (Paint $accent $text) }
$check = [char]0x2713; $cross = [char]0x2717
function Ok($text) { Write-Host "  $(Paint $green $check) $text" }
function No($text) { Write-Host "  $(Paint $red $cross) $text" }
function Note($text) { Write-Host "  $(Paint $muted $text)" }
function Die($text) { throw "prowl: $text" }

# Ask puts a yes/no question to the person at the console. With nobody there
# the answer is no, unless $env:PROWL_YES = 1.
function Ask($question) {
    if ($env:PROWL_YES -eq "1") { return $true }
    if (-not [Environment]::UserInteractive) { return $false }
    Write-Host ""
    try { $answer = Read-Host "$(Paint $eye "?") $question $(Paint $muted "[Y/n]")" } catch { return $false }
    return $answer -in @("", "y", "Y", "yes", "Yes")
}

Write-Host ""
Write-Host (Paint $muted "    ,_____,")
Write-Host ((Paint $muted '    (') + (Paint $eye ' O') + (Paint $muted ',') + (Paint $eye 'O ') + (Paint $muted ')') + '    ' + (Paint $accent 'prowl'))
Write-Host ((Paint $muted '    /)   (\') + '    ' + (Paint $muted 'the PR owl'))
Write-Host (Paint $muted '   ((  "  ))')
Write-Host ((Paint $muted '  ===') + (Paint $talon '^') + (Paint $muted '===') + (Paint $talon '^') + (Paint $muted '==='))

$winget = Get-Command winget -ErrorAction SilentlyContinue
$wingetArgs = @("-e", "--source", "winget", "--accept-package-agreements", "--accept-source-agreements")

Step "What prowl needs"
$need = @(); $lack = @()
if (Get-Command gh -ErrorAction SilentlyContinue) {
    Ok "gh $(((gh --version) -split "`n")[0] -replace '^gh version (\S+).*', '$1')"
} else {
    No "gh, the GitHub CLI"; $need += "GitHub.cli"; $lack += "gh"
}
if (Get-Command git -ErrorAction SilentlyContinue) {
    Ok "git $((git --version) -replace '^git version ', '')"
} else {
    No "git"; $need += "Git.Git"; $lack += "git"
}
if ($need) {
    $what = $lack -join " and "
    if (-not $winget) { Die "needs ${what}: install it (gh: https://cli.github.com), then run this again" }
    foreach ($id in $need) { Note "winget install $id" }
    if (-not (Ask "Install $what with this?")) { Die "needs ${what}: install it, then run this again" }
    foreach ($id in $need) {
        winget install $id @wingetArgs
        if ($LASTEXITCODE -ne 0) { Die "couldn't install ${what}: install it by hand (gh: https://cli.github.com), then run this again" }
    }
    Note "open a new terminal so it finds $what"
}

Step "Extras"
$fonts = @("$env:WINDIR\Fonts", "$env:LOCALAPPDATA\Microsoft\Windows\Fonts") |
    Where-Object { Test-Path $_ } | ForEach-Object { Get-ChildItem $_ -Filter "*Nerd*" -ErrorAction SilentlyContinue }
if ($fonts -or $env:TERM_PROGRAM -in @("WezTerm", "ghostty")) {
    Ok "a Nerd Font, for icons"
} else {
    No "a Nerd Font, for icons"
    if ($winget) {
        Note "winget install DEVCOM.JetBrainsMonoNerdFont"
        if (Ask "Install the Nerd Font?") {
            winget install DEVCOM.JetBrainsMonoNerdFont @wingetArgs
            if ($LASTEXITCODE -eq 0) {
                Note "choose JetBrainsMono Nerd Font in Windows Terminal: Settings, your profile, Appearance, Font face"
            } else {
                No "couldn't install the Nerd Font; prowl works without icons"
            }
        }
    }
}

Step "Installing prowl"
$archive = "prowl_windows_$arch.zip"
$base = "https://github.com/$repo/releases/latest/download"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null

$exe = Join-Path $dir "prowl.exe"
$service = Get-ScheduledTask -TaskName "prowl-watch" -ErrorAction SilentlyContinue

try {
    $zip = Join-Path $tmp $archive
    try {
        Invoke-WebRequest "$base/$archive" -OutFile $zip -UseBasicParsing
        Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt") -UseBasicParsing
    } catch {
        Die "couldn't download the latest release from https://github.com/$repo/releases"
    }
    $want = Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -like "*  $archive" } | ForEach-Object { ($_ -split "\s+")[0] }
    $got = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want -ne $got) { Die "the download doesn't match its checksum; not installing" }

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
        Die "$exe is in use; close prowl and run this again"
    }
} finally {
    Remove-Item $tmp -Recurse -Force
}

$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (($path -split ";") -notcontains $dir) {
    [Environment]::SetEnvironmentVariable("Path", "$path;$dir", "User")
    Note "added $dir to your PATH; open a new terminal to use it"
}
if ($service) {
    Start-ScheduledTask -TaskName "prowl-watch"
    Note "restarted background notifications"
}
Ok "$((& $exe version | Select-Object -First 1)) is in $dir"
if (Get-Command gh -ErrorAction SilentlyContinue) {
    cmd /c "gh auth status >nul 2>&1"
    if ($LASTEXITCODE -ne 0) { Note "gh isn't logged in; prowl offers to run gh auth login when it starts" }
}
Write-Host ""
Write-Host "  Run $(Paint $accent "prowl") to start."
Write-Host ""
