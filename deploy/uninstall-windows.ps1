<#
.SYNOPSIS
    LLM Relay -- Windows uninstaller.

.DESCRIPTION
    Removes what install.ps1 created. Two entry points, one file:

        irm https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/uninstall-windows.ps1 | iex
        pwsh -File .\deploy\uninstall-windows.ps1

    It first lists what exists, then asks what to remove:

        [1] Keep data (default) -- remove the service and program files only;
            the dedicated PostgreSQL service, database, config, logs,
            credentials and backups all stay.
        [2] Remove data too -- needs a typed DELETE confirmation.

    WHAT "REMOVE DATA" MEANS DEPENDS ON THE INSTALL

    install.ps1 records which database it used in <InstallDir>\install.json:

      * pg_owned = true  -- a dedicated PostgreSQL instance created by the
        installer. Uninstall stops and deletes that service and removes its
        data directory with the install directory.

      * pg_owned = false -- an existing PostgreSQL the user chose to reuse.
        Uninstall drops ONLY this project's database and role. It never
        uninstalls or stops that PostgreSQL, because other applications on
        the machine share it. Dropping the role needs the superuser password,
        so you are asked for it (Enter skips and prints the SQL instead).

    ENCODING: plain ASCII on purpose -- see the note in install.ps1 (PS 5.1
    decodes BOM-less scripts as ANSI; a BOM breaks `irm | iex`).

.PARAMETER InstallDir
    Install directory. Defaults to $env:ProgramData\llm-relay. Only change it
    if the service was installed elsewhere.

.PARAMETER KeepData
    Do not prompt; remove service and program files, keep all data.

.PARAMETER Purge
    Do not prompt; remove everything (still asks for the DELETE confirmation
    unless -Force is also given).

.PARAMETER Force
    Never prompt (used by scripted removal).

.PARAMETER GhProxy
    GitHub download accelerator, only used by the one-click form.

.EXAMPLE
    .\uninstall-windows.ps1

.EXAMPLE
    .\uninstall-windows.ps1 -Purge -Force
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramData 'llm-relay'),
    [switch]$KeepData,
    [switch]$Purge,
    [switch]$Force,
    [string]$GhProxy = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# ---------------------------------------------------------------- self-bootstrap
#
# Same mechanism as install.ps1: under `irm | iex` this file runs inside a
# scriptblock of the caller's session ($PSCommandPath is empty), and a
# terminating error there would close their terminal. Download ourselves to a
# temp file and re-run with -File so the rest only deals with file mode.
if ([string]::IsNullOrEmpty($PSCommandPath) -or [string]::IsNullOrEmpty($PSScriptRoot)) {
    $selfUrl = 'https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/uninstall-windows.ps1'
    if ($GhProxy) { $selfUrl = ($GhProxy.TrimEnd('/')) + '/' + $selfUrl }

    $principal = New-Object Security.Principal.WindowsPrincipal(
        [Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Administrator rights are required. Reopen PowerShell as administrator and retry.'
    }

    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $boot = Join-Path $env:TEMP ('llm-relay-unboot-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    New-Item -ItemType Directory -Path $boot -Force | Out-Null
    try {
        $selfFile = Join-Path $boot 'uninstall-windows.ps1'
        Write-Host "==> Fetching uninstaller: $selfUrl" -ForegroundColor Cyan
        Invoke-WebRequest -Uri $selfUrl -OutFile $selfFile -TimeoutSec 60 -UseBasicParsing
        try { Unblock-File -LiteralPath $selfFile -ErrorAction Stop } catch { }

        $exe = (Get-Command pwsh.exe -ErrorAction SilentlyContinue).Source
        if (-not $exe) { $exe = (Get-Command powershell.exe -ErrorAction Stop).Source }
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

# No `exit` anywhere: this script may be invoked by another script, and exit
# would terminate the caller.
function Fail { param([string]$m) throw $m }

function Write-Head { param([string]$m) Write-Host ''; Write-Host $m -ForegroundColor Cyan }
function Write-Ok { param([string]$m) Write-Host "    $m" -ForegroundColor Green }
function Write-Warn2 { param([string]$m) Write-Host "[WARN] $m" -ForegroundColor Yellow }

# ---------------------------------------------------------------- admin
$principal = New-Object Security.Principal.WindowsPrincipal(
    [Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail @'
Administrator rights are required (stopping/deleting a Windows service and
removing files under %ProgramData%).

Reopen your terminal as administrator, then run the command again:
  * Start menu -> search "PowerShell" -> right-click -> "Run as administrator"
  * Win+X -> "Terminal (Admin)" or "Windows PowerShell (Admin)"
'@
}

# ---------------------------------------------------------------- survey
# Show what exists before touching anything: users need to know what will go.
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
$hasDir = Test-Path -LiteralPath $InstallDir
$pgDataDir = Join-Path $InstallDir 'pgdata'
$pgsqlDir = Join-Path $InstallDir 'pgsql'
$backupDir = Join-Path $InstallDir 'backups'
$configPath = Join-Path $InstallDir 'config.yaml'
$metaPath = Join-Path $InstallDir 'install.json'

# Database provenance comes from install.json. Without it (an install made by
# an older version), infer from the directory layout -- that is what those
# versions created.
$pgOwned = $false
$pgPort = 5432
$pgDb = 'llm_relay'
$pgUser = 'llmrelay'
$pgSvcName = $PgServiceName
if (Test-Path -LiteralPath $metaPath) {
    try {
        $meta = Get-Content -LiteralPath $metaPath -Raw | ConvertFrom-Json
        # Property existence is checked explicitly: under Set-StrictMode a
        # missing property is a terminating error, not $null, and a marker
        # written by an older version lacks several of these fields.
        if ($meta.PSObject.Properties['pg_owned']) { $pgOwned = [bool]$meta.pg_owned }
        if ($meta.PSObject.Properties['pg_port'] -and $meta.pg_port) { $pgPort = [int]$meta.pg_port }
        if ($meta.PSObject.Properties['pg_database'] -and $meta.pg_database) { $pgDb = $meta.pg_database }
        if ($meta.PSObject.Properties['pg_user'] -and $meta.pg_user) { $pgUser = $meta.pg_user }
        if ($meta.PSObject.Properties['pg_service'] -and $meta.pg_service) { $pgSvcName = $meta.pg_service }
    } catch {
        Write-Warn2 "Could not read $metaPath; inferring database provenance from the directory"
        $pgOwned = (Test-Path -LiteralPath $pgDataDir)
    }
} else {
    $pgOwned = (Test-Path -LiteralPath $pgDataDir)
}

$backupCount = 0
if (Test-Path -LiteralPath $backupDir) {
    $backupCount = @(Get-ChildItem -LiteralPath $backupDir -File -ErrorAction SilentlyContinue).Count
}

if (-not $svc -and -not $hasDir) {
    Write-Host ''
    Write-Host 'No LLM Relay installation found.' -ForegroundColor Yellow
    Write-Host "  Service $ServiceName does not exist and $InstallDir is absent." -ForegroundColor DarkGray
    Write-Host '  It may already be uninstalled, or installed elsewhere (use -InstallDir).' -ForegroundColor DarkGray
    return
}

Write-Host ''
Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray
Write-Host '  LLM Relay uninstall' -ForegroundColor Cyan
Write-Host ''
Write-Host "  Install dir  $InstallDir"
if ($svc) {
    Write-Host "  Service      $ServiceName (currently $($svc.Status))"
} else {
    Write-Host '  Service      not registered'
}
if ($pgOwned) {
    Write-Host '  Database     dedicated instance created by this installer' -ForegroundColor Yellow
    Write-Host "               port $pgPort, data at $pgDataDir" -ForegroundColor DarkGray
    Write-Host '               removing data deletes the instance as well' -ForegroundColor DarkGray
} else {
    Write-Host '  Database     an existing PostgreSQL instance you chose to reuse' -ForegroundColor Cyan
    Write-Host "               127.0.0.1:$pgPort / $pgDb (user $pgUser)" -ForegroundColor DarkGray
    Write-Host '               removing data drops only this project''s DB and role;' -ForegroundColor DarkGray
    Write-Host '               PostgreSQL itself is left installed' -ForegroundColor DarkGray
}
if ($backupCount -gt 0) {
    Write-Host "  Backups      $backupCount file(s) in $backupDir" -ForegroundColor Yellow
}
Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray

# ---------------------------------------------------------------- choose
# Default to keeping data: deleting a database is irreversible, and having the
# data still there is the common case (reinstall later).
$mode = ''
if ($Purge) {
    $mode = 'purge'
} elseif ($KeepData) {
    $mode = 'keep'
} elseif ($Force) {
    $mode = 'keep'
} else {
    Write-Host ''
    Write-Host '  Choose how to uninstall:'
    Write-Host '    [1] Keep data (default)   ' -NoNewline -ForegroundColor Cyan
    Write-Host 'remove the service and program files only'
    Write-Host '    [2] Remove data too       ' -NoNewline -ForegroundColor Yellow
    if ($pgOwned) {
        Write-Host 'delete the database instance, all data and backups'
    } else {
        Write-Host 'drop this project''s database and role (PostgreSQL stays installed)'
    }
    Write-Host ''
    while (-not $mode) {
        $answer = Read-Host '  Enter 1 or 2 (press Enter for 1)'
        switch ($answer.Trim()) {
            '1' { $mode = 'keep'; break }
            '2' { $mode = 'purge'; break }
            ''  { $mode = 'keep'; break }
            default { Write-Warn2 'Please enter 1 or 2' }
        }
    }
}

if ($mode -eq 'purge' -and -not $Force) {
    Write-Host ''
    if ($pgOwned) {
        Write-Warn2 'This permanently deletes the database instance, all data and every backup.'
    } else {
        Write-Warn2 "This permanently deletes database $pgDb and role $pgUser (PostgreSQL itself is untouched)."
    }
    $typed = Read-Host '  Type DELETE to confirm (anything else cancels)'
    if ($typed.Trim() -ne 'DELETE') {
        Write-Host ''
        Write-Host 'Cancelled; nothing was changed.' -ForegroundColor Yellow
        return
    }
}

# ---------------------------------------------------------------- stop
Write-Head 'Stopping services'
# Only our own service. When reusing the user's PostgreSQL, that service is
# shared with other applications -- stopping it would disrupt them.
#
# The dedicated PostgreSQL service is stopped only on a purge: "keep data"
# must leave it running, because the data is only usable through that service
# and a later reinstall reuses it (install.ps1 looks for the service and
# fails if it is gone).
$stopServices = @($ServiceName)
if ($pgOwned -and $mode -eq 'purge') { $stopServices += $pgSvcName }
foreach ($n in $stopServices) {
    $s = Get-Service -Name $n -ErrorAction SilentlyContinue
    if ($s -and $s.Status -ne 'Stopped') {
        Write-Ok "Stopping $n ($($s.Status))"
        Stop-Service -Name $n -Force -ErrorAction SilentlyContinue
        try { $s.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30)) } catch { }
    }
}

# ---------------------------------------------------------------- delete services
Write-Head 'Removing services'
# Same rule as the stop above: the dedicated PostgreSQL service is removed
# only on a purge. Deleting it under "keep data" used to leave pgdata\ intact
# but unstartable, while this script told the user a reinstall would reuse it
# (2026-10-23).
$servicesToDelete = @($ServiceName)
if ($pgOwned -and $mode -eq 'purge') { $servicesToDelete += $pgSvcName }
foreach ($n in $servicesToDelete) {
    $s = Get-Service -Name $n -ErrorAction SilentlyContinue
    if ($s) {
        & sc.exe delete $n | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Ok "Removed service $n"
        } else {
            Write-Warn2 "Failed to remove service $n (exit code $LASTEXITCODE)"
        }
    }
}

# ---------------------------------------------------------------- database
# The two provenance cases differ completely -- this is the most important
# branch in the uninstaller:
#
#   dedicated: an instance this installer created. Service already removed,
#              its data directory goes with the install directory.
#   existing : the user's own PostgreSQL. NEVER touch that installation;
#              drop only this project's database and role.
if ($mode -eq 'purge') {
    Write-Head 'Removing database'

    $psql = $null
    foreach ($candidate in @(
            (Join-Path $pgsqlDir 'bin\psql.exe'),
            (Get-ChildItem 'C:\Program Files\PostgreSQL\*\bin\psql.exe' -ErrorAction SilentlyContinue |
                Sort-Object FullName -Descending | Select-Object -First 1 -ExpandProperty FullName)
        )) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { $psql = $candidate; break }
    }

    if ($pgOwned) {
        Write-Ok "Dedicated instance $pgSvcName stopped and removed; its data directory goes with the install dir"
    } else {
        if (-not $psql) {
            Write-Warn2 'psql.exe not found; cannot drop this project''s database automatically.'
            Write-Warn2 "  Run manually: DROP DATABASE $pgDb; DROP USER $pgUser;"
        } else {
            $pgPass = ''
            if (Test-Path -LiteralPath $configPath) {
                $raw = Get-Content -LiteralPath $configPath -Raw
                if ($raw -match '(?m)^\s*password:\s*"([^"]*)"') { $pgPass = $Matches[1] }
            }
            # Connect as the application role (it owns the database, so it can
            # drop it). We deliberately do NOT ask for the postgres superuser
            # password just to drop the database: uninstalling should not be
            # harder than installing.
            $env:PGPASSWORD = $pgPass
            try {
                & $psql -w -U $pgUser -h 127.0.0.1 -p $pgPort -d postgres -c "DROP DATABASE IF EXISTS $pgDb" 2>&1 | Out-Null
                if ($LASTEXITCODE -eq 0) {
                    Write-Ok "Dropped database $pgDb"
                } else {
                    Write-Warn2 "Failed to drop database $pgDb (insufficient privileges? run as the postgres superuser)"
                    Write-Warn2 "  DROP DATABASE $pgDb;"
                }
            } finally {
                Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
            }

            # The role is a separate matter and cannot be done as $pgUser:
            # PostgreSQL refuses "DROP ROLE" for the current user ("current user
            # cannot be dropped"), and a role owning objects cannot be dropped
            # either -- so the database must go first (above) and the drop needs
            # the superuser. Rather than leave the role behind silently -- the
            # earlier version did exactly that while claiming the role was
            # removed -- ask for the superuser password, and say plainly what is
            # left to do if it is not given.
            $pgSuper = ''
            if (-not $Force -and [Environment]::UserInteractive) {
                Write-Host ''
                Write-Host "  The role $pgUser cannot drop itself; dropping it needs the" -ForegroundColor DarkGray
                Write-Host '  postgres superuser password (Enter to skip and keep the role):' -ForegroundColor DarkGray
                $secure = Read-Host "  postgres password for the instance at 127.0.0.1:$pgPort" -AsSecureString
                $pgSuper = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
                    [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
            }
            if ([string]::IsNullOrWhiteSpace($pgSuper)) {
                Write-Warn2 "Role $pgUser was left in place (no superuser password given)."
                Write-Warn2 "  psql -U postgres -c `"DROP ROLE IF EXISTS $pgUser`""
            } else {
                $env:PGPASSWORD = $pgSuper
                try {
                    & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -d postgres -c "DROP ROLE IF EXISTS $pgUser" 2>&1 | Out-Null
                    if ($LASTEXITCODE -eq 0) {
                        Write-Ok "Dropped role $pgUser"
                    } else {
                        Write-Warn2 "Failed to drop role $pgUser (still owns objects outside this database?)"
                        Write-Warn2 "  DROP OWNED BY $pgUser; DROP ROLE $pgUser;"
                    }
                } finally {
                    Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
                }
            }
            Write-Ok 'The existing PostgreSQL instance was left installed and running'
        }
    }
}

# ---------------------------------------------------------------- files
Write-Head 'Removing files'
if ($mode -eq 'keep') {
    # Program files only; config, logs, database, backups and credentials stay.
    foreach ($f in @('llm-relay.exe', 'rotate-secret.exe')) {
        $p = Join-Path $InstallDir $f
        if (Test-Path -LiteralPath $p) {
            Remove-Item -LiteralPath $p -Force -ErrorAction SilentlyContinue
            Write-Ok "Removed $f"
        }
    }
    if ($pgOwned) {
        Write-Warn2 "The dedicated PostgreSQL files remain in $pgsqlDir and $pgDataDir"
        Write-Warn2 "  Its service $pgSvcName was kept too, so a reinstall reuses it directly."
        Write-Warn2 '  No data is lost.'
    } else {
        Write-Ok 'The database lives in your own PostgreSQL instance and was not touched'
    }
    Write-Host ''
    Write-Host '  Kept:' -ForegroundColor DarkGray
    foreach ($item in @(
            @('config  ', $configPath),
            @('creds   ', (Join-Path $InstallDir 'credentials.txt')),
            @('logs    ', (Join-Path $InstallDir 'logs')),
            @('backups ', $backupDir)
        )) {
        if (Test-Path -LiteralPath $item[1]) {
            Write-Host "    $($item[0])  $($item[1])" -ForegroundColor DarkGray
        }
    }
} else {
    # Remove everything. Give the services a moment to release their locks.
    Start-Sleep -Seconds 2
    if (Test-Path -LiteralPath $InstallDir) {
        try {
            Remove-Item -LiteralPath $InstallDir -Recurse -Force -ErrorAction Stop
            Write-Ok "Removed $InstallDir"
        } catch {
            Write-Warn2 "Error removing $InstallDir : $($_.Exception.Message)"
            Write-Warn2 '  Files are usually still locked. Reboot once and run this again.'
        }
    }
    $cacheDir = Join-Path $env:LOCALAPPDATA 'llm-relay\cache'
    if (Test-Path -LiteralPath $cacheDir) {
        Remove-Item -LiteralPath $cacheDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Ok "Removed download cache $cacheDir"
    }
}

# ---------------------------------------------------------------- summary
Write-Host ''
Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray
if ($mode -eq 'keep') {
    Write-Host '  Uninstall complete (data kept)' -ForegroundColor Green
    Write-Host ''
    if (-not $pgOwned) {
        Write-Host "  Database $pgDb is still in your PostgreSQL; a reinstall can reuse it." -ForegroundColor DarkGray
    }
    Write-Host '  Run again and choose option 2 to remove the data as well.' -ForegroundColor DarkGray
} else {
    Write-Host '  Uninstall complete (data removed)' -ForegroundColor Green
    if (-not $pgOwned) {
        Write-Host ''
        Write-Host '  Your PostgreSQL installation was not uninstalled; only this' -ForegroundColor DarkGray
        Write-Host '  project''s database and role were dropped.' -ForegroundColor DarkGray
    }
}
Write-Host '------------------------------------------------------------' -ForegroundColor DarkGray
