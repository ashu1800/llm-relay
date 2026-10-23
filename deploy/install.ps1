<#
.SYNOPSIS
    LLM Relay —— Windows 一键安装（裸二进制 + Windows 服务）

.DESCRIPTION
    deploy/install.sh 的 Windows 对应物。从 GitHub Releases 下载预编译
    二进制（前端已 embed 进二进制），按需静默安装 PostgreSQL、建库建角色、
    生成配置与随机密钥，注册为 Windows 服务并做健康检查。

    重装安全性：重复运行本脚本就是升级。数据库密码、加密主密钥、
    管理台登录密钥从已安装的 config.yaml 原样沿用（沿用不了会随机新生成
    并打印），升级前自动把数据库备份到 <InstallDir>\backups\。

    与 install.sh 的差异（都是平台决定的，不是取舍）：
      · 没有 systemd，服务由 SCM 托管，恢复策略用 sc failure
        （等价 Restart=always：崩溃/非零退出后自动重启）
      · SCM 不接管 stdout，所以日志写文件（LOG_FILE），不写 journald
      · systemd 的 EnvironmentFile 没有对应物，配置写成 config.yaml
        （YAML 里可以直接放密钥，配合 ACL 收紧）
      · 安装目录默认 %ProgramData%\llm-relay 而不是 Program Files：
        自更新要在可执行文件同目录改名/写临时文件，Program Files 对
        服务账户不可写

.PARAMETER InstallDir
    安装目录。默认 $env:ProgramData\llm-relay

.PARAMETER Port
    监听端口。默认 8888

.PARAMETER BindAddr
    监听地址。默认 127.0.0.1；公网部署改 0.0.0.0 时必须同时给 -PublicOrigin

.PARAMETER PublicOrigin
    管理台允许的固定来源，例如 https://relay.example.com

.PARAMETER DbName / DbUser
    数据库名 / 用户。默认 llm_relay / llmrelay

.PARAMETER Version
    要安装的版本号（不带 v 前缀）。默认取最新 Release

.PARAMETER GhProxy
    GitHub 下载加速前缀（形如 https://gh-proxy.com）

.PARAMETER PgMode
    auto（默认）：检测不到 PostgreSQL 时静默安装
    skip：只检测，不安装（已有实例或想自己装）

.PARAMETER PgSuperPassword
    PostgreSQL 超级用户 postgres 的密码。首次自动安装时为空会自动生成；
    复用已有实例时必须提供（或用 -PgMode skip 并自行建库）

.EXAMPLE
    pwsh -File deploy\install.ps1

.EXAMPLE
    pwsh -File deploy\install.ps1 -Port 9000 -BindAddr 0.0.0.0 -PublicOrigin https://relay.example.com
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
    [string]$PgSuperPassword = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo = 'ashu1800/llm-relay'
$ServiceName = 'llm-relay'
$PgServiceName = 'llm-relay-pg'
$PgMajor = '17'
$PgInstallerVersion = '17.6-1'

function Write-Log  { param([string]$m) Write-Host "==> $m" -ForegroundColor Cyan }
function Write-Ok   { param([string]$m) Write-Host "    $m" -ForegroundColor Green }
function Write-Warn { param([string]$m) Write-Host "[注意] $m" -ForegroundColor Yellow }
function Fail       { param([string]$m) Write-Host "[失败] $m" -ForegroundColor Red; exit 1 }

# ---------------------------------------------------------------- 前置检查
# 必须管理员：要写 ProgramData、注册服务、可能还要装 PostgreSQL。
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail '需要管理员权限。请在「以管理员身份运行」的 PowerShell / 终端里重试。'
}

# TLS 1.2：Windows Server 2016/2012 R2 的默认协议里没有它，
# 不设的话 Invoke-WebRequest 访问 GitHub 会直接报「基础连接已关闭」。
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# 架构：发布矩阵只有 amd64（windows/arm64 在 .goreleaser.yaml 里被 ignore）
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
    Fail "不支持的架构：$arch（发布矩阵只有 windows/amd64）"
}

if ($Port -lt 1 -or $Port -gt 65535) { Fail "PORT 必须在 1-65535 之间" }

# 安全校验与 install.sh 同一口径：非回环监听必须配 PublicOrigin，
# 否则管理台的同源校验会把所有管理请求都挡掉（表现为「登录后一片空白」）。
$loopback = @('127.0.0.1', 'localhost', '::1')
if ($loopback -notcontains $BindAddr -and [string]::IsNullOrWhiteSpace($PublicOrigin)) {
    Fail "BindAddr=$BindAddr 时必须设置 -PublicOrigin（例如 https://relay.example.com）"
}
if ($PublicOrigin -and $PublicOrigin -notmatch '^https?://[^\s/]+$') {
    Fail 'PublicOrigin 必须是完整的 scheme://host（不能带路径、查询或凭据）'
}
# 数据库标识符会进入 SQL：只允许安全字符（与 install.sh 一致）
foreach ($pair in @(@('DbUser', $DbUser), @('DbName', $DbName))) {
    if ($pair[1] -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
        Fail "$($pair[0]) 只能包含字母、数字、下划线，且必须以字母或下划线开头"
    }
}

# ---------------------------------------------------------------- 确定版本
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
    Write-Log '查询最新版本'
    $Version = Resolve-LatestVersion
    if ([string]::IsNullOrWhiteSpace($Version)) {
        Fail 'GitHub API 查询失败。国内网络可设置 -GhProxy <加速前缀> 重试；或手动指定 -Version <版本号>'
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
    # ------------------------------------------------------------ 下载与校验
    Write-Log "下载 $archiveName"
    $archive = Join-Path $tmp $archiveName
    $sums = Join-Path $tmp 'checksums.txt'
    try {
        Invoke-WebRequest -Uri "$relBase/$archiveName" -OutFile $archive -TimeoutSec 300 -UseBasicParsing
        Invoke-WebRequest -Uri "$relBase/checksums.txt" -OutFile $sums -TimeoutSec 60 -UseBasicParsing
    } catch {
        Fail "下载失败：$relBase/$archiveName（GitHub 直连不稳定时，设置 -GhProxy <加速前缀> 后重试）。$($_.Exception.Message)"
    }

    # 校验和缺失即拒绝安装 —— 与 Linux 侧同一口径：这是「从网络下载
    # 可执行文件并以服务身份运行」的最后一道完整性检查。
    $expected = $null
    foreach ($line in Get-Content -LiteralPath $sums) {
        $parts = $line -split '\s+'
        if ($parts.Count -ge 2 -and $parts[1] -eq $archiveName) { $expected = $parts[0]; break }
    }
    if (-not $expected) { Fail "checksums.txt 里没有 $archiveName（版本与架构不匹配？）" }
    $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) {
        Fail "sha256 校验失败：期望 $expected，实际 $actual"
    }
    Write-Ok 'sha256 校验通过'

    # ------------------------------------------------------------ 解压安装
    Write-Log "安装到 $InstallDir"
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $InstallDir 'backups') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $InstallDir 'logs') -Force | Out-Null

    Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force
    $exeSrc = Join-Path $tmp 'llm-relay.exe'
    if (-not (Test-Path -LiteralPath $exeSrc)) { Fail '归档里没有 llm-relay.exe，归档可能已损坏' }

    # 停掉正在运行的服务再覆盖：Windows 上运行中的 exe 删不掉也覆盖不了。
    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existing -and $existing.Status -ne 'Stopped') {
        Write-Log '停止正在运行的服务以便替换程序文件'
        Stop-Service -Name $ServiceName -Force
        (Get-Service -Name $ServiceName).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }

    $exePath = Join-Path $InstallDir 'llm-relay.exe'
    Copy-Item -LiteralPath $exeSrc -Destination $exePath -Force
    if (Test-Path (Join-Path $tmp 'rotate-secret.exe')) {
        Copy-Item -LiteralPath (Join-Path $tmp 'rotate-secret.exe') -Destination (Join-Path $InstallDir 'rotate-secret.exe') -Force
    }

    # ------------------------------------------------------------ PostgreSQL
    $pgsqlRoot = Join-Path $InstallDir 'pgsql'
    $pgBin = Join-Path $pgsqlRoot 'bin'
    $pgData = Join-Path $InstallDir 'pgdata'
    $psql = Join-Path $pgBin 'psql.exe'
    $envFile = Join-Path $InstallDir 'install.env'

    # 超级用户密码：先取参数，再取上次自动安装时记下的，最后随机生成
    # （Windows 密码复杂度要求：大写+小写+数字+符号）。
    function New-PgSuperPassword {
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

    $pgPort = 5432
    # 超管密码必须先初始化 —— Set-StrictMode 下引用未定义变量会直接抛错。
    $superPassword = $PgSuperPassword
    if ([string]::IsNullOrWhiteSpace($superPassword)) { $superPassword = Read-SavedSuperPassword }
    if ([string]::IsNullOrWhiteSpace($superPassword)) { $superPassword = New-PgSuperPassword }

    # 候选 psql：本安装器自己装的那份优先，其次是系统里 EnterpriseDB 的安装包。
    $systemPsql = $null
    foreach ($candidate in @(
            (Join-Path $pgsqlRoot 'bin\psql.exe'),
            (Get-ChildItem 'C:\Program Files\PostgreSQL\*\bin\psql.exe' -ErrorAction SilentlyContinue |
                Sort-Object FullName -Descending | Select-Object -First 1 -ExpandProperty FullName)
        )) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { $systemPsql = $candidate; break }
    }

    # ----------------------------------------------------------------
    # 决定用哪个实例：**能连上才复用，连不上就自己装一个**。
    #
    # 这里曾经是「检测到 psql.exe 就复用」，代价在 2026-10-23 暴露：
    # 机器上装过 EnterpriseDB 的 PostgreSQL 17 时，脚本会把**那个实例**
    # 当成目标，然后用空密码去连 postgres 超级用户 —— 而那个密码是当初
    # 装 PostgreSQL 时设的，安装器既不知道也无从绕过。
    #
    # 更糟的是没传 -w（禁止交互式索要密码）：psql 会反复提示「用户 postgres
    # 的口令」，用户输入什么都是错的，屏幕上看起来像一个死循环。
    #
    # 所以判据改成「真的能连上吗」：
    #   · 连得上 → 复用（用户自己装好并在 pg_hba 里放过认证的实例）
    #   · 连不上 → 不问、不等，直接在 <InstallDir> 下装一个独立实例。
    #     两者端口相同会冲突，所以已有实例占着 5432 时用 5433。
    # 无论哪条路，数据库密码都由本脚本随机生成并最后打印给用户。
    # ----------------------------------------------------------------
    $useExisting = $false
    if ($systemPsql) {
        # -w：永不提示输入密码。连不上就立刻失败，而不是卡在口令提示上。
        $env:PGPASSWORD = $superPassword
        $probe = & $systemPsql -w -U postgres -h 127.0.0.1 -p $pgPort -tAc "SELECT 1" 2>&1
        $probeOk = ($LASTEXITCODE -eq 0 -and "$probe".Trim() -eq '1')
        Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
        if ($probeOk) {
            $useExisting = $true
            $psql = $systemPsql
            Write-Ok "复用已有 PostgreSQL：$systemPsql（端口 $pgPort）"
        } else {
            if ($PgMode -eq 'skip') {
                Fail @"
检测到 PostgreSQL（$systemPsql），但用当前凭据连不上 postgres 超级用户。

当前是 -PgMode skip（不自动安装），请任选其一：
  · 指定已有实例的超级用户密码：-PgSuperPassword <密码>
  · 手动建好库与用户后，用与服务一致的密码重新运行本脚本
  · 去掉 -PgMode skip，让安装器在 $InstallDir 下装一个独立实例（推荐）
"@
            }
            Write-Warn "检测到 PostgreSQL（$systemPsql），但用当前凭据连不上，将安装独立实例"
            Write-Warn '  （已有实例的 postgres 密码是当初安装时设的，本脚本无法得知；'
            Write-Warn '   若想复用它，请用 -PgMode skip -PgSuperPassword <密码> 指定）'
        }
    } elseif ($PgMode -eq 'skip') {
        Fail "未检测到 psql.exe。当前为 -PgMode skip，请先自行安装 PostgreSQL 并把 bin 目录加入 PATH，或去掉该参数让安装器自动安装。"
    }

    if (-not $useExisting) {
        # 独立实例：端口避开已被占用的那个
        if (Get-NetTCPConnection -LocalPort $pgPort -State Listen -ErrorAction SilentlyContinue) {
            $pgPort = 5433
            Write-Warn "端口 5432 已被其它服务占用，独立实例改用 $pgPort"
        }
        Write-Log "下载 PostgreSQL $PgMajor 安装器（约 348MB，首次安装需要几分钟）"
        $pgInstaller = Join-Path $tmp "postgresql-$PgInstallerVersion-windows-x64.exe"
        $pgUrl = "https://get.enterprisedb.com/postgresql/postgresql-$PgInstallerVersion-windows-x64.exe"
        try {
            Invoke-WebRequest -Uri $pgUrl -OutFile $pgInstaller -TimeoutSec 900 -UseBasicParsing
        } catch {
            Fail "下载 PostgreSQL 安装器失败：$($_.Exception.Message)。可改用 -PgMode skip 并自行安装。"
        }

        # 用 optionfile 而不是命令行参数：路径里有空格（ProgramData）
        # 和逗号（locale）时，Start-Process 的参数拼接会静默错位。
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
        Write-Log '静默安装 PostgreSQL'
        $proc = Start-Process -FilePath $pgInstaller `
            -ArgumentList "--optionfile `"$optFile`" --debuglevel 4 --debugtrace `"$debugLog`"" `
            -Wait -PassThru -WindowStyle Hidden
        if ($proc.ExitCode -ne 0) {
            if (Test-Path -LiteralPath $debugLog) {
                Write-Host '----- PostgreSQL 安装日志（末尾 60 行）-----' -ForegroundColor DarkYellow
                Get-Content -LiteralPath $debugLog -Tail 60 | ForEach-Object { Write-Host $_ }
            }
            Fail "PostgreSQL 安装器退出码 $($proc.ExitCode)"
        }
        if (-not (Test-Path -LiteralPath $psql)) {
            Fail "安装器报告成功，但没找到 $psql"
        }
        $pgOwned = $true
        Write-Ok "PostgreSQL $PgMajor 已安装（服务名 $PgServiceName，端口 $pgPort）"

        # 记住超级用户密码：重装时要能再次连上
        @(
            '# 由 install.ps1 生成。含 PostgreSQL 超级用户密码，勿外传。'
            "PG_SUPER_PASSWORD=`"$superPassword`""
        ) | Set-Content -LiteralPath $envFile -Encoding UTF8
        Write-Ok "PostgreSQL 超级用户密码已保存到 $envFile"
    }

    # ------------------------------------------------------------ 密钥生成/沿用
    # 三把密钥重装必须沿用（与 install.sh 同一条规则）：换掉 DB 密码会让
    # 服务连不上库，换掉 RELAY_SECRET 会让已存的渠道密钥永久解不开。
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

    $dbPass = if ($oldDbPass) { $oldDbPass; Write-Log '复用已有配置中的数据库密码' } else { New-HexSecret }
    $relaySecret = if ($oldSecret) { $oldSecret; Write-Log '复用已有配置中的加密主密钥' } else { New-HexSecret }
    $adminGenerated = -not $oldAdminKey
    $adminKey = if ($oldAdminKey) { $oldAdminKey; Write-Log '复用已有配置中的管理台登录密钥' } else { New-HexSecret }

    # 复用已有实例时，超管密码只可能来自 install.env（之前自动装过）
    # —— 已在上面统一读过，这里不再重复。

    # ------------------------------------------------------------ 建库建角色
    Write-Log '配置数据库'
    $env:PGPASSWORD = $superPassword
    try {
        $roleExists = (& $psql -w -U postgres -h 127.0.0.1 -p $pgPort -tAc "SELECT 1 FROM pg_roles WHERE rolname='$DbUser'") 2>$null
        if ($roleExists -notmatch '1') {
            & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "CREATE USER $DbUser WITH PASSWORD '$dbPass'" | Out-Null
            if ($LASTEXITCODE -ne 0) { Fail '创建数据库用户失败（超级用户密码是否正确？可先用 -PgSuperPassword 指定）' }
        } else {
            # 已有用户也要同步密码，否则改过密码后服务连不上
            & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "ALTER USER $DbUser WITH PASSWORD '$dbPass'" | Out-Null
        }
        $dbExists = (& $psql -w -U postgres -h 127.0.0.1 -p $pgPort -tAc "SELECT 1 FROM pg_database WHERE datname='$DbName'") 2>$null
        if ($dbExists -notmatch '1') {
            & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "CREATE DATABASE $DbName OWNER $DbUser" | Out-Null
            if ($LASTEXITCODE -ne 0) { Fail '创建数据库失败' }
        }
        & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -c "GRANT ALL PRIVILEGES ON DATABASE $DbName TO $DbUser" | Out-Null
        # PostgreSQL 15+ 起 public schema 默认不再给普通用户建表权限
        & $psql -w -U postgres -h 127.0.0.1 -p $pgPort -d $DbName -c "GRANT ALL ON SCHEMA public TO $DbUser" | Out-Null
    } finally {
        Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
    }
    Write-Ok '数据库已就绪'

    # ------------------------------------------------------------ 升级前备份
    if (Test-Path -LiteralPath $exePath) {
        Write-Log '检测到已有安装，先备份数据库'
        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
        $backupFile = Join-Path (Join-Path $InstallDir 'backups') "pre-upgrade-$stamp.sql"
        $pgDump = Join-Path (Split-Path $psql -Parent) 'pg_dump.exe'
        $env:PGPASSWORD = $dbPass
        try {
            & $pgDump -U $DbUser -h 127.0.0.1 -p $pgPort -d $DbName -f $backupFile 2>$null
            if ($LASTEXITCODE -eq 0) {
                Write-Ok "已备份到 $backupFile"
            } else {
                Remove-Item -LiteralPath $backupFile -ErrorAction SilentlyContinue
                Write-Warn '数据库备份失败，继续安装（旧二进制仍在原位，可直接重试）'
            }
        } finally {
            Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
        }
    }

    # ------------------------------------------------------------ 写配置
    Write-Log '写入配置'
    $logFile = Join-Path (Join-Path $InstallDir 'logs') 'llm-relay.log'
    $yaml = @"
# 由 install.ps1 生成于 $(Get-Date -Format o)
# 与 deploy/.env.example 的字段一一对应，只是换成 YAML（Windows 服务没有
# systemd 的 EnvironmentFile，配置只能走文件或系统环境变量）。
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
  # 渠道密钥的加密主密钥。换机必须沿用，否则已存库的渠道密钥解不开。
  secret: "$relaySecret"
  # 管理台登录密钥（无账号模型）：浏览器打开管理台时输入它登录。
  admin_key: "$adminKey"

log:
  level: info
  format: text
  # Windows 服务没有 stdout 接收方，日志必须落文件。
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

    # ACL：配置里有三把密钥，只有 SYSTEM 与 Administrators 能读。
    # 等价于 Linux 侧的 chmod 600。
    & icacls.exe $configPath /inheritance:r /grant:r 'SYSTEM:(R,W)' 'Administrators:(F)' | Out-Null

    # ------------------------------------------------------------ 注册服务
    Write-Log '注册 Windows 服务'
    $binPath = "`"$exePath`" -config `"$configPath`""

    if ($existing) {
        # 已存在：只更新 binPath（配置或路径可能变了）
        & sc.exe config $ServiceName binPath= $binPath start= auto | Out-Null
    } else {
        & sc.exe create $ServiceName binPath= $binPath start= auto DisplayName= 'LLM Relay' | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail '注册服务失败（sc.exe create）' }
    }

    & sc.exe description $ServiceName 'LLM Relay - 本地大模型中转服务' | Out-Null
    # 恢复策略 = systemd 的 Restart=always。
    # 注意：SCM 的恢复动作**只对非零退出码生效**（api 层的 restartService
    # 在 Windows 上退出码是 75 正是为此）。reset= 86400 表示一天内
    # 失败计数清零，避免服务稳定运行很久后第一次崩溃就不重启。
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/30000 | Out-Null

    # ------------------------------------------------------------ 启动与健康检查
    Write-Log '启动服务'
    $svc = Get-Service -Name $ServiceName
    if ($svc.Status -ne 'Running') {
        Start-Service -Name $ServiceName
        (Get-Service -Name $ServiceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
    }

    Write-Log '等待健康检查'
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
        # 凭据清单：全部密码都在这里一次性给出，用户装完不必再去翻配置文件。
        # 同时写一份到 <InstallDir>\credentials.txt（ACL 同配置文件），
        # 因为控制台历史会被关掉、截图会丢，而「刚才那个密码是什么」是
        # 部署后最常被问到的问题。
        $credLines = @()
        $credLines += "LLM Relay $Version  (windows/amd64)"
        $credLines += "安装时间: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
        $credLines += ''
        $credLines += "管理后台:   http://localhost:$Port"
        $credLines += "管理密钥:   $adminKey"
        $credLines += ''
        $credLines += '数据库连接:'
        $credLines += "  地址:     127.0.0.1:$pgPort"
        $credLines += "  库名:     $DbName"
        $credLines += "  用户名:   $DbUser"
        $credLines += "  密码:     $dbPass"
        $credLines += "  PG 超管:  postgres / $superPassword"
        $credLines += "  （超管密码同样保存在 $(Join-Path $InstallDir 'install.env')）"
        $credLines += ''
        $credLines += "配置文件:   $configPath"
        $credLines += "日志文件:   $logFile"

        $credFile = Join-Path $InstallDir 'credentials.txt'
        $credLines | Set-Content -LiteralPath $credFile -Encoding UTF8
        & icacls.exe $credFile /inheritance:r /grant:r 'SYSTEM:(R,W)' 'Administrators:(F)' | Out-Null

        Write-Host "部署完成（llm-relay $Version windows/amd64）" -ForegroundColor Green
        Write-Host '────────────────────────────────────────────' -ForegroundColor DarkGray
        Write-Host '  管理后台   ' -NoNewline; Write-Host "http://localhost:$Port" -ForegroundColor Cyan
        Write-Host '  管理密钥   ' -NoNewline; Write-Host $adminKey -ForegroundColor Yellow
        if ($adminGenerated) {
            Write-Host '             （本次新生成，请妥善保存；泄露后修改配置文件并重启服务）' -ForegroundColor DarkGray
        }
        Write-Host ''
        Write-Host "  数据库     127.0.0.1:$pgPort / $DbName" -ForegroundColor Cyan
        Write-Host '  数据库用户 ' -NoNewline; Write-Host "$DbUser" -ForegroundColor Yellow
        Write-Host '  数据库密码 ' -NoNewline; Write-Host $dbPass -ForegroundColor Yellow
        Write-Host '  PG 超管    ' -NoNewline; Write-Host "postgres / $superPassword" -ForegroundColor DarkYellow
        Write-Host '────────────────────────────────────────────' -ForegroundColor DarkGray
        Write-Host "  凭据已保存到 $credFile" -ForegroundColor DarkGray
        Write-Host '  （仅 SYSTEM 与 Administrators 可读）' -ForegroundColor DarkGray
        Write-Host ''
        Write-Host "  安装目录: $InstallDir"
        Write-Host "  配置文件: $configPath"
        Write-Host ''
        Write-Host '  常用命令：'
        Write-Host "    Get-Service $ServiceName                    # 服务状态"
        Write-Host "    Restart-Service $ServiceName                # 重启"
        Write-Host "    Get-Content -Wait '$logFile'                # 实时日志"
        Write-Host "    Get-ChildItem '$(Join-Path $InstallDir 'backups')'   # 升级前备份"
        exit 0
    }

    Write-Warn '健康检查未通过，最近日志如下：'
    if (Test-Path -LiteralPath $logFile) { Get-Content -LiteralPath $logFile -Tail 40 }
    Write-Host ''
    & sc.exe query $ServiceName
    Fail "服务已安装但未通过健康检查。排查：服务状态用 sc query $ServiceName，日志在 $logFile"
}
finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
