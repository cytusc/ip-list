# Keep this script UTF-8 with BOM for Windows PowerShell 5.1.
param(
  [Parameter(Mandatory=$true)][string]$Server,
  [Parameter(Mandatory=$true)][string]$Token,
  [string]$Pin = ''
)
$ErrorActionPreference = 'Stop'
if ($Server -notmatch '^https://[a-zA-Z0-9.:/_-]+$') { throw '网站地址无效，必须使用 HTTPS' }
if ($Token -notmatch '^[a-zA-Z0-9_-]+$') { throw '探针密钥格式无效' }
if ($Pin -and $Pin -notmatch '^[a-zA-Z0-9+/=]+$') { throw '服务器公钥指纹格式无效' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { 'arm64' } else { 'amd64' }
$root = Join-Path $env:LOCALAPPDATA 'ip-list-probe'
$temp = Join-Path $env:TEMP ('ip-list-probe-' + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temp | Out-Null
try {
  $binary = Join-Path $temp 'latency-probe.exe'
  $curlArgs = @('--compressed', '--retry', '2', '--fail', '--silent', '--show-error', '--connect-timeout', '15', '--max-time', '180')
  if ($Pin) { $curlArgs += @('--insecure', '--pinnedpubkey', "sha256//$Pin") }
  & curl.exe @curlArgs "$($Server.TrimEnd('/'))/probe-downloads/latency-probe-windows-$arch.exe" -o $binary
  if ($LASTEXITCODE -ne 0) { throw '下载探针失败' }
  $config = @{ Server = $Server.TrimEnd('/'); Token = $Token; TLSPin = $Pin } | ConvertTo-Json
  $configPath = Join-Path $temp 'config.json'
  # UTF-8 without BOM, compatible with Go JSON decoder.
  [IO.File]::WriteAllText($configPath, $config, (New-Object Text.UTF8Encoding($false)))
  & $binary -config $configPath -check
  if ($LASTEXITCODE -ne 0) { throw '连接或密钥检查失败，保留原安装' }
  $taskName = 'IP List Latency Probe'
  Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Path $root -Force | Out-Null
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
  & icacls.exe $root /inheritance:r /grant:r "${identity}:(OI)(CI)F" 'SYSTEM:(OI)(CI)F' | Out-Null
  if ($LASTEXITCODE -ne 0) { throw '无法保护配置目录权限' }
  Copy-Item $binary (Join-Path $root 'latency-probe.exe') -Force
  Copy-Item $configPath (Join-Path $root 'config.json') -Force
  $action = New-ScheduledTaskAction -Execute (Join-Path $root 'latency-probe.exe') -Argument ('-config "' + (Join-Path $root 'config.json') + '"')
  $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity
  $principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
  $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
  Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
  Start-ScheduledTask -TaskName $taskName
  Write-Host '安装完成，探针正在后台运行，登录后自动启动。'
  Write-Host "停止：Stop-ScheduledTask -TaskName '$taskName'"
  Write-Host "卸载：Unregister-ScheduledTask -TaskName '$taskName'"
} finally {
  Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
