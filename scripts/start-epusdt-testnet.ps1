param([switch]$Build)

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$source = Join-Path (Get-Location) '.cache/epusdt-source'
$state = Join-Path (Get-Location) '.cache/epusdt-testnet'
$revision = '619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3'
$utf8 = New-Object System.Text.UTF8Encoding($false)

$engine = docker info --format '{{.OSType}}'
if ($LASTEXITCODE -ne 0 -or $engine -ne 'linux') { throw 'Docker Desktop Linux engine is required.' }
if (-not (Test-Path (Join-Path $source '.git'))) {
    git clone --depth 1 --branch v2.0.0 https://github.com/GMWalletApp/epusdt.git $source
    if ($LASTEXITCODE -ne 0) { throw 'Epusdt source download failed.' }
}
$actual = git -C $source rev-parse HEAD
if ($LASTEXITCODE -ne 0 -or $actual -ne $revision) { throw 'Epusdt source does not match the audited commit.' }
$changes = git -C $source status --porcelain
if ($LASTEXITCODE -ne 0 -or $changes) { throw 'Cached Epusdt source must be unmodified.' }
New-Item -ItemType Directory -Force -Path (Join-Path $state 'conf') | Out-Null
$lock = Join-Path $state 'compose.env'
if (-not (Test-Path $lock)) {
    $pins = @()
    foreach ($image in @(
        @('GO_IMAGE', 'golang@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440'),
        @('RUNTIME_IMAGE', 'alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8'),
        @('EDGE_IMAGE', 'nginx@sha256:30f1c0d78e0ad60901648be663a710bdadf19e4c10ac6782c235200619158284')
    )) {
        docker pull $image[1]
        if ($LASTEXITCODE -ne 0) { throw "Image download failed: $($image[1])" }
        $pins += "$($image[0])=$($image[1])"
    }
    [System.IO.File]::WriteAllText($lock, ($pins -join "`n") + "`n", $utf8)
}
$config = Join-Path $state 'conf/.env'
if (-not (Test-Path $config)) {
    $settings = @(
        'app_name=Fcard Nile test gateway', 'app_uri=http://127.0.0.1:19800', 'http_listen=:8000',
        'install=false', 'log_level=warn', 'http_access_log=false', 'sql_debug=false', 'static_path=/static',
        'runtime_root_path=/app/runtime', 'log_save_path=/app/runtime/logs', 'log_max_size=16', 'log_max_age=7', 'max_backups=3',
        'db_type=sqlite', 'sqlite_database_filename=/app/data/epusdt.db', 'sqlite_table_prefix=',
        'runtime_sqlite_filename=epusdt-runtime.db', 'queue_concurrency=2', 'queue_poll_interval_ms=1000',
        'callback_retry_base_seconds=5', 'order_expiration_time=10', 'order_notice_max_retry=5', 'api_rate_url='
    )
    [System.IO.File]::WriteAllText($config, ($settings -join "`n") + "`n", $utf8)
}
$compose = @('compose', '--env-file', $lock, '-f', 'deploy/epusdt/compose.yaml')
docker @compose config --quiet
if ($LASTEXITCODE -ne 0) { throw 'Compose configuration is invalid.' }
$imageID = docker image ls --quiet --filter 'reference=fcard/epusdt-nile:619cecb'
if ($LASTEXITCODE -ne 0) { throw 'Could not inspect local Docker images.' }
if ($Build -or -not $imageID) {
    docker @compose --profile setup build bootstrap
    if ($LASTEXITCODE -ne 0) { throw 'Pinned Epusdt build failed.' }
}
docker @compose stop gateway edge
if ($LASTEXITCODE -ne 0) { throw 'Could not stop existing test gateway safely.' }
docker @compose --profile setup run --rm bootstrap
if ($LASTEXITCODE -ne 0) { throw 'Safety bootstrap failed; gateway was not started.' }
docker @compose up -d --wait --wait-timeout 120 gateway edge
if ($LASTEXITCODE -ne 0) { throw 'Gateway failed its health check.' }
if (Test-Path -LiteralPath (Join-Path $state 'conf/nile-receiving.json')) {
    Write-Host 'Nile deployment ready at http://127.0.0.1:19800; reviewed receiving policy applied. Fcard admission is separate.'
} else {
    Write-Host 'Nile deployment ready at http://127.0.0.1:19800; receiving is disabled.'
}
Write-Host 'Local test credentials: .cache/epusdt-testnet/conf/credentials.json (do not commit or share).'
