<#
.SYNOPSIS
  claude-master Windows 一括導入 / 更新スクリプト（install.sh の Windows 版）。

.DESCRIPTION
  Mac の claude-wrap カットオーバー＋launchd 常駐に相当する Windows 構成を
  冪等に組む（M8g 知見を集約）:
    1. claude-master.exe を配置（-Build=ソースビルド / -Source=既存 exe /
       既定=GitHub Release から DL＋sha256 検証。実行中ロックは .old 退避で回避）
    2. `claude` シム（ASCII の .cmd）生成＋User PATH 先頭へ前置（永続）
    3. monitor.cmd / cloud-agent.cmd（ASCII・psmux を PATH 先頭・ログ追記）
    4. ~/.claude-master.toml 雛形（既存は温存）
    5. S4U スケジュールタスク monitor/cloud 登録（要管理者＝自動で自己昇格）
  前提物（自動生成不可・検出して案内）: 実 claude.exe / psmux / sa.json。

  再実行で冪等（既存検出→再利用/修復）。-DryRun で変更せず計画のみ表示。

.PARAMETER BinDir   claude-master.exe の配置先（既定 $env:USERPROFILE\go\bin）
.PARAMETER Repo     OWNER/REPO（既定 4noha/claude-master-go）
.PARAMETER Version  tag（既定 latest）
.PARAMETER Source   既存 claude-master.exe のパス（DL/Build せず流用）
.PARAMETER Build    スクリプトが repo 内なら go build ./cmd/claude-master
.PARAMETER SkipTasks  S4U タスク登録をしない（昇格不可環境向け）
.PARAMETER DryRun   変更せず実行計画のみ表示
.PARAMETER TasksOnly  内部用（自己昇格時に S4U 登録だけ実行）
#>
[CmdletBinding()]
param(
  [string]$BinDir  = (Join-Path $env:USERPROFILE 'go\bin'),
  [string]$Repo    = '4noha/claude-master-go',
  [string]$Version = 'latest',
  [string]$Source  = '',
  [switch]$Build,
  [switch]$SkipTasks,
  [switch]$DryRun,
  [switch]$TasksOnly
)
$ErrorActionPreference = 'Stop'
$CM   = Join-Path $env:USERPROFILE '.claude-master'
$ShimDir = Join-Path $CM 'bin'
$Exe  = Join-Path $BinDir 'claude-master.exe'

function Say([string]$m){ Write-Host "  $m" }
function Step([string]$m){ Write-Host "==> $m" -ForegroundColor Cyan }
function Act([string]$what,[scriptblock]$act){
  if($DryRun){ Write-Host "  [dry-run] $what" -ForegroundColor DarkYellow }
  else { Say $what; & $act }
}
function IsAdmin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  (New-Object Security.Principal.WindowsPrincipal $id).IsInRole(
    [Security.Principal.WindowsBuiltinRole]::Administrator)
}

# ---- S4U タスク登録（要管理者）。-TasksOnly で自己昇格時もここだけ実行 ----
function Register-CMTasks {
  $sid = ([Security.Principal.WindowsIdentity]::GetCurrent()).User.Value
  $defs = @(
    @{ Name='claude-master-monitor'; Cmd=(Join-Path $CM 'monitor.cmd');
       Desc='claude-master monitor (S4U resident)' },
    @{ Name='claude-master-cloud';   Cmd=(Join-Path $CM 'cloud-agent.cmd');
       Desc='claude-master cloud agent (S4U resident)' }
  )
  foreach($d in $defs){
    if($DryRun){ Write-Host "  [dry-run] register S4U task $($d.Name) -> $($d.Cmd)" -ForegroundColor DarkYellow; continue }
    $act = New-ScheduledTaskAction -Execute $d.Cmd
    $trg = New-ScheduledTaskTrigger -AtLogOn
    $pri = New-ScheduledTaskPrincipal -UserId $sid -LogonType S4U -RunLevel Limited
    $set = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries `
            -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew `
            -StartWhenAvailable
    $set.ExecutionTimeLimit = 'PT0S'
    $set.RestartCount = 999
    $set.RestartInterval = 'PT1M'
    Register-ScheduledTask -TaskName $d.Name -Action $act -Trigger $trg `
      -Principal $pri -Settings $set -Description $d.Desc -Force | Out-Null
    Start-ScheduledTask -TaskName $d.Name -ErrorAction SilentlyContinue
    Say "task $($d.Name): registered (S4U) & started"
  }
}

if($TasksOnly){ Register-CMTasks; return }

Write-Host "claude-master Windows installer" -ForegroundColor Green
if($DryRun){ Write-Host "(DRY-RUN: 変更は行いません)" -ForegroundColor DarkYellow }

# ---- 1. ディレクトリ ----
Step "ディレクトリ準備"
foreach($p in @($CM,$ShimDir,$BinDir,(Join-Path $CM 'sessions'))){
  if(Test-Path $p){ Say "exists: $p" }
  else { Act "mkdir $p" { New-Item -ItemType Directory -Force -Path $p | Out-Null } }
}

# ---- 2. バイナリ配置 ----
Step "claude-master.exe 配置 ($Exe)"
$scriptRepo = Split-Path -Parent $PSCommandPath
$haveSrc = (Test-Path (Join-Path $scriptRepo 'go.mod')) -and
           (Test-Path (Join-Path $scriptRepo 'cmd\claude-master'))
function Place-Exe([string]$srcExe){
  # 実行中だと上書き不可 → .old へ rename 退避（M8f1 と同手）。
  if((Test-Path $Exe) -and -not $DryRun){
    try { Move-Item $Exe "$Exe.old" -Force -ErrorAction Stop }
    catch { } # 既に消えている等は無視
  }
  Act "place -> $Exe" { Copy-Item $srcExe $Exe -Force }
  if(-not $DryRun){ Remove-Item "$Exe.old" -Force -ErrorAction SilentlyContinue }
}
if($Source){
  if(-not (Test-Path $Source)){ throw "-Source 不在: $Source" }
  Place-Exe $Source
}
elseif($Build -or ($haveSrc -and -not $DryRun -and -not (Test-Path $Exe))){
  if(-not $haveSrc){ throw "-Build 指定だが repo（go.mod/cmd/claude-master）が見つからない: $scriptRepo" }
  Step "go build ./cmd/claude-master"
  $tmp = Join-Path $env:TEMP ("cm_{0}.exe" -f ([guid]::NewGuid().ToString('N')))
  Act "go build -> $tmp" { Push-Location $scriptRepo; try { & go build -o $tmp ./cmd/claude-master } finally { Pop-Location } }
  if(-not $DryRun){ Place-Exe $tmp; Remove-Item $tmp -Force -ErrorAction SilentlyContinue }
}
elseif(Test-Path $Exe){
  Say "既存 exe を使用（更新は -Build / -Source / -Version で）: $Exe"
}
else {
  # GitHub Release から DL＋sha256（install.sh と同規約）。
  $arch = if($env:PROCESSOR_ARCHITECTURE -match 'ARM64'){ 'arm64' } else { 'amd64' }
  $asset = "claude-master_windows_$arch"
  $base = if($Version -eq 'latest'){ "https://github.com/$Repo/releases/latest/download" }
          else { "https://github.com/$Repo/releases/download/$Version" }
  Step "Release 取得: $Repo $Version (windows/$arch)"
  if($DryRun){ Write-Host "  [dry-run] download $base/$asset(.exe) + checksums.txt, verify sha256" -ForegroundColor DarkYellow }
  else {
    $t = New-Item -ItemType Directory -Force -Path (Join-Path $env:TEMP ("cmdl_"+[guid]::NewGuid().ToString('N')))
    try {
      $bin = Join-Path $t 'cm.exe'; $sum = Join-Path $t 'sums'
      try { Invoke-WebRequest "$base/$asset.exe" -OutFile $bin -UseBasicParsing }
      catch { Invoke-WebRequest "$base/$asset" -OutFile $bin -UseBasicParsing }
      Invoke-WebRequest "$base/checksums.txt" -OutFile $sum -UseBasicParsing
      $want = (Select-String -Path $sum -Pattern ([regex]::Escape($asset)) |
               Select-Object -First 1).Line -split '\s+' | Select-Object -First 1
      if(-not $want){ throw "checksums.txt に $asset が無い（Windows Release 未発行の可能性。-Build か -Source を使用）" }
      $got = (Get-FileHash $bin -Algorithm SHA256).Hash.ToLower()
      if($got -ne $want.ToLower()){ throw "sha256 不一致（中止）" }
      Place-Exe $bin
    } finally { Remove-Item $t -Recurse -Force -ErrorAction SilentlyContinue }
  }
}
if((Test-Path $Exe) -and -not $DryRun){ & $Exe version 2>$null }

# ---- 3. claude シム＋User PATH 前置 ----
Step "claude シム（proxy カットオーバー）"
$shim = Join-Path $ShimDir 'claude.cmd'
$shimBody = "@echo off`r`n`"$Exe`" proxy %*`r`n"
if((Test-Path $shim) -and ((Get-Content $shim -Raw -ErrorAction SilentlyContinue) -eq $shimBody)){
  Say "shim 最新: $shim"
} else {
  Act "write $shim (ASCII)" { Set-Content -Path $shim -Value $shimBody -Encoding ascii -NoNewline }
}
$curUserPath = [Environment]::GetEnvironmentVariable('Path','User')
$parts = @($curUserPath -split ';' | Where-Object { $_ })
if($parts.Count -gt 0 -and $parts[0].TrimEnd('\') -eq $ShimDir.TrimEnd('\')){
  Say "User PATH 先頭に既に: $ShimDir"
} else {
  $rest = $parts | Where-Object { $_.TrimEnd('\') -ne $ShimDir.TrimEnd('\') }
  $newPath = (@($ShimDir)+$rest) -join ';'
  Act "prepend User PATH: $ShimDir" { [Environment]::SetEnvironmentVariable('Path',$newPath,'User') }
  Say "※ 反映には新しいターミナル / 再ログオンが必要"
}

# ---- 4. wrapper .cmd（ASCII。日本語 rem は cmd.exe が OEM 誤実行＝不可） ----
Step "monitor.cmd / cloud-agent.cmd（ASCII）"
$mon = @(
 '@echo off',
 'rem claude-master monitor resident wrapper (Mac launchd equivalent).',
 'setlocal',
 'set "PATH=%USERPROFILE%\psmux;'+$BinDir+';%PATH%"',
 'echo [%DATE% %TIME%] starting claude-master monitor>> "%USERPROFILE%\.claude-master-monitor.log"',
 '"'+$Exe+'" monitor >> "%USERPROFILE%\.claude-master-monitor.log" 2>&1',
 'echo [%DATE% %TIME%] monitor exited rc=%ERRORLEVEL%>> "%USERPROFILE%\.claude-master-monitor.log"',
 'endlocal'
) -join "`r`n"
$cld = @(
 '@echo off',
 'rem claude-master cloud agent resident wrapper (Mac launchd plist env equiv).',
 'setlocal',
 'set "GOOGLE_APPLICATION_CREDENTIALS=%USERPROFILE%\.claude-master\sa.json"',
 'set "PATH=%USERPROFILE%\psmux;'+$BinDir+';%PATH%"',
 'echo [%DATE% %TIME%] starting claude-master cloud agent>> "%USERPROFILE%\.claude-master-cloud.log"',
 '"'+$Exe+'" cloud agent >> "%USERPROFILE%\.claude-master-cloud.log" 2>&1',
 'echo [%DATE% %TIME%] cloud agent exited rc=%ERRORLEVEL%>> "%USERPROFILE%\.claude-master-cloud.log"',
 'endlocal'
) -join "`r`n"
Act "write $CM\monitor.cmd"     { Set-Content -Path (Join-Path $CM 'monitor.cmd')     -Value $mon -Encoding ascii }
Act "write $CM\cloud-agent.cmd" { Set-Content -Path (Join-Path $CM 'cloud-agent.cmd') -Value $cld -Encoding ascii }

# ---- 5. toml 雛形（既存温存） ----
Step "~/.claude-master.toml"
$toml = Join-Path $env:USERPROFILE '.claude-master.toml'
if(Test-Path $toml){ Say "既存を温存: $toml" }
else {
  $tb = "GCP_PROJECT = `"`"`r`nCLOUD_RELAY_URL = `"`"`r`n"
  Act "write $toml (要編集: GCP_PROJECT/CLOUD_RELAY_URL)" { Set-Content -Path $toml -Value $tb -Encoding ascii }
}

# ---- 前提物チェック（自動生成不可・案内のみ） ----
Step "前提物チェック"
$realClaude = Join-Path $env:USERPROFILE '.local\bin\claude.exe'
if(Test-Path $realClaude){ Say "OK 実 claude: $realClaude" }
else { Write-Host "  ! 実 claude.exe 不在（$realClaude）。Claude Code CLI を入れるか REAL_CLAUDE を toml/env で指定" -ForegroundColor Yellow }
if(Test-Path (Join-Path $env:USERPROFILE 'psmux\psmux.exe')){ Say "OK psmux: ~/psmux/psmux.exe" }
else { Write-Host "  ! psmux 不在（~/psmux/psmux.exe）。tmux 同期は無効化（monitor は動作）。psmux を ~/psmux へ配置で有効化" -ForegroundColor Yellow }
if(Test-Path (Join-Path $CM 'sa.json')){ Say "OK SA 鍵: ~/.claude-master/sa.json" }
else { Write-Host "  ! sa.json 不在。cloud 同期は Web『端末を追加』→ ``claude-master cloud enroll <code> --relay wss://…`` で配置" -ForegroundColor Yellow }

# ---- 6. S4U スケジュールタスク（要管理者・自己昇格） ----
if($SkipTasks){
  Step "S4U タスク登録: -SkipTasks 指定によりスキップ"
  Write-Host "  後で: 管理者 PowerShell で `"& '$PSCommandPath' -TasksOnly`"" -ForegroundColor Yellow
}
elseif($DryRun){
  Step "S4U タスク登録（dry-run）"; Register-CMTasks
}
elseif(IsAdmin){
  Step "S4U タスク登録（管理者）"; Register-CMTasks
}
else {
  Step "S4U タスク登録（自己昇格 UAC）"
  try {
    Start-Process powershell -Verb RunAs -Wait -ArgumentList @(
      '-NoProfile','-ExecutionPolicy','Bypass','-File',"`"$PSCommandPath`"",
      '-TasksOnly','-BinDir',"`"$BinDir`"")
    Say "S4U タスク登録（昇格プロセス）完了"
  } catch {
    Write-Host "  ! 自己昇格に失敗/拒否。手動で: 管理者 PowerShell にて" -ForegroundColor Yellow
    Write-Host "    & '$PSCommandPath' -TasksOnly" -ForegroundColor Yellow
  }
}

Write-Host ""
Write-Host "完了。新しいターミナルで `"claude`" を起動するとセッションが Web に出ます。" -ForegroundColor Green
Write-Host "更新: このスクリプト再実行（冪等）／ claude-master update"
