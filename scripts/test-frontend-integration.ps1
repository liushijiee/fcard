$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$previous = @{}
foreach ($name in @('DATABASE_URL', 'ADMIN_PASSWORD', 'APP_ENV', 'HTTP_ADDR', 'BACKEND_TARGET', 'FCARD_E2E_PASSWORD', 'AIRWALLEX_ISSUING_ENABLED')) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$databaseName = 'fcard_test_frontend_' + [Guid]::NewGuid().ToString('N')
$created = $false
$server = $null
try {
    $settings = @{}
    Get-Content -LiteralPath '.env.postgres' | ForEach-Object {
        if ($_ -match '^([A-Z_]+)=(.*)$') { $settings[$matches[1]] = $matches[2].Trim() }
    }
    $username = [Uri]::EscapeDataString($settings['POSTGRES_USER'])
    $password = [Uri]::EscapeDataString($settings['POSTGRES_PASSWORD'])
    $hostName = $settings['POSTGRES_HOST']
    $port = $settings['POSTGRES_PORT']
    docker exec fcard psql -U $settings['POSTGRES_USER'] -d $settings['POSTGRES_DB'] -c "CREATE DATABASE $databaseName;"
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated frontend test database' }
    $created = $true
    $env:DATABASE_URL = "postgres://${username}:${password}@${hostName}:${port}/${databaseName}?sslmode=disable"
    $env:APP_ENV = 'sandbox'
    $env:AIRWALLEX_ISSUING_ENABLED = 'false'
    $env:HTTP_ADDR = '127.0.0.1:18081'
    $env:BACKEND_TARGET = 'http://127.0.0.1:18081'
    $bytes = New-Object byte[] 24
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $rng.GetBytes($bytes)
    $rng.Dispose()
    $env:ADMIN_PASSWORD = [Convert]::ToBase64String($bytes)
    $env:FCARD_E2E_PASSWORD = $env:ADMIN_PASSWORD
    go run ./cmd/migrate
    if ($LASTEXITCODE -ne 0) { throw 'Test database migration failed' }
    go run ./cmd/admin --email e2e@fcard.test
    if ($LASTEXITCODE -ne 0) { throw 'Test administrator initialization failed' }
    Remove-Item Env:\ADMIN_PASSWORD
    go build -o bin/fcard-frontend-test.exe ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'Backend build failed' }
    $server = Start-Process -FilePath (Join-Path $root 'bin/fcard-frontend-test.exe') -WorkingDirectory $root -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $root 'bin/frontend-test.stdout.log') -RedirectStandardError (Join-Path $root 'bin/frontend-test.stderr.log')
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        Start-Sleep -Milliseconds 300
        $server.Refresh()
        if ($server.HasExited) { throw 'Isolated test backend exited; check port 18081 and bin/frontend-test.stderr.log' }
        try {
            $response = Invoke-RestMethod 'http://127.0.0.1:18081/health/ready' -TimeoutSec 2
            if ($response.data.status -eq 'ready') { $ready = $true; break }
        } catch { }
    }
    if (-not $ready) { throw 'Isolated backend readiness timed out' }
    Set-Location (Join-Path $root 'frontend')
    npm run test:e2e
    if ($LASTEXITCODE -ne 0) { throw 'Frontend browser integration tests failed' }
} finally {
    Set-Location $root
    if ($server) {
        $server.Refresh()
        if (-not $server.HasExited) { Stop-Process -Id $server.Id }
    }
    if ($created) {
        docker exec fcard psql -U $settings['POSTGRES_USER'] -d $settings['POSTGRES_DB'] -c "DROP DATABASE $databaseName WITH (FORCE);"
        if ($LASTEXITCODE -ne 0) { Write-Warning "Could not remove disposable database $databaseName" }
    }
    foreach ($name in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
}
