# Pearl CLI installer for Windows: downloads the latest release binary,
# verifies its SHA-256 checksum, and installs it into a user-owned directory.
#
# Usage:
#   irm https://raw.githubusercontent.com/August-H/pearl-cli/main/install.ps1 | iex
#
# Optional environment overrides (useful for mirrors and testing):
#   PEARL_INSTALL_DIR   target directory (default: %USERPROFILE%\bin)
#   PEARL_DOWNLOAD_BASE asset base URL

$ErrorActionPreference = "Stop"

$Repo = "August-H/pearl-cli"
$DownloadBase = if ($env:PEARL_DOWNLOAD_BASE) {
    $env:PEARL_DOWNLOAD_BASE
} else {
    "https://github.com/$Repo/releases/latest/download"
}
$InstallDir = if ($env:PEARL_INSTALL_DIR) {
    $env:PEARL_INSTALL_DIR
} else {
    Join-Path $HOME "bin"
}

if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
    Write-Warning "Pearl ships x64 Windows binaries; they run on Arm through emulation."
}
$Asset = "pearl-windows-amd64.exe"

$TempDir = Join-Path ([IO.Path]::GetTempPath()) ("pearl-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
try {
    $BinaryPath = Join-Path $TempDir $Asset
    $ChecksumPath = "$BinaryPath.sha256"

    Write-Host "Downloading Pearl ($Asset)..."
    try {
        Invoke-WebRequest -UseBasicParsing "$DownloadBase/$Asset" -OutFile $BinaryPath
        Invoke-WebRequest -UseBasicParsing "$DownloadBase/$Asset.sha256" -OutFile $ChecksumPath
    } catch {
        Write-Error @"
Could not download $DownloadBase/$Asset.
Check https://github.com/$Repo/releases for available releases,
or build from source: go build -o pearl.exe ./cmd/pearl
"@
    }

    $Expected = (Get-Content $ChecksumPath).Split(" ")[0].Trim().ToLower()
    $Actual = (Get-FileHash -Algorithm SHA256 $BinaryPath).Hash.ToLower()
    if ($Actual -ne $Expected) {
        Write-Error "Checksum mismatch; refusing to install.`n  expected $Expected`n  actual   $Actual"
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $Target = Join-Path $InstallDir "pearl.exe"
    Unblock-File $BinaryPath
    Move-Item -Force $BinaryPath $Target

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (($UserPath -split ";") -notcontains $InstallDir) {
        $NewPath = if ($UserPath) { "$UserPath;$InstallDir" } else { $InstallDir }
        [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
        Write-Host "Added $InstallDir to your user PATH; open a new terminal to pick it up."
    }
    $env:Path = "$env:Path;$InstallDir"

    $Version = ""
    try {
        $Version = (& $Target version | Select-Object -First 1)
    } catch {
        Write-Warning "Pearl was installed but could not be executed; it may be blocked by antivirus."
    }
    Write-Host "Installed $Target $Version"
    Write-Host 'Next step: run "pearl configure" to add your OpenRouter API key.'
} finally {
    Remove-Item -Recurse -Force $TempDir
}
