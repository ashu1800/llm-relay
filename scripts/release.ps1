#!/usr/bin/env powershell
# ============================================================
#  release.ps1 —— 发布一个新版本（Windows 侧入口，逻辑同 scripts/release.sh）
#
#  用法：
#    pwsh -File scripts\release.ps1 patch        # 0.1.0 → 0.1.1（默认）
#    pwsh -File scripts\release.ps1 minor
#    pwsh -File scripts\release.ps1 v0.1.5
#    pwsh -File scripts\release.ps1 --dry-run
#    pwsh -File scripts\release.ps1 patch -Yes
#
#  为什么存在这个文件：发布要调用 gh（GitHub CLI）与 git，而开发机是
#  Windows，gh 装在 Windows 侧、WSL 里没有。bash 版脚本（scripts/release.sh）
#  是唯一实现，本文件只是把参数转发过去 —— 两份实现会漂移，所以这里
#  刻意不做任何逻辑，只负责把 Windows PATH 拼好再调用 bash。
#
#  前提：Windows 侧已装 Git for Windows（提供 bash）与 GitHub CLI，且 gh auth login 过。
# ============================================================
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('patch', 'minor', 'major')]
  [string]$Bump,
  # 显式版本号（v0.1.5 / 0.1.5）。与 -Bump 互斥，但为了脚本简单不硬校验：
  # bash 版会对形状做最终校验。
  [Parameter(Position = 0, ValueFromRemainingArguments = $true)]
  [string]$Version,
  [switch]$DryRun,
  [switch]$Yes
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

# gh 的常见安装位置。winget 装的默认不在 bash 的 PATH 里，这里显式补上。
$ghPaths = @(
  "$env:ProgramFiles\GitHub CLI",
  "$env:LOCALAPPDATA\Programs\GitHub CLI"
) | Where-Object { Test-Path (Join-Path $_ 'gh.exe') }
if ($ghPaths) { $env:Path += ";$($ghPaths[0])" }

$bash = "$env:ProgramFiles\Git\bin\bash.exe"
if (-not (Test-Path $bash)) { $bash = "$env:ProgramFiles\Git\usr\bin\bash.exe" }
if (-not (Test-Path $bash)) { throw "未找到 Git for Windows 的 bash.exe（$bash），请安装 Git for Windows" }

$argv = @()
if ($Bump)   { $argv += $Bump }
if ($Version) { $argv += $Version }
if ($DryRun) { $argv += '--dry-run' }
if ($Yes)    { $argv += '--yes' }

& $bash (Join-Path $root 'scripts\release.sh') @argv
exit $LASTEXITCODE
