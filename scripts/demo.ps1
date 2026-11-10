# End-to-end smoke of the read-only loop against a real process on Windows.
#
# The product targets Linux; this script exists so the same loop can be
# exercised on the development host with Git Bash as the shell. It exits
# non-zero when the task does not reach SEALED or the audit chain does not
# verify, so it cannot pass by accident.
#
# Usage: pwsh scripts/demo.ps1 [-Addr 127.0.0.1:18080]

param(
  [string]$Addr = '127.0.0.1:18080'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("gradops-demo-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null

$bin = Join-Path $work 'gradops.exe'
$db = Join-Path $work 'gradops.db'
$log = Join-Path $work 'server.out'
$err = Join-Path $work 'server.err'
$server = $null

function Cleanup {
  if ($server -and -not $server.HasExited) {
    Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
  }
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

trap { Cleanup }

Write-Host '==> building'
Push-Location $root
try {
  go build -o $bin ./cmd/gradops
  if ($LASTEXITCODE -ne 0) { throw 'build failed' }
} finally {
  Pop-Location
}

Write-Host "==> starting on $Addr"
$env:GRADOPS_ADDR = $Addr
$env:GRADOPS_DB_PATH = $db
$env:GRADOPS_LOG_LEVEL = 'warn'
$env:GRADOPS_ALLOWED_ROOTS = '/var/log'
$env:GRADOPS_WORK_DIR = $work
$env:GRADOPS_CONTRACT_WORK_DIR = '/work'
$env:GRADOPS_BASH = if ($env:GRADOPS_BASH) { $env:GRADOPS_BASH } else { 'C:\Program Files\Git\bin\bash.exe' }

$server = Start-Process -FilePath $bin -PassThru -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError $err

$base = "http://$Addr"
$headers = @{}
if ($env:GRADOPS_API_TOKEN) {
  $headers['Authorization'] = "Bearer $env:GRADOPS_API_TOKEN"
}
$healthy = $false
for ($i = 0; $i -lt 100; $i++) {
  if ($server.HasExited) { throw ("server exited: " + ((Get-Content $log -Raw -ErrorAction SilentlyContinue) + (Get-Content $err -Raw -ErrorAction SilentlyContinue))) }
  try {
    if ((Invoke-RestMethod "$base/healthz" -TimeoutSec 2).status -eq 'ok') { $healthy = $true; break }
  } catch { }
  Start-Sleep -Milliseconds 200
}
if (-not $healthy) { throw 'server did not become healthy' }

Write-Host '==> creating a task'
$task = Invoke-RestMethod "$base/api/tasks" -Method Post -ContentType 'application/json' -Headers $headers `
  -Body (@{ request_text = 'check the disk and report usage' } | ConvertTo-Json)
Write-Host "    task $($task.id)"

Write-Host '==> waiting for the worker'
$status = ''
for ($i = 0; $i -lt 100; $i++) {
  $detail = Invoke-RestMethod "$base/api/tasks/$($task.id)" -Headers $headers
  $status = $detail.status
  if ($status -in @('SEALED', 'FAILED', 'REJECTED')) { break }
  Start-Sleep -Milliseconds 200
}
Write-Host "    status: $status"

Write-Host '==> detail'
Invoke-RestMethod "$base/api/tasks/$($task.id)" -Headers $headers | ConvertTo-Json -Depth 4
Write-Host '==> evidence'
Invoke-RestMethod "$base/api/tasks/$($task.id)/evidence" -Headers $headers | ConvertTo-Json -Depth 4
Write-Host '==> timeline'
Invoke-RestMethod "$base/api/tasks/$($task.id)/timeline" -Headers $headers | ConvertTo-Json -Depth 4
Write-Host '==> audit chain'
$audit = Invoke-RestMethod "$base/api/audit/verify" -Headers $headers
$audit | ConvertTo-Json

if ($status -ne 'SEALED') { throw "expected the task to be sealed, got $status" }
if (-not $audit.ok) { throw 'audit chain check failed' }

Write-Host '==> ok'
Cleanup

