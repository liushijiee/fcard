$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
go test ./internal/deposit -count=1
if ($LASTEXITCODE -ne 0) { throw 'Deposit regression failed.' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'Static checks failed.' }
$binary = Join-Path $root 'bin/fcard-nile-server.exe'
$pidFile = Join-Path $root '.cache/nile-server/server-18082.pid'
if (Test-Path -LiteralPath $pidFile) {
    $serverProcessId = [int](Get-Content -LiteralPath $pidFile)
    $existingProcess = Get-Process -Id $serverProcessId -ErrorAction SilentlyContinue
    if ($existingProcess) {
        if ($existingProcess.Path -ne $binary) { throw 'Recorded process does not match the Nile binary.' }
        Stop-Process -Id $serverProcessId
        $existingProcess.WaitForExit(10000) | Out-Null
    }
}
go build -o bin/fcard-nile-server.exe ./cmd/server
if ($LASTEXITCODE -ne 0) { throw 'Nile backend build failed.' }
& (Join-Path $PSScriptRoot 'start-fcard-nile.ps1') -Port 18082
go run ./cmd/deposit-status
if ($LASTEXITCODE -ne 0) { throw 'Scanner progress query failed.' }
