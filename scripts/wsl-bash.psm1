<#
.SYNOPSIS
    在 Windows PowerShell 里可靠地跑 WSL Ubuntu 里的 bash 脚本。

.DESCRIPTION
    直接 `wsl -u root -- bash -lc "..."` 有几个反复踩到的坑，这个模块把它们一次解决：

      1. **乱码**：wsl.exe 每次都会往 stderr 打一条固定的中文警告
         （「检测到 localhost 代理配置，但未镜像到 WSL…」），它用 UTF-16LE 输出，
         PowerShell 按 UTF-8 解码 → 一屏乱码。解法是设 `WSL_UTF8=1`（见下），
         而不是去重定向 stderr（重定向会把真正的报错也一起丢掉）。
      2. **引号地狱**：PowerShell → wsl.exe → bash 要过三层解析。
         PowerShell 的**双引号**会先插值（`$name` 被换成值、反引号被当转义），
         所以内联命令一律用**单引号**；脚本里有单引号时改用脚本文件。
      3. **路径**：Windows 路径（`C:\...`）不能直接喂给 bash，要用 `wslpath -u` 转换，
         不要手工拼 `/mnt/c/...`（盘符与大小写都会出错）。
      4. **退出码**：`$LASTEXITCODE` 能正确拿到 bash 的退出码，可用于判断成败。

    设计取舍：**优先用脚本文件而不是内联长命令**。内联命令一旦超过几行，
    引号与转义就变得难以审查；写成 .sh 文件（LF 换行、UTF-8 无 BOM）后
    bash 拿到的就是原始内容，Windows/PowerShell 的解析完全不参与。

.NOTES
    适配环境：Windows 11 + PowerShell 7 + WSL2 Ubuntu 24.04
    （本仓库的部署与验证脚本都在这个组合上跑）
#>

# 关掉 wsl.exe 的 UTF-16 输出，改成 UTF-8 —— 这一行就是乱码问题的解药。
# 放在模块级：Import-Module 之后整个会话都受益。
$env:WSL_UTF8 = '1'
# 让 PowerShell 按 UTF-8 解读外部程序（wsl.exe、git 等）的输出
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

# wsl.exe 每次调用都会往 stderr 打这条固定警告（因为宿主机配了 localhost 代理，
# 而 NAT 模式的 WSL 不支持它）。它和我们要跑的命令毫无关系，却会混进输出里 ——
# 用 WSL_UTF8=1 之后它不再乱码，但依然占着 stdout/stderr，会干扰解析输出。
# 按前缀丢弃它：只认这一条已知噪音，其余 stderr 一律保留（真报错绝不能吞）。
$script:WslNoisePattern = '^\s*wsl:\s*检测到 localhost 代理配置'

<#
.SYNOPSIS
    在 WSL 里执行一条 bash 命令（内联，单参数）。

.PARAMETER Command
    bash 命令。**请用 PowerShell 单引号字符串**传进来，避免 PowerShell 先做插值。
    多行命令直接在单引号里换行即可。

.PARAMETER User
    以哪个用户执行。默认 root —— 部署脚本需要 root 权限。

.PARAMETER WorkDir
    在 WSL 里的工作目录（如 /opt/llm-relay）。不给则沿用当前目录。

.PARAMETER KeepNoise
    保留 wsl.exe 那条 localhost 代理警告。默认丢弃。

.EXAMPLE
    Invoke-WslBash -Command 'systemctl is-active llm-relay'
    Invoke-WslBash -Command 'curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8888/healthz'
#>
function Invoke-WslBash {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, Position = 0)]
        [string]$Command,

        [string]$User = 'root',

        [string]$WorkDir,

        [switch]$KeepNoise
    )

    # 组装给 wsl.exe 的参数。**不用 -lc**：-l 会读 ~/.profile 等启动文件，
    # 它们可能改 PATH、打印额外内容、甚至以非零码退出 —— 那会把脚本的
    # 退出码污染掉（踩过：明明命令成功，$LASTEXITCODE 却是 1）。
    # 用 -c 起一个干净的非登录 shell。
    $wslArgs = @()
    if ($User) { $wslArgs += @('-u', $User) }
    if ($WorkDir) { $wslArgs += @('--cd', $WorkDir) }
    $wslArgs += @('-e', 'bash', '-c', $Command)

    # 2>&1 合并 stderr 是必要的：bash 的报错走 stderr，只抓 stdout 会漏掉真错误
    $raw = & wsl.exe @wslArgs 2>&1
    $code = $LASTEXITCODE

    $lines = @($raw)
    if (-not $KeepNoise) {
        $lines = @($lines | Where-Object { "$_" -notmatch $script:WslNoisePattern })
    }

    [pscustomobject]@{
        Output   = ($lines | Out-String).TrimEnd()
        ExitCode = $code
        Ok       = ($code -eq 0)
    }
}

<#
.SYNOPSIS
    在 WSL 里执行一个 .sh 脚本文件（Windows 路径自动转换）。

.DESCRIPTION
    这是**推荐**的调用方式：脚本内容由 bash 直接读取，不经过 PowerShell
    与 wsl.exe 的引号解析，于是中文、单引号、反引号、$ 全都不需要转义。

    脚本文件本身必须是 LF 换行、UTF-8 无 BOM（见 New-WslScript）。

.EXAMPLE
    Invoke-WslScript -Path .\deploy\install.sh -Arguments '--verify'
#>
function Invoke-WslScript {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, Position = 0)]
        [string]$Path,

        [string[]]$Arguments = @(),

        [string]$User = 'root',

        [string]$WorkDir
    )

    # 相对路径先解析成绝对路径：wslpath 要求路径真实存在，
    # 而相对路径会被按 WSL 的当前目录解释（两者通常不一致）
    $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath
    $wslPath = ConvertTo-WslPath -WindowsPath $resolved

    $cmd = 'bash ' + (ConvertTo-ShellArg $wslPath)
    foreach ($a in $Arguments) { $cmd += ' ' + (ConvertTo-ShellArg $a) }

    Invoke-WslBash -Command $cmd -User $User -WorkDir $WorkDir
}

<#
.SYNOPSIS
    把 Windows 路径转成 WSL 路径（C:\foo → /mnt/c/foo）。

.DESCRIPTION
    用 `wslpath -u` 而不是手工替换：盘符大小写、UNC 路径、空格都要它处理。
#>
function ConvertTo-WslPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, Position = 0)]
        [string]$WindowsPath
    )

    # 必须过滤 wsl.exe 那条噪音：它混进来会让返回值变成长度 80 的
    # 「警告 + 路径」，后续 `bash <这个值>` 直接失败（实测踩到过）。
    # 这里不能复用 Invoke-WslBash —— 那会为了一个路径查询多包一层
    $raw = & wsl.exe -e wslpath -u $WindowsPath 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "路径转换失败：$WindowsPath`n$raw"
    }
    $clean = @($raw | Where-Object { "$_" -notmatch $script:WslNoisePattern })
    $result = ($clean | Out-String).Trim()
    if (-not $result) {
        throw "路径转换返回空：$WindowsPath"
    }
    return $result
}

<#
.SYNOPSIS
    把一段 bash 脚本内容写成 WSL 可直接执行的文件（LF 换行 + UTF-8 无 BOM）。

.DESCRIPTION
    Set-Content 在 Windows 上默认写 CRLF 且可能带 BOM，两者都会让 bash 报错：
      · CRLF：`bash: $'\r': command not found`（行尾多一个 \r）
      · BOM ：`#!/usr/bin/env bash` 前面的 BOM 让 shebang 失效
    这里统一按 LF + 无 BOM 写，并 chmod +x。

.EXAMPLE
    New-WslScript -Path 'C:\temp\check.sh' -Content @'
    #!/usr/bin/env bash
    echo "中文正常: $1"
    '@ | Invoke-WslScript
#>
function New-WslScript {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, Position = 0)]
        [string]$Path,

        [Parameter(Mandatory, Position = 1)]
        [string]$Content
    )

    # PowerShell 里字符串字面量的换行是 CRLF，bash 不认，统一成 LF
    $normalized = $Content -replace "`r`n", "`n" -replace "`r", "`n"
    # utf8NoBOM 是 PowerShell 7 的写法（Windows PowerShell 5.1 没有这个选项）
    Set-Content -LiteralPath $Path -Value $normalized -Encoding utf8NoBOM -NoNewline

    # 顺带把权限设好：WSL 挂载 /mnt/c 时默认可能是 777 或 644，
    # 没有执行位时直接 ./script.sh 会 permission denied
    $wslPath = ConvertTo-WslPath -WindowsPath (Resolve-Path -LiteralPath $Path).ProviderPath
    Invoke-WslBash -Command "chmod +x $(ConvertTo-ShellArg $wslPath)" | Out-Null

    Get-Item -LiteralPath $Path
}

<#
.SYNOPSIS
    给 shell 参数加单引号（内部单引号用 '\'' 转义）。

.DESCRIPTION
    参数里含空格、中文、引号时必需。用单引号而不是双引号：
    bash 的单引号里除了 ' 本身，一切字符都是字面量 —— 包括 $、反引号、\。

    名字以 ConvertTo- 开头是为了用上 PowerShell 的**已批准谓词**：
    未批准的动词（如 Quote-、Escape-）会让 Import-Module 打一条
    「包含未经批准的谓词」警告，把模块导入本身变成噪音源。
#>
function ConvertTo-ShellArg {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, Position = 0)]
        [AllowEmptyString()]
        [string]$Value
    )
    # 单引号内部不能直接写单引号，标准做法是结束单引号 → 插入 \' → 重新开始
    $escaped = $Value -replace "'", "'\''"
    return "'$escaped'"
}
