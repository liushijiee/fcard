$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$previous = $env:TEST_DATABASE_URL
try {
    if (-not $env:TEST_DATABASE_URL) {
        $settings = @{}
        Get-Content -LiteralPath '.env.postgres' | ForEach-Object {
            if ($_ -match '^([A-Z_]+)=(.*)$') { $settings[$matches[1]] = $matches[2].Trim() }
        }
        $username = [Uri]::EscapeDataString($settings['POSTGRES_USER'])
        $password = [Uri]::EscapeDataString($settings['POSTGRES_PASSWORD'])
        $hostName = $settings['POSTGRES_HOST']
        $port = $settings['POSTGRES_PORT']
        $database = [Uri]::EscapeDataString($settings['POSTGRES_DB'])
        $env:TEST_DATABASE_URL = "postgres://${username}:${password}@${hostName}:${port}/${database}?sslmode=disable"
    }
    go test ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw 'Integration tests failed' }
} finally {
    if ($null -eq $previous) { Remove-Item Env:\TEST_DATABASE_URL -ErrorAction SilentlyContinue }
    else { $env:TEST_DATABASE_URL = $previous }
}
