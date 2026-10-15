param([switch]$Recreate)

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$lock = Join-Path (Get-Location) '.cache/epusdt-testnet/compose.env'
$credentials = Join-Path (Get-Location) '.cache/epusdt-testnet/conf/credentials.json'
if (-not (Test-Path $lock) -or -not (Test-Path $credentials)) { throw 'Start the isolated test gateway first.' }
$before = (Get-FileHash -LiteralPath $credentials -Algorithm SHA256).Hash
$compose = @('compose', '--env-file', $lock, '-f', 'deploy/epusdt/compose.yaml')
if ($Recreate) {
    docker @compose up -d --force-recreate --wait --wait-timeout 120 gateway edge
    if ($LASTEXITCODE -ne 0) { throw 'Test gateway recreation failed.' }
}
$oldBase = $env:EPUSDT_TEST_BASE_URL
$oldCredentials = $env:EPUSDT_TEST_CREDENTIALS_FILE
try {
    $env:EPUSDT_TEST_BASE_URL = 'http://127.0.0.1:19800'
    $env:EPUSDT_TEST_CREDENTIALS_FILE = $credentials
    go test ./internal/epusdt -run '^TestNileGatewayContract$' -count=1 -v
    if ($LASTEXITCODE -ne 0) { throw 'Nile no-funds gateway contract tests failed.' }
} finally {
    if ($null -eq $oldBase) { Remove-Item Env:EPUSDT_TEST_BASE_URL -ErrorAction SilentlyContinue } else { $env:EPUSDT_TEST_BASE_URL = $oldBase }
    if ($null -eq $oldCredentials) { Remove-Item Env:EPUSDT_TEST_CREDENTIALS_FILE -ErrorAction SilentlyContinue } else { $env:EPUSDT_TEST_CREDENTIALS_FILE = $oldCredentials }
}
$after = (Get-FileHash -LiteralPath $credentials -Algorithm SHA256).Hash
if ($before -ne $after) { throw 'Test credentials unexpectedly changed.' }
Write-Host 'Nile no-funds contract checks passed; credentials and database identity are intact.'
