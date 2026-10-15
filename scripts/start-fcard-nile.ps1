param([int]$Port = 8081)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$binary = Join-Path $root 'bin/fcard-nile-server.exe'
$state = Join-Path $root '.cache/nile-server'
if ($Port -lt 1024 -or $Port -gt 65535) { throw 'Invalid local port.' }
if (-not (Test-Path -LiteralPath $binary)) { throw 'Build bin/fcard-nile-server.exe first.' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw 'Requested local port is already occupied.' }
New-Item -ItemType Directory -Force -Path $state | Out-Null
$previous = $env:HTTP_ADDR
try {
    $env:HTTP_ADDR = "127.0.0.1:$Port"
    $process = Start-Process -FilePath $binary -WorkingDirectory $root -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $state "server-$Port.stdout.log") -RedirectStandardError (Join-Path $state "server-$Port.stderr.log")
    $process.Id | Set-Content -LiteralPath (Join-Path $state "server-$Port.pid")
    Write-Host "Fcard Nile process started: pid=$($process.Id), local port=$Port; inspect readiness before payment."
} finally {
    if ($null -eq $previous) { Remove-Item Env:\HTTP_ADDR -ErrorAction SilentlyContinue } else { $env:HTTP_ADDR = $previous }
}
