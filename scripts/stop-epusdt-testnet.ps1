$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$lock = Join-Path (Get-Location) '.cache/epusdt-testnet/compose.env'
if (-not (Test-Path $lock)) { throw 'No test gateway deployment was prepared.' }
docker compose --env-file $lock -f deploy/epusdt/compose.yaml stop gateway edge
if ($LASTEXITCODE -ne 0) { throw 'Could not stop the test gateway.' }
Write-Host 'Test gateway stopped; persistent volumes and credentials were preserved.'
