# gitea2forgejo installer (Windows)
#
# Usage:
#   iwr -useb https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/install.ps1 | iex
#
# Environment variable overrides:
#   $env:INSTALL_DIR  install directory (default: %LOCALAPPDATA%\Programs\gitea2forgejo)
#   $env:VERSION      version tag to install (default: latest release)
#   $env:SKIP_DEPS    set to 1 to skip the dependency install step
#
# The script:
#   1. Detects CPU (amd64/arm64)
#   2. Installs dependencies via winget (OpenSSH, Git, rsync,
#      PostgreSQL client, SQLite, zstd)
#   3. Downloads the matching .exe from the latest GitHub release
#   4. Stages it under LocalAppData and adds to the user PATH (no admin)

$ErrorActionPreference = 'Stop'
$Repo = 'pacnpal/gitea2forgejo'
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\gitea2forgejo' }
$Version = $env:VERSION

function Write-Info  ($m) { Write-Host "» $m" -ForegroundColor Blue }
function Write-Ok    ($m) { Write-Host "✓ $m" -ForegroundColor Green }
function Write-Warn2 ($m) { Write-Host "! $m" -ForegroundColor Yellow }
function Die         ($m) { Write-Host "✗ $m" -ForegroundColor Red; exit 1 }

function Install-Deps {
    if ($env:SKIP_DEPS -eq '1') {
        Write-Info "skipping dependency install (SKIP_DEPS=1)"
        return
    }
    # Native PowerShell on Windows can't run gitea2forgejo's full migration
    # (rsync/pg_dump/tar-with-zstd are POSIX tools). We still install the
    # preflight/manifest-only flow's dependencies here; for dump/restore
    # the operator will want WSL2 with a full Linux userland.
    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
        Write-Warn2 "winget not found — install App Installer from the Microsoft Store"
        Write-Warn2 "then re-run this installer, or install dependencies manually:"
        Write-Warn2 "  OpenSSH, Git, PostgreSQL client, SQLite, rsync (via WSL2 for full features)"
        return
    }
    Write-Info "installing dependencies via winget ..."
    $deps = @(
        'Microsoft.OpenSSH.Beta',         # ssh-keygen, ssh-keyscan, ssh-copy-id
        'Git.Git',                        # git + bundled utilities
        'PostgreSQL.PostgreSQL.16',       # pg_dump, pg_restore
        'SQLite.SQLite',                  # sqlite3
        'Facebook.Zstandard'              # zstd (for tar.zst extraction)
    )
    foreach ($pkg in $deps) {
        try {
            winget install --id $pkg --silent --accept-package-agreements --accept-source-agreements --disable-interactivity 2>&1 | Out-Null
            Write-Ok "  $pkg"
        } catch {
            Write-Warn2 "  $pkg (skipped / already present / failed)"
        }
    }
    Write-Warn2 "Full dump/restore also needs rsync. On Windows, the recommended path is"
    Write-Warn2 "WSL2 + a Debian/Ubuntu distro, then run the Linux installer inside that."
}

function Detect-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { Die "unsupported CPU architecture: $env:PROCESSOR_ARCHITECTURE" }
    }
}

function Get-LatestVersion {
    # Follow /releases/latest redirect — no GitHub token or jq equivalent needed.
    $url = "https://github.com/$Repo/releases/latest"
    $resp = Invoke-WebRequest -Uri $url -MaximumRedirection 0 -SkipHttpErrorCheck -UseBasicParsing 2>$null
    if (-not $resp) {
        # PowerShell 5.1 fallback: follow redirects and use the final URL.
        $resp = Invoke-WebRequest -Uri $url -UseBasicParsing
        return ($resp.BaseResponse.ResponseUri.AbsoluteUri -split '/')[-1]
    }
    $loc = $resp.Headers.Location
    if (-not $loc) { Die "couldn't resolve latest release; try setting `$env:VERSION" }
    return ($loc -split '/')[-1]
}

Write-Host "gitea2forgejo installer" -ForegroundColor Cyan
Write-Host ""

$arch = Detect-Arch
$platform = "windows-$arch"
Write-Info "platform:     $platform"

Install-Deps
Write-Host ""

if (-not $Version) { $Version = Get-LatestVersion }
Write-Info "version:      $Version"

$binary = "gitea2forgejo-$platform.exe"
$url = "https://github.com/$Repo/releases/download/$Version/$binary"
Write-Info "source:       $url"
Write-Info "install dir:  $InstallDir"
Write-Host ""

$tmp = [System.IO.Path]::GetTempFileName()
try { Remove-Item $tmp -Force } catch {}
$tmp = "$tmp.exe"

Write-Info "downloading ..."
try {
    Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
} catch {
    Die "download failed: $($_.Exception.Message)"
}

$size = (Get-Item $tmp).Length
if ($size -lt 1048576) {
    Die "downloaded file is only $size bytes — aborting"
}
Write-Ok "downloaded $([int]($size / 1048576)) MB"

# Install
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$dest = Join-Path $InstallDir 'gitea2forgejo.exe'
Move-Item -Force $tmp $dest
Write-Ok "installed to $dest"

# Unblock (removes Zone.Identifier alternate stream so SmartScreen doesn't warn)
try { Unblock-File -Path $dest -ErrorAction SilentlyContinue } catch {}

# Add to PATH (user scope)
$userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
if ($null -eq $userPath) { $userPath = '' }
$paths = $userPath -split ';' | Where-Object { $_ -ne '' }
if ($paths -notcontains $InstallDir) {
    $newPath = ($paths + $InstallDir) -join ';'
    [Environment]::SetEnvironmentVariable('PATH', $newPath, 'User')
    Write-Ok "added $InstallDir to user PATH"
    Write-Warn2 "open a new terminal (or 'refreshenv') to pick up the PATH change"
} else {
    Write-Ok "$InstallDir already in user PATH"
}

# Verify
Write-Host ""
try {
    $ver = & $dest --version
    Write-Ok $ver
    Write-Host ""
    Write-Host "Next: " -NoNewline; Write-Host "gitea2forgejo init" -ForegroundColor White
    Write-Host "Docs: https://github.com/$Repo" -ForegroundColor Blue
} catch {
    Write-Warn2 "installed, but couldn't execute — check SmartScreen/Defender settings"
}
