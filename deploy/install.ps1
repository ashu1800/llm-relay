<#
.SYNOPSIS
    LLM Relay -- Windows installer (bare binary + Windows service).

.DESCRIPTION
    The Windows counterpart of deploy/install.sh. Downloads the prebuilt
    binary from GitHub Releases (the frontend is embedded in it), installs
    PostgreSQL if needed, creates the database and role, generates a config
    with random secrets, registers a Windows service, and health-checks it.

    Reinstall safety: running this again is an upgrade. The DB password,
    encryption master key and console login key are carried over from the
    existing config.yaml (regenerated and printed if they cannot be read),
    and the database is dumped to <InstallDir>\backups\ before upgrading.

    Differences from install.sh (all forced by the platform, not choices):
      * No systemd. The service is hosted by SCM and recovery is configured
        with `sc failure` (equivalent to Restart=always).
      * SCM does not capture stdout, so logs go to a file (LOG_FILE) rather
        than journald.
      * There is no EnvironmentFile equivalent, so config is written as
        config.yaml (secrets can live there; ACLs are tightened).
      * The install directory defaults to %ProgramData%\llm-relay rather than
        Program Files: self-update renames and writes temp files next to the
        executable, and Program Files is not writable by the service account.

    ONE FILE, TWO ENTRY POINTS
    --------------------------
    This same file is both the one-click command and the local installer:

        irm https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1 | iex
        pwsh -File .\deploy\install.ps1

    There used to be a second file (install-windows.ps1) that only downloaded
    this one, which forced users to work out which to run. That is an
    implementation detail leaking into the UI. Now there is one file, and the
    self-bootstrap block below makes the piped form behave exactly like the
    file form.

    ENCODING: this file is plain ASCII on purpose. Windows PowerShell 5.1
    decodes a BOM-less .ps1 as ANSI (so non-ASCII comments would be corrupted
    under -File), and a BOM makes `irm | iex` fail outright (the BOM character
    survives into the string and stops `<#` from opening the block comment;
    measured: 13 parse errors). Only ASCII works under both entry points.
    Keep it that way.

.PARAMETER InstallDir
    Install directory. Defaults to $env:ProgramData\llm-relay.

.PARAMETER Port
    Listen port. Default 8888.

.PARAMETER BindAddr
    Listen address. Default 127.0.0.1; changing it to 0.0.0.0 (public
    deployment) requires -PublicOrigin.

.PARAMETER PublicOrigin
    Fixed origin allowed to reach the console, e.g. https://relay.example.com.

.PARAMETER DbName / DbUser
    Database name / user. Defaults llm_relay / llmrelay.

.PARAMETER Version
    Version to install (no v prefix). Defaults to the latest Release.

.PARAMETER GhProxy
    GitHub download accelerator prefix (e.g. https://gh-proxy.com).

.PARAMETER PgMode
    auto (default): install a dedicated PostgreSQL instance when none works.
    skip: never install automatically; only use an existing instance.

.PARAMETER PgDecision
    What to do when an existing PostgreSQL is found:
      existing -- reuse it (requires -PgSuperPassword, or you will be asked)
      new      -- install a separate instance, leaving the existing one alone
    Empty (default) means: ask on an interactive terminal, default to new.

.PARAMETER PgSuperPassword
    Password of the existing instance's postgres superuser. Generated
    automatically for instances this installer creates.

.PARAMETER Force
    Re-download and overwrite even when the same version is already installed.

.EXAMPLE
    # Simplest: everything has a sensible default
    irm https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1 | iex

.EXAMPLE
    # Local copy, custom port
    pwsh -File .\deploy\install.ps1 -Port 9000
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramData 'llm-relay'),
    [int]$Port = 8888,
    [string]$BindAddr = '127.0.0.1',
    [string]$PublicOrigin = '',
    [string]$DbName = 'llm_relay',
    [string]$DbUser = 'llmrelay',
    [string]$Version = '',
    [string]$GhProxy = '',
    [ValidateSet('auto', 'skip')]
    [string]$PgMode = 'auto',
    # existing = reuse the instance already on this machine
    # new      = install a separate instance next to it
    # empty    = ask (interactive) / default to new (non-interactive)
    [ValidateSet('', 'existing', 'new')]
    [string]$PgDecision = '',
    [string]$PgSuperPassword = '',
    # Re-download even when this version is already installed
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo = 'ashu1800/llm-relay'

# ---------------------------------------------------------------- self-bootstrap
#
# Under `irm ... | iex` this script runs inside a scriptblock of the caller's
# session: $PSCommandPath and $PSScriptRoot are both empty. The install needs
# a temp directory, needs Unblock-File, and must run as a file (so that a
# terminating error does not close the user's terminal -- measured).
#
# So: when not running from a file, download ourselves to a temp file, re-run
# that copy with -File, and return. Everything below therefore only ever runs
# in file mode, and both entry points share one implementation.
if ([string]::IsNullOrEmpty($PSCommandPath) -or [string]::IsNullOrEmpty($PSScriptRoot)) {
    $selfUrl = 'https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1'
    if ($GhProxy) { $selfUrl = ($GhProxy.TrimEnd('/')) + '/' + $selfUrl }

    $principal = New-Object Security.Principal.WindowsPrincipal(
        [Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw @'
Administrator rights are required (writing to %ProgramData%, registering a
Windows service, installing PostgreSQL).

Reopen your terminal as administrator, then run the command again:
  * Start menu -> search "PowerShell" -> right-click -> "Run as administrator"
  * Win+X -> "Terminal (Admin)" or "Windows PowerShell (Admin)"
'@
    }

    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $boot = Join-Path $env:TEMP ('llm-relay-boot-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    New-Item -ItemType Directory -Path $boot -Force | Out-Null
    try {
        $selfFile = Join-Path $boot 'install.ps1'
        Write-Host "==> Fetching installer: $selfUrl" -ForegroundColor Cyan
        Invoke-WebRequest -Uri $selfUrl -OutFile $selfFile -TimeoutSec 60 -UseBasicParsing
        try { Unblock-File -LiteralPath $selfFile -ErrorAction Stop } catch { }

        $exe = (Get-Command pwsh.exe -ErrorAction SilentlyContinue).Source
        if (-not $exe) { $exe = (Get-Command powershell.exe -ErrorAction Stop).Source }

        # Forward every bound parameter unchanged so user options are not lost.
        $fwd = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $selfFile)
        foreach ($k in $PSBoundParameters.Keys) {
            $v = $PSBoundParameters[$k]
            if ($v -is [System.Management.Automation.SwitchParameter]) {
                if ($v.IsPresent) { $fwd += "-$k" }
            } elseif ($null -ne $v -and "$v" -ne '') {
                $fwd += @("-$k", "$v")
            }
        }
        & $exe @fwd
    } finally {
        Remove-Item -LiteralPath $boot -Recurse -Force -ErrorAction SilentlyContinue
    }
    return
}

$ServiceName = 'llm-relay'
$PgServiceName = 'llm-relay-pg'
$PgMajor = '17'
$PgInstallerVersion = '17.6-1'

function Write-Log  { param([string]$m) Write-Host "==> $m" -ForegroundColor Cyan }
function Write-Ok   { param([string]$m) Write-Host "    $m" -ForegroundColor Green }
function Write-Warn { param([string]$m) Write-Host "[WARN] $m" -ForegroundColor Yellow }
# Writes the reason to stderr as well as the console, so it survives when
# stdout is redirected into a file or captured by a parent process: Write-Host
# output can be lost on `exit`, which is how an earlier failure produced
# "installer exited with code 1" and no explanation at all (2026-10-23).
# Deliberately not Write-Error: that would print a PowerShell stack trace
# around an already human-readable message.
function Fail {
    param([string]$m)
    Write-Host "[FAIL] $m" -ForegroundColor Red
    [Console]::Error.WriteLine("[FAIL] $m")
    [Console]::Out.Flush()
    exit 1
}

# ---------------------------------------------------------------- preflight
# Administrator is mandatory: we write to ProgramData, register a service, and
# may install PostgreSQL.
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail 'Administrator rights are required. Reopen PowerShell as administrator and retry.'
}

# TLS 1.2 is not enabled by default on Server 2016/2012 R2; without it,
# Invoke-WebRequest to GitHub fails with "underlying connection was closed".
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# The release matrix only ships windows/amd64 (.goreleaser.yaml ignores arm64).
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
    Fail "Unsupported architecture: $arch (releases are windows/amd64 only)"
}

if ($Port -lt 1 -or $Port -gt 65535) { Fail 'PORT must be between 1 and 65535' }

# Same rule as install.sh: a non-loopback listen address requires a fixed
# origin, otherwise the console's same-origin check rejects every admin
# request (symptom: blank page after login).
$loopback = @('127.0.0.1', 'localhost', '::1')
if ($loopback -notcontains $BindAddr -and [string]::IsNullOrWhiteSpace($PublicOrigin)) {
    Fail "BindAddr=$BindAddr requires -PublicOrigin (e.g. https://relay.example.com)"
}
if ($PublicOrigin -and $PublicOrigin -notmatch '^https?://[^\s/]+$') {
    Fail 'PublicOrigin must be a full scheme://host (no path, query or credentials)'
}
# Database identifiers end up in SQL: allow only safe characters.
foreach ($pair in @(@('DbUser', $DbUser), @('DbName', $DbName))) {
    if ($pair[1] -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
        Fail "$($pair[0]) may contain only letters, digits and underscores, and must start with a letter or underscore"
    }
}

# ---------------------------------------------------------------- version
function Resolve-LatestVersion {
    $url = "https://api.github.com/repos/$Repo/releases/latest"
    try {
        $resp = Invoke-RestMethod -Uri $url -TimeoutSec 30 -Headers @{ 'User-Agent' = 'llm-relay-installer' }
        return ($resp.tag_name -replace '^v', '')
    } catch {
        return ''
    }
}

if ([string]::IsNullOrWhiteSpace($Version)) {
    Write-Log 'Resolving latest version'
    $Version = Resolve-LatestVersion
    if ([string]::IsNullOrWhiteSpace($Version)) {
        Fail 'GitHub API query failed. Retry with -GhProxy <prefix>, or pass -Version <ver> explicitly.'
    }
}
$Version = $Version -replace '^v', ''

if ([string]::IsNullOrWhiteSpace($GhProxy)) {
    $dlBase = 'https://github.com'
} else {
    $dlBase = ($GhProxy.TrimEnd('/')) + '/https://github.com'
}
$relBase = "$dlBase/$Repo/releases/download/v$Version"
$archiveName = "llm-relay_${Version}_windows_amd64.zip"

$tmp = Join-Path $env:TEMP ("llm-relay-install-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
    # Paths used throughout this script. Defined here, before any branch, so
    # that every code path -- including the "same version already installed"
    # one -- can reference them. Under Set-StrictMode an unassigned variable
    # is a terminating error, and one previous version of this file assigned
    # $exePath only inside the extract branch, which made every reinstall of
    # the same version die with "variable not set" before writing config.
    $exePath = Join-Path $InstallDir 'llm-relay.exe'
    $metaPath = Join-Path $InstallDir 'install.json'

    # ------------------------------------------------------------ download
    #
    # Skip the download when the same version is already installed, and keep a
    # copy in %LOCALAPPDATA%\llm-relay\cache so retries do not restart from
    # zero (important when GitHub is reachable but flaky). -Force overrides.
    #
    # The version comes from install.json, NOT from VersionInfo.ProductVersion:
    # the Go linker only writes the version into a string constant (-ldflags
    # -X), no PE version resource is generated, so ProductVersion is always
    # empty on a real release binary (measured 2026-10-23: an installed
    # llm-relay.exe reported empty ProductVersion/FileVersion/ProductName).
    # install.json is written by the last install that reached the health
    # check, so it is the only trustworthy record of what is on disk.
    $alreadyInstalled = $false
    $installedMeta = $null
    # Recorded before extraction overwrites the binary, so the pre-upgrade
    # backup below only runs when there really was a previous install.
    $hadPreviousInstall = Test-Path -LiteralPath $exePath
    if ((-not $Force) -and $hadPreviousInstall -and (Test-Path -LiteralPath $metaPath)) {
        try {
            $installedMeta = Get-Content -LiteralPath $metaPath -Raw | ConvertFrom-Json
            # Older installs predate llm_relay_ver; they fall through to the
            # normal download path, which is correct (we cannot prove a match).
            # Property existence is checked explicitly: under Set-StrictMode a
            # missing property is a terminating error, not $null.
            if ($installedMeta.PSObject.Properties['llm_relay_ver'] -and $installedMeta.llm_relay_ver) {
                if ("$($installedMeta.llm_relay_ver)" -eq $Version) { $alreadyInstalled = $true }
            }
        } catch {
            # Unreadable marker: treat as not installed rather than guessing.
        }
    }

    $cacheDir = Join-Path $env:LOCALAPPDATA 'llm-relay\cache'
    New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null
    $archive = Join-Path $cacheDir $archiveName
    $sums = Join-Path $cacheDir 'checksums.txt'

    if ($alreadyInstalled) {
        Write-Ok "llm-relay $Version is already installed; skipping download (use -Force to reinstall)"
    } else {
        if (Test-Path -LiteralPath $archive) {
            Write-Log "Using cached archive $archive"
        } else {
            Write-Log "Downloading $archiveName"
            try {
                Invoke-WebRequest -Uri "$relBase/$archiveName" -OutFile $archive -TimeoutSec 300 -UseBasicParsing
                Invoke-WebRequest -Uri "$relBase/checksums.txt" -OutFile $sums -TimeoutSec 60 -UseBasicParsing
            } catch {
                Fail "Download failed: $relBase/$archiveName (retry with -GhProxy <prefix> if GitHub is unstable). $($_.Exception.Message)"
            }
        }

        # A missing checksum file is a hard failure: this is the last integrity
        # check between "file from the network" and "runs as a service".
        if (-not (Test-Path -LiteralPath $sums)) {
            try {
                Invoke-WebRequest -Uri "$relBase/checksums.txt" -OutFile $sums -TimeoutSec 60 -UseBasicParsing
            } catch {
                Fail "Failed to download checksums.txt: $($_.Exception.Message)"
            }
        }
        $expected = $null
        foreach ($line in Get-Content -LiteralPath $sums) {
            $parts = $line -split '\s+'
            if ($parts.Count -ge 2 -and $parts[1] -eq $archiveName) { $expected = $parts[0]; break }
        }
        if (-not $expected) { Fail "checksums.txt has no entry for $archiveName (version/arch mismatch?)" }
        $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLower()
        if ($actual -ne $expected.ToLower()) {
            # Drop the bad cache entry: a corrupted cache must not wedge retries.
            Remove-Item -LiteralPath $archive -Force -ErrorAction SilentlyContinue
            Fail "sha256 mismatch: expected $expected, got $actual (corrupt cache removed; retry)"
        }
        Write-Ok 'sha256 verified'
    }

    # ------------------------------------------------------------ extract
    Write-Log "Installing to $InstallDir"
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $InstallDir 'backups') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $InstallDir 'logs') -Force | Out-Null

    if (-not $alreadyInstalled) {
        Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force
        $exeSrc = Join-Path $tmp 'llm-relay.exe'
        if (-not (Test-Path -LiteralPath $exeSrc)) { Fail 'Archive has no llm-relay.exe; it may be corrupt' }

        # Stop the service before replacing files: Windows locks a running exe.
        $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($existing -and $existing.Status -ne 'Stopped') {
            Write-Log 'Stopping the running service to replace program files'
            Stop-Service -Name $ServiceName -Force
            (Get-Service -Name $ServiceName).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
        }

        Copy-Item -LiteralPath $exeSrc -Destination $exePath -Force
        if (Test-Path (Join-Path $tmp 'rotate-secret.exe')) {
            Copy-Item -LiteralPath (Join-Path $tmp 'rotate-secret.exe') -Destination (Join-Path $InstallDir 'rotate-secret.exe') -Force
        }
    } else {
        # Service registration and health check still run below: config may
        # have changed even though the binary did not.
        $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    }

    # ------------------------------------------------------------ PostgreSQL
    $pgsqlRoot = Join-Path $InstallDir 'pgsql'
    $pgBin = Join-Path $pgsqlRoot 'bin'
    $pgData = Join-Path $InstallDir 'pgdata'
    $psql = Join-Path $pgBin 'psql.exe'
    $envFile = Join-Path $InstallDir 'install.env'

    function New-PgSuperPassword {
        # Windows password policy: upper + lower + digit + symbol.
        $alphabet = [char[]]((48..57) + (65..90) + (97..122))
        $rand = -join (1..18 | ForEach-Object { $alphabet | Get-Random })
        return "$rand#A1"
    }
    function Read-SavedSuperPassword {
        if (-not (Test-Path -LiteralPath $envFile)) { return '' }
        foreach ($line in Get-Content -LiteralPath $envFile) {
            if ($line -match '^PG_SUPER_PASSWORD=(.+)$') { return $Matches[1].Trim('"') }
        }
        return ''
    }

    # The port is not a constant: a dedicated instance lands on 5433 when 5432
    # is taken, and the startup loop below can walk further (5434/5435/15432).
    # install.json (written by the last successful install) is the record of
    # what was actually used, so a reinstall reconnects to the right instance
    # instead of assuming 5432 and failing with a misleading "wrong password".
    #
    # Read via the PSObject property list, not $meta.pg_port: under
    # Set-StrictMode, accessing a property that a JSON object does not have is
    # a terminating error (PropertyNotFoundStrict), and every install.json
    # written before this field existed would trigger it.
    $pgPort = 5432
    if ($installedMeta -and $installedMeta.PSObject.Properties['pg_port'] -and $installedMeta.pg_port) {
        $pgPort = [int]$installedMeta.pg_port
    }
    # Must be initialised before use: under Set-StrictMode referencing an
    # undefined variable throws.
    $superPassword = $PgSuperPassword
    if ([string]::IsNullOrWhiteSpace($superPassword)) { $superPassword = Read-SavedSuperPassword }
    if ([string]::IsNullOrWhiteSpace($superPassword)) { $superPassword = New-PgSuperPassword }

    # Candidate psql binaries: the instance this installer created wins, then
    # the newest EnterpriseDB installation on the machine.
    $systemPsql = $null
    foreach ($candidate in @(
            (Join-Path $pgsqlRoot 'bin\psql.exe'),
            (Get-ChildItem 'C:\Program Files\PostgreSQL\*\bin\psql.exe' -ErrorAction SilentlyContinue |
                Sort-Object FullName -Descending | Select-Object -First 1 -ExpandProperty FullName)
        )) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { $systemPsql = $candidate; break }
    }

    # ----------------------------------------------------------------
    # Which PostgreSQL instance to use. This judgement has changed twice
    # after reported problems (2026-10-23):
    #
    #   Round 1 (wrong): reuse whenever psql.exe exists. On a machine with the
    #   EnterpriseDB PostgreSQL 17 installed, that targeted the *user's own*
    #   instance and connected with an empty password; worse, every psql call
    #   lacked -w, so psql stopped and prompted for the superuser password in
    #   a loop that could never succeed.
    #
    #   Round 2 (current): ASK. When an existing instance is found, offer a
    #   clear choice -- reuse it (then request that instance's postgres
    #   password) or install a separate instance that touches nothing else.
    #   The two outcomes are very different, so the user decides.
    #
    # Non-interactive runs use -PgDecision instead of prompting.
    # ----------------------------------------------------------------
    $useExisting = $false
    if ($systemPsql) {
        $ownInstance = Test-Path -LiteralPath (Join-Path $pgsqlRoot 'bin\psql.exe')
        $pgLabel = 'PostgreSQL'
        if ($systemPsql -match 'PostgreSQL\\(\d+)\\') { $pgLabel = "PostgreSQL $($Matches[1])" }

        if ($ownInstance) {
            # Created by this installer previously: reuse without asking, the
            # password is in install.env.
            #
            # The service is checked, not assumed. Reasons it can be missing or
            # stopped on a machine whose pgsql\ directory still exists:
            #   * `uninstall-windows.ps1 -KeepData` used to delete the service
            #     while keeping the data directory;
            #   * a manual `sc delete`, a failed upgrade, or a reboot with the
            #     service set to manual.
            # Without this check every later psql call fails to connect and the
            # script reports "is the superuser password correct?", which sends
            # the user looking in the wrong place (measured 2026-10-23).
            $useExisting = $true
            $psql = $systemPsql
            $pgSvc = Get-Service -Name $PgServiceName -ErrorAction SilentlyContinue
            if (-not $pgSvc) {
                Fail @"
The PostgreSQL instance at $pgsqlRoot has no service '$PgServiceName'.
Its data is still in $pgData, but nothing can start it.

Either restore the service (created by the EnterpriseDB installer, so the
cleanest fix is to uninstall and reinstall this product, choosing the
existing instance), or move the data directory away and let the installer
create a fresh instance.
"@
            }
            # Service present but stopped is normal after a reboot; start it.
            if ($pgSvc.Status -ne 'Running') {
                Write-Log "Starting the PostgreSQL service ($PgServiceName)"
                Start-Service -Name $PgServiceName
                (Get-Service -Name $PgServiceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
            }
            # And confirm the port from install.json is really the one it bound:
            # if nothing listens there, the connection errors below are not
            # about the password.
            $listening = $false
            for ($i = 0; $i -lt 20; $i++) {
                if (Get-NetTCPConnection -LocalPort $pgPort -State Listen -ErrorAction SilentlyContinue) {
                    $listening = $true
                    break
                }
                Start-Sleep -Milliseconds 500
            }
            if (-not $listening) {
                Fail "PostgreSQL service $PgServiceName is running but nothing listens on port $pgPort (expected from install.json). Check its postgresql.conf, or pass -Force with a fresh instance."
            }
            Write-Ok "Reusing the instance created by this installer: $systemPsql (port $pgPort)"
        } else {
            Write-Host ''
            Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray
            Write-Host "  Found an existing $pgLabel" -ForegroundColor Yellow
            Write-Host "    $systemPsql" -ForegroundColor DarkGray
            Write-Host ''
            Write-Host '  Reusing it needs that instance''s postgres superuser password'
            Write-Host '  (set when PostgreSQL itself was installed).'
            Write-Host '  Installing a new instance creates a separate one here and'
            Write-Host '  leaves the existing database completely untouched:'
            Write-Host "    $InstallDir" -ForegroundColor DarkGray
            Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray

            $decision = $PgDecision
            if ([string]::IsNullOrWhiteSpace($decision)) {
                if ($PgMode -eq 'skip') {
                    # -PgMode skip means "reuse only, never install"
                    $decision = 'existing'
                } elseif (-not [Environment]::UserInteractive) {
                    $decision = 'new'
                    Write-Warn 'Non-interactive session: defaulting to a new instance (use -PgDecision existing|new)'
                } else {
                    while (-not $decision) {
                        Write-Host ''
                        Write-Host '  Choose: ' -NoNewline
                        Write-Host '[1] Use the existing instance    ' -NoNewline -ForegroundColor Cyan
                        Write-Host '[2] Install a new instance'
                        Write-Host '  (default 2)'
                        $answer = Read-Host '  Enter 1 or 2'
                        switch ($answer.Trim()) {
                            '1' { $decision = 'existing'; break }
                            '2' { $decision = 'new'; break }
                            ''  { $decision = 'new'; break }
                            default { Write-Warn 'Please enter 1 or 2' }
                        }
                    }
                }
            }

            if ($decision -eq 'existing') {
                $entered = $PgSuperPassword
                if ([string]::IsNullOrWhiteSpace($entered) -and [Environment]::UserInteractive) {
                    $secure = Read-Host "  Enter the postgres password for $pgLabel" -AsSecureString
                    $entered = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
                        [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
                }
                if ([string]::IsNullOrWhiteSpace($entered)) {
                    Fail 'Chose to reuse the existing instance but no postgres password was given. Pass -PgSuperPassword.'
                }
                # -w: never prompt. One wrong password fails immediately.
                $env:PGPASSWORD = $entered
                $probe = & $systemPsql -w -U postgres -h 127.0.0.1 -p $pgPort -tAc "SELECT 1" 2>&1
                $probeOk = ($LASTEXITCODE -eq 0 -and "$probe".Trim() -eq '1')
                Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
                if (-not $probeOk) {
                    Fail @"
Cannot connect to the postgres superuser of $pgLabel at 127.0.0.1:$pgPort with that password.

Possible causes: wrong password, the instance is not listening on 5432, or
pg_hba.conf does not allow password auth from localhost.

Either run again and choose [2] (a separate instance, existing data untouched),
or retry with -PgDecision existing -PgSuperPassword <password>.
"@
                }
                $superPassword = $entered
                $useExisting = $true
                $psql = $systemPsql
                Write-Ok "Reusing the existing instance: $systemPsql (port $pgPort)"
            } else {
                Write-Ok 'Installing a separate instance; the existing database is untouched'
            }
        }
    } elseif ($PgMode -eq 'skip') {
        Fail 'No psql.exe found and -PgMode skip was given. Install PostgreSQL yourself and add its bin directory to PATH, or drop -PgMode skip.'
    }

    if (-not $useExisting) {
        # Separate instance: move off 5432 when something else is listening.
        if (Get-NetTCPConnection -LocalPort $pgPort -State Listen -ErrorAction SilentlyContinue) {
            Write-Warn "Port $pgPort is in use; the separate instance will listen on 5433"
            $pgPort = 5433
        }

        Write-Log "Downloading the PostgreSQL $PgMajor installer (~348 MB, takes a few minutes)"
        $pgInstallerName = "postgresql-$PgInstallerVersion-windows-x64.exe"
        $pgUrl = "https://get.enterprisedb.com/postgresql/$pgInstallerName"
        # Cache the 348 MB installer next to the relay archive: a retry after a
        # failure (or a reinstall on the same machine) should not re-download it.
        $pgCached = Join-Path $cacheDir $pgInstallerName
        $pgInstaller = $pgCached
        if (Test-Path -LiteralPath $pgCached) {
            Write-Ok "Using cached PostgreSQL installer ($pgCached)"
        } else {
            try {
                Invoke-WebRequest -Uri $pgUrl -OutFile $pgCached -TimeoutSec 900 -UseBasicParsing
            } catch {
                Fail "Failed to download the PostgreSQL installer: $($_.Exception.Message). Alternatively install PostgreSQL yourself and rerun with -PgMode skip -PgDecision existing -PgSuperPassword <password>."
            }
        }

        # Use an option file rather than command-line args: paths contain
        # spaces (ProgramData) and the locale contains commas, either of which
        # silently shifts Start-Process arguments.
        $optFile = Join-Path $tmp 'pg-options.txt'
        @(
            'mode=unattended'
            'unattendedmodeui=none'
            "prefix=$pgsqlRoot"
            "datadir=$pgData"
            "superpassword=$superPassword"
            "servicepassword=$superPassword"
            'superaccount=postgres'
            "servicename=$PgServiceName"
            "serverport=$pgPort"
            'install_runtimes=1'
            'create_shortcuts=0'
            'enable_acledit=1'
            'enable-components=server,commandlinetools'
            'disable-components=pgAdmin,stackbuilder'
        ) | Set-Content -LiteralPath $optFile -Encoding ASCII

        $debugLog = Join-Path $tmp 'pg-install-debug.log'
        Write-Log 'Installing PostgreSQL silently'
        $proc = Start-Process -FilePath $pgInstaller `
            -ArgumentList "--optionfile `"$optFile`" --debuglevel 4 --debugtrace `"$debugLog`"" `
            -Wait -PassThru -WindowStyle Hidden
        if ($proc.ExitCode -ne 0) {
            if (Test-Path -LiteralPath $debugLog) {
                Write-Host '----- PostgreSQL installer log (last 60 lines) -----' -ForegroundColor DarkYellow
                Get-Content -LiteralPath $debugLog -Tail 60 | ForEach-Object { Write-Host $_ }
            }
            Fail "PostgreSQL installer exited with code $($proc.ExitCode)"
        }
        if (-not (Test-Path -LiteralPath $psql)) {
            Fail "Installer reported success but $psql is missing"
        }
        Write-Ok "PostgreSQL $PgMajor installed (service $PgServiceName, port $pgPort)"

        # ------------------------------------------------------------
        # Verify the service actually RUNS, and move it if it cannot bind.
        #
        # The port check above only proves nothing was listening *before* the
        # install. That is not enough: on Windows, Hyper-V / WSL reserve whole
        # dynamic port ranges, and a port inside such a range is not reported
        # by Get-NetTCPConnection (nothing owns it) yet binds fail with
        # "Only one usage of each socket address ... is normally permitted".
        # Measured 2026-10-23: PostgreSQL installed fine, the service stayed
        # Stopped, port 5432 had no listener, and every startup attempt died on
        # socket creation -- the install then failed with no message at all
        # because the follow-up psql call could not connect.
        #
        # So: start the service and confirm it is listening. If not, walk a
        # few alternative ports and rewrite postgresql.conf.
        # ------------------------------------------------------------
        $pgConf = Join-Path $pgData 'postgresql.conf'
        # $pgPort is already 5433 when 5432 was taken, so de-duplicate rather
        # than listing it twice (a repeat costs a full start/stop cycle).
        $candidatePorts = @($pgPort, 5433, 5434, 5435, 15432 | Select-Object -Unique)
        $started = $false
        foreach ($tryPort in $candidatePorts) {
            if ($tryPort -ne $pgPort) {
                Write-Warn "Port $pgPort could not be bound; retrying on $tryPort"
                if (Test-Path -LiteralPath $pgConf) {
                    # Write WITHOUT a BOM, in ASCII: postgresql.conf is parsed by
                    # PostgreSQL's own tokenizer, and a UTF-8 BOM at offset 0 is
                    # a syntax error ("syntax error at or near ..."). PowerShell
                    # 5.1's -Encoding UTF8 prepends one, which is exactly how a
                    # first version of this fix broke the config (2026-10-23).
                    # The config is ASCII anyway, so ASCII is lossless here.
                    $conf = [IO.File]::ReadAllText($pgConf)
                    if ($conf -match '(?m)^\s*port\s*=') {
                        $conf = $conf -replace '(?m)^\s*port\s*=.*$', "port = $tryPort"
                    } else {
                        $conf += "`r`nport = $tryPort`r`n"
                    }
                    [IO.File]::WriteAllText($pgConf, $conf, (New-Object Text.ASCIIEncoding))
                }
                $pgPort = $tryPort
            }

            try {
                Start-Service -Name $PgServiceName -ErrorAction Stop
                # Wait for the listener rather than trusting "Running": pg_ctl
                # reports Running while postmaster is still initialising.
                for ($i = 0; $i -lt 20; $i++) {
                    if (Get-NetTCPConnection -LocalPort $pgPort -State Listen -ErrorAction SilentlyContinue) {
                        $started = $true
                        break
                    }
                    Start-Sleep -Milliseconds 500
                }
            } catch {
                $started = $false
            }

            if ($started) {
                Write-Ok "PostgreSQL listening on port $pgPort"
                break
            }
            Stop-Service -Name $PgServiceName -Force -ErrorAction SilentlyContinue
        }
        if (-not $started) {
            $logDir = Join-Path $pgData 'log'
            if (Test-Path -LiteralPath $logDir) {
                Write-Host '----- PostgreSQL startup log (last 40 lines) -----' -ForegroundColor DarkYellow
                Get-ChildItem -LiteralPath $logDir -Filter '*.log' -ErrorAction SilentlyContinue |
                    Sort-Object LastWriteTime -Descending | Select-Object -First 1 |
                    ForEach-Object { Get-Content -LiteralPath $_.FullName -Tail 40 | ForEach-Object { Write-Host $_ } }
            }
            Fail "PostgreSQL was installed but the service will not start on any of: $($candidatePorts -join ', '). See the log above."
        }

        # Remember the superuser password so a reinstall can connect again.
        @(
            '# Generated by install.ps1. Contains the PostgreSQL superuser password; do not share.'
            "PG_SUPER_PASSWORD=`"$superPassword`""
        ) | Set-Content -LiteralPath $envFile -Encoding UTF8
        Write-Ok "PostgreSQL superuser password saved to $envFile"
    }

    # ------------------------------------------------------------ secrets
    # All three must survive a reinstall (same rule as install.sh): changing
    # the DB password breaks the service, and changing RELAY_SECRET makes
    # every stored channel key permanently undecryptable.
    $configPath = Join-Path $InstallDir 'config.yaml'
    $oldDbPass = $null
    $oldSecret = $null
    $oldAdminKey = $null
    if (Test-Path -LiteralPath $configPath) {
        $raw = Get-Content -LiteralPath $configPath -Raw
        if ($raw -match '(?m)^\s*password:\s*"([^"]*)"')      { $oldDbPass = $Matches[1] }
        if ($raw -match '(?m)^\s*secret:\s*"([^"]*)"')        { $oldSecret = $Matches[1] }
        if ($raw -match '(?m)^\s*admin_key:\s*"([^"]*)"')     { $oldAdminKey = $Matches[1] }
    }

    function New-HexSecret {
        param([int]$Bytes = 24)
        $buf = New-Object byte[] $Bytes
        [System.Security.Cryptography.RandomNumberGenerator]::Fill($buf)
        return ($buf | ForEach-Object { $_.ToString('x2') }) -join ''
    }

    $dbPass = if ($oldDbPass) { $oldDbPass; Write-Log 'Reusing the DB password from the existing config' } else { New-HexSecret }
    $relaySecret = if ($oldSecret) { $oldSecret; Write-Log 'Reusing the encryption master key from the existing config' } else { New-HexSecret }
    $adminGenerated = -not $oldAdminKey
    $adminKey = if ($oldAdminKey) { $oldAdminKey; Write-Log 'Reusing the console login key from the existing config' } else { New-HexSecret }

    # ------------------------------------------------------------ database
    Write-Log 'Configuring the database'
    $env:PGPASSWORD = $superPassword
    try {
        $roleExists = (& $psql -w -U postgres -h 127.0.0.1 -p $pgPort -tAc "SELECT 1 FROM pg_roles WHERE rolname='$DbUser'") 2>$null
        if ($roleExists -notmatch '1') {
            & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "CREATE USER $DbUser WITH PASSWORD '$dbPass'" | Out-Null
            if ($LASTEXITCODE -ne 0) { Fail "Failed to create the database user. The superuser password was rejected or PostgreSQL refused the statement (see the psql output above; pass -PgSuperPassword if the password came from an existing instance)." }
        } else {
            # Sync the password even for an existing role, otherwise a rotated
            # password would leave the service unable to connect.
            & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "ALTER USER $DbUser WITH PASSWORD '$dbPass'" | Out-Null
        }
        $dbExists = (& $psql -w -U postgres -h 127.0.0.1 -p $pgPort -tAc "SELECT 1 FROM pg_database WHERE datname='$DbName'") 2>$null
        if ($dbExists -notmatch '1') {
            & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "CREATE DATABASE $DbName OWNER $DbUser" | Out-Null
            if ($LASTEXITCODE -ne 0) { Fail 'Failed to create the database' }
        }
        & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "GRANT ALL PRIVILEGES ON DATABASE $DbName TO $DbUser" | Out-Null
        # PostgreSQL 15+ no longer grants CREATE on public to ordinary users.
        & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -d $DbName -c "GRANT ALL ON SCHEMA public TO $DbUser" | Out-Null
    } finally {
        Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
    }
    Write-Ok 'Database ready'

    # ------------------------------------------------------------ pre-upgrade backup
    # Keyed on the pre-existing install, not on "$exePath exists": by this point
    # the binary has already been extracted, so the path is always present and
    # the check would fire on a first install too (producing a pointless dump).
    if ($hadPreviousInstall) {
        Write-Log 'Existing install detected; backing up the database first'
        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
        $backupFile = Join-Path (Join-Path $InstallDir 'backups') "pre-upgrade-$stamp.sql"
        $pgDump = Join-Path (Split-Path $psql -Parent) 'pg_dump.exe'
        $env:PGPASSWORD = $dbPass
        try {
            & $pgDump -U $DbUser -h 127.0.0.1 -p $pgPort -d $DbName -f $backupFile 2>$null
            if ($LASTEXITCODE -eq 0) {
                Write-Ok "Backed up to $backupFile"
            } else {
                Remove-Item -LiteralPath $backupFile -ErrorAction SilentlyContinue
                Write-Warn 'Database backup failed; continuing (the old binary is still in place, so retry is safe)'
            }
        } finally {
            Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
        }
    }

    # ------------------------------------------------------------ config
    Write-Log 'Writing config'
    $logFile = Join-Path (Join-Path $InstallDir 'logs') 'llm-relay.log'
    $yaml = @"
# Generated by install.ps1 at $(Get-Date -Format o)
# Mirrors deploy/.env.example, as YAML: Windows services have no systemd
# EnvironmentFile, so config comes from this file or system env vars.
server:
  host: "$BindAddr"
  port: $Port
  mode: release
  public_origin: "$PublicOrigin"

database:
  host: 127.0.0.1
  port: $pgPort
  user: "$DbUser"
  password: "$dbPass"
  dbname: "$DbName"
  sslmode: disable
  timezone: Asia/Shanghai

security:
  # Master key for channel keys at rest. Must be preserved across machines,
  # or stored channel keys can never be decrypted again.
  secret: "$relaySecret"
  # Console login key (no accounts): entered in the browser to log in.
  admin_key: "$adminKey"

log:
  level: info
  format: text
  # A Windows service has no stdout consumer, so logs must go to a file.
  file: "$($logFile -replace '\\', '/')"
  max_size_mb: 100

relay:
  payload_storage_mode: errors
  payload_max_kb: 256
  max_concurrency: 64
  default_rpm: 0
  retry_same_upstream_delay: 500ms
"@
    Set-Content -LiteralPath $configPath -Value $yaml -Encoding UTF8

    # ACL: the config holds three secrets, readable only by SYSTEM and
    # Administrators. Equivalent to chmod 600 on Linux.
    & icacls.exe $configPath /inheritance:r /grant:r 'SYSTEM:(R,W)' 'Administrators:(F)' | Out-Null

    # ------------------------------------------------------------ service
    Write-Log 'Registering the Windows service'
    $binPath = "`"$exePath`" -config `"$configPath`""

    if ($existing) {
        # Already registered: only update binPath (config or path may differ).
        & sc.exe config $ServiceName binPath= $binPath start= auto | Out-Null
    } else {
        & sc.exe create $ServiceName binPath= $binPath start= auto DisplayName= 'LLM Relay' | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail 'Failed to register the service (sc.exe create)' }
    }

    & sc.exe description $ServiceName 'LLM Relay - local LLM API gateway' | Out-Null
    # Recovery = systemd's Restart=always. SCM recovery actions only fire on a
    # NON-ZERO exit code (which is why restartService exits with 75 on
    # Windows). reset= 86400 clears the failure counter daily so a crash after
    # a long uptime still restarts.
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/30000 | Out-Null

    # ------------------------------------------------------------ start + health
    Write-Log 'Starting the service'
    $svc = Get-Service -Name $ServiceName
    if ($svc.Status -ne 'Running') {
        Start-Service -Name $ServiceName
        (Get-Service -Name $ServiceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
    }

    Write-Log 'Waiting for the health check'
    $ok = $false
    for ($i = 0; $i -lt 40; $i++) {
        try {
            $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/healthz" -TimeoutSec 3 -UseBasicParsing
            if ($resp.StatusCode -eq 200) { $ok = $true; break }
        } catch { }
        Start-Sleep -Seconds 1
    }

    Write-Host ''
    if ($ok) {
        # Credentials are printed here and written to credentials.txt: the
        # console history is closed or the screenshot lost, and "what was that
        # password" is the most common post-install question.
        $credLines = @()
        $credLines += "LLM Relay $Version  (windows/amd64)"
        $credLines += "Installed: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
        $credLines += ''
        $credLines += "Console:    http://localhost:$Port"
        $credLines += "Admin key:  $adminKey"
        $credLines += ''
        $credLines += 'Database:'
        $credLines += "  Host:    127.0.0.1:$pgPort"
        $credLines += "  Name:    $DbName"
        $credLines += "  User:    $DbUser"
        $credLines += "  Pass:    $dbPass"
        $credLines += "  Super:   postgres / $superPassword"
        $credLines += "  (the superuser password is also in $(Join-Path $InstallDir 'install.env'))"
        $credLines += ''
        $credLines += "Config:    $configPath"
        $credLines += "Log:       $logFile"

        $credFile = Join-Path $InstallDir 'credentials.txt'
        $credLines | Set-Content -LiteralPath $credFile -Encoding UTF8
        & icacls.exe $credFile /inheritance:r /grant:r 'SYSTEM:(R,W)' 'Administrators:(F)' | Out-Null

        # Record which database instance this install uses; the uninstaller
        # reads it to decide how to clean up:
        #   pg_owned = $true  -> dedicated instance, removed entirely on uninstall
        #   pg_owned = $false -> an existing instance, only this project's
        #                        database and role are dropped
        # A separate file rather than a config.yaml field: config is read by the
        # service, this marker is only for the install/uninstall scripts.
        $pgMeta = Join-Path $InstallDir 'install.json'
        @{
            schema        = 1
            pg_owned      = (-not $useExisting)
            pg_port       = $pgPort
            pg_database   = $DbName
            pg_user       = $DbUser
            pg_data_dir   = $(if ($useExisting) { '' } else { $pgData })
            pg_service    = $(if ($useExisting) { '' } else { $PgServiceName })
            installed_at  = (Get-Date -Format 'o')
            llm_relay_ver = $Version
        } | ConvertTo-Json | Set-Content -LiteralPath $pgMeta -Encoding UTF8
        & icacls.exe $pgMeta /inheritance:r /grant:r 'SYSTEM:(R,W)' 'Administrators:(F)' | Out-Null

        Write-Host "Install complete (llm-relay $Version windows/amd64)" -ForegroundColor Green
        Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray
        Write-Host '  Console    ' -NoNewline; Write-Host "http://localhost:$Port" -ForegroundColor Cyan
        Write-Host '  Admin key  ' -NoNewline; Write-Host $adminKey -ForegroundColor Yellow
        if ($adminGenerated) {
            Write-Host '             (newly generated; keep it. Change it in the config and restart to rotate)' -ForegroundColor DarkGray
        }
        Write-Host ''
        Write-Host "  Database   127.0.0.1:$pgPort / $DbName" -ForegroundColor Cyan
        Write-Host '  DB user    ' -NoNewline; Write-Host "$DbUser" -ForegroundColor Yellow
        Write-Host '  DB pass    ' -NoNewline; Write-Host $dbPass -ForegroundColor Yellow
        Write-Host '  PG super   ' -NoNewline; Write-Host "postgres / $superPassword" -ForegroundColor DarkYellow
        Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray
        Write-Host "  Credentials saved to $credFile" -ForegroundColor DarkGray
        Write-Host '  (readable only by SYSTEM and Administrators)' -ForegroundColor DarkGray
        Write-Host ''
        Write-Host "  Install dir: $InstallDir"
        Write-Host "  Config:      $configPath"
        Write-Host ''
        Write-Host '  Common commands:'
        Write-Host "    Get-Service $ServiceName                    # status"
        Write-Host "    Restart-Service $ServiceName                # restart"
        Write-Host "    Get-Content -Wait '$logFile'                # live log"
        Write-Host "    Get-ChildItem '$(Join-Path $InstallDir 'backups')'   # pre-upgrade backups"
        Write-Host ''
        Write-Host '  To uninstall: deploy\uninstall-windows.ps1' -ForegroundColor DarkGray
        # No exit: this file can be invoked by another script, and exit would
        # terminate the caller. Falling off the end naturally is equivalent.
        return
    }

    Write-Warn 'Health check did not pass; last log lines follow:'
    if (Test-Path -LiteralPath $logFile) { Get-Content -LiteralPath $logFile -Tail 40 }
    Write-Host ''
    & sc.exe query $ServiceName
    Fail "Service installed but the health check failed. Check: sc query $ServiceName, log at $logFile"
}
finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
