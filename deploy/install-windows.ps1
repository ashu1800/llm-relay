<#
.SYNOPSIS
    LLM Relay -- Windows one-click installer (bootstrap entry point).

.DESCRIPTION
    This script does exactly one thing: fetch the real installer
    (deploy/install.ps1) from GitHub and run it. All installation logic lives
    in install.ps1; nothing is reimplemented here.

    WHY THIS FILE EXISTS
    --------------------
    The Linux equivalent of this entry point is a single line:

        curl -sSL .../deploy/install.sh | sudo bash

    Copying that shape on Windows does not work, for three platform reasons:

      1. `curl ... | pwsh` is unreliable. When PowerShell reads a script from
         stdin, $PSScriptRoot is empty and relative paths resolve against the
         current working directory while the installer downloads archives,
         extracts them and writes config. Any relative path would land in
         whatever directory the user happened to be in (often System32).
      2. The default execution policy is RemoteSigned/Restricted. A .ps1
         downloaded from the internet carries the Zone.Identifier alternate
         data stream, so running it is refused with "not digitally signed" --
         a message that gives the user no useful next step.
      3. install.ps1 lives in the GitHub repo; the user has only a command,
         not a clone.

    So this entry point downloads to a temp directory (cleaned up afterwards),
    clears the zone marker, and executes via `powershell -File` rather than a
    pipe, which keeps $PSScriptRoot valid.

    ENCODING NOTE (important if you edit this file)
    -----------------------------------------------
    This script is deliberately pure ASCII. Windows PowerShell 5.1 decodes a
    BOM-less .ps1 as ANSI, and a BOM breaks `irm ... | iex` because the BOM
    character (U+FEFF) survives into the string and stops `<#` from opening
    the block comment. Since this file must work BOTH ways -- piped through
    `iex` and run with `-File` -- the only encoding that is safe in both is
    plain ASCII. Keep it that way. (install.ps1 can use Chinese because it is
    only ever run with -File and is stored with a BOM.)

    TRUST MODEL
    -----------
    Downloading and executing a script means the trust anchor is the GitHub
    repository itself, exactly like `curl | bash`. The installer still
    verifies the SHA-256 of the binary archive it downloads, but that protects
    against a corrupted or swapped archive in transit -- not against the
    repository being modified. If that is not acceptable, install offline:

        # download the release zip, extract it, then run the bundled installer
        pwsh -File .\deploy\install.ps1

.PARAMETER InstallerUrl
    Installer location. Defaults to deploy/install.ps1 on the main branch.

.PARAMETER GhProxy
    GitHub download accelerator prefix (e.g. https://gh-proxy.com).
    Use this when raw.githubusercontent.com is unreachable.

.PARAMETER InstallerArgs
    Arguments forwarded verbatim to install.ps1. Example:
        .\install-windows.ps1 -InstallerArgs '-Port','9000','-BindAddr','0.0.0.0'

.PARAMETER Ref
    Branch or tag to fetch the installer from. Defaults to main. Pin it with
    -Ref v0.1.29 to install using the installer as it was at that release;
    note that older installers may not understand newer parameters.

.EXAMPLE
    # Simplest form (in an elevated PowerShell window)
    irm https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install-windows.ps1 | iex

.EXAMPLE
    # Custom port and bind address (non-loopback also needs PublicOrigin)
    .\install-windows.ps1 -InstallerArgs '-Port','9000','-BindAddr','0.0.0.0','-PublicOrigin','https://relay.example.com'

.EXAMPLE
    # Behind a GitHub accelerator
    irm https://gh-proxy.com/https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install-windows.ps1 | iex
#>
[CmdletBinding()]
param(
    [string]$InstallerUrl = 'https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1',
    [string]$GhProxy = '',
    [string[]]$InstallerArgs = @(),
    [string]$Ref = 'main'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Write-Step { param([string]$m) Write-Host "==> $m" -ForegroundColor Cyan }
function Fail { param([string]$m) Write-Host "[ERROR] $m" -ForegroundColor Red; exit 1 }

# ---------------------------------------------------------------- admin check
# Checked here rather than leaving it to install.ps1: the message below can
# explain how to open an elevated shell, and the user has not yet seen any
# installer output that would otherwise be the only clue.
$principal = New-Object Security.Principal.WindowsPrincipal(
    [Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail @'
Administrator rights are required (writing to %ProgramData%, registering a
Windows service, installing PostgreSQL).

Reopen your terminal as administrator, then run the command again:
  * Start menu -> search "PowerShell" -> right-click -> "Run as administrator"
  * Win+X -> "Terminal (Admin)" or "Windows PowerShell (Admin)"
'@
}

# TLS 1.2: not enabled by default on Server 2016/2012 R2. Without it, fetching
# from GitHub fails immediately with "The underlying connection was closed".
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# ---------------------------------------------------------------- build URL
if ($Ref -and $Ref -ne 'main') {
    # Only rewrite the default main-branch URL; never touch an explicit -InstallerUrl
    if ($InstallerUrl -like '*/main/*') {
        $InstallerUrl = $InstallerUrl -replace '/main/', "/$Ref/"
    }
}
if ($GhProxy) {
    # gh-proxy takes "prefix + full original URL", same as install.sh
    $InstallerUrl = ($GhProxy.TrimEnd('/')) + '/' + $InstallerUrl
}

# ---------------------------------------------------------------- download
# Temp dir with guaranteed cleanup. A unique name (not a fixed filename under
# %TEMP%) so two concurrent runs cannot overwrite each other's download.
$tmp = Join-Path $env:TEMP ('llm-relay-bootstrap-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
    $installer = Join-Path $tmp 'install.ps1'

    Write-Step "Fetching installer: $InstallerUrl"
    try {
        # -UseBasicParsing keeps Windows PowerShell 5.1 off the IE engine
        # (unavailable on Server Core).
        Invoke-WebRequest -Uri $InstallerUrl -OutFile $installer -TimeoutSec 60 -UseBasicParsing
    } catch {
        Fail @"
Failed to download the installer: $InstallerUrl
$($_.Exception.Message)

If GitHub is unreachable, retry with an accelerator prefix:
  -GhProxy https://gh-proxy.com

Or extract deploy/install.ps1 from the release zip and run it directly:
  pwsh -File .\deploy\install.ps1
"@
    }

    if (-not (Test-Path -LiteralPath $installer) -or (Get-Item -LiteralPath $installer).Length -lt 1024) {
        Fail 'The downloaded installer does not look valid (empty or too small). Check your network/proxy and retry.'
    }

    # Clear the "downloaded from the internet" marker. Under RemoteSigned this
    # would otherwise abort with "not digitally signed" -- an error that tells
    # the user nothing, since downloading it was the whole point.
    try { Unblock-File -LiteralPath $installer -ErrorAction Stop } catch { }

    # ------------------------------------------------------------ execute
    #
    # -File rather than a pipe: $PSScriptRoot is therefore valid and any
    # relative path inside the installer resolves against its own directory.
    # There is no reliable runtime way to detect the piped case, so the entry
    # point avoids it entirely.
    #
    # pwsh (PowerShell 7) is preferred for its better UTF-8 and CJK handling;
    # powershell.exe (always present on Windows) is the fallback.
    $exe = (Get-Command pwsh.exe -ErrorAction SilentlyContinue).Source
    if (-not $exe) { $exe = (Get-Command powershell.exe -ErrorAction Stop).Source }

    Write-Step "Running installer ($([IO.Path]::GetFileName($exe)))"
    Write-Host ''

    # Forward as a string array, not a concatenated string: joining would
    # silently misplace arguments containing spaces.
    $argv = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $installer) + $InstallerArgs

    & $exe @argv
    $code = $LASTEXITCODE

    if ($code -ne 0) {
        # The installer already printed its reason; add only what comes next.
        Write-Host ''
        Fail "Installer exited with code $code. See the output above; logs and config are under %ProgramData%\llm-relay."
    }
    exit 0
}
finally {
    # Always remove the temp dir: it only holds a script that has already run.
    # The installer manages its own temp directory separately.
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
