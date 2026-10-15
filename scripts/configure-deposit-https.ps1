param(
    [Parameter(Mandatory = $true)]
    [string]$PublicOrigin
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$origin = [Uri]$PublicOrigin
if ($origin.Scheme -ne 'https' -or -not $origin.Host -or $origin.UserInfo -or $origin.Query -or $origin.Fragment -or $origin.AbsolutePath -ne '/') {
    throw 'PublicOrigin must be an HTTPS root origin without a path, query or credentials.'
}
$publicRoot = $origin.GetLeftPart([System.UriPartial]::Authority)
$utf8 = New-Object System.Text.UTF8Encoding($false)

function Update-Settings([string]$Path, [System.Collections.IDictionary]$Values) {
    if (-not (Test-Path -LiteralPath $Path)) { throw "Missing configuration: $Path" }
    $lines = [System.Collections.Generic.List[string]]::new()
    $seen = @{}
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        if ($line -match '^([A-Za-z_][A-Za-z0-9_]*)=') {
            $key = $matches[1]
            if ($Values.Contains($key)) {
                if ($seen.ContainsKey($key)) { throw "Duplicate setting: $key" }
                $lines.Add("$key=$($Values[$key])")
                $seen[$key] = $true
                continue
            }
        }
        $lines.Add($line)
    }
    foreach ($key in $Values.Keys) {
        if (-not $seen.ContainsKey($key)) { $lines.Add("$key=$($Values[$key])") }
    }
    [System.IO.File]::WriteAllText($Path, ($lines -join "`n") + "`n", $utf8)
}

$gatewayConfig = Join-Path $root '.cache/epusdt-testnet/conf/.env'
$depositConfig = Join-Path $root '.env.deposits'
if (-not (Test-Path -LiteralPath $gatewayConfig) -or -not (Test-Path -LiteralPath $depositConfig)) {
    throw 'Both existing private configuration files are required.'
}
$depositValues = [ordered]@{
    DEPOSIT_NOTIFY_URL = "$publicRoot/api/v1/deposits/notify"
    EPUSDT_CHECKOUT_ORIGIN = $publicRoot
    EPUSDT_BASE_URL = 'http://127.0.0.1:19800'
}
# Reuse this isolated gateway's merchant credentials without printing them.
$existing = @{}
foreach ($line in [System.IO.File]::ReadAllLines($depositConfig)) {
    if ($line -match '^([A-Za-z_][A-Za-z0-9_]*)=(.*)$') { $existing[$matches[1]] = $matches[2] }
}
if (-not $existing['EPUSDT_PID'] -and -not $existing['EPUSDT_SECRET']) {
    $credentialPath = Join-Path $root '.cache/epusdt-testnet/conf/credentials.json'
    $merchant = Get-Content -LiteralPath $credentialPath -Raw | ConvertFrom-Json
    if ($merchant.environment -ne 'nile' -or -not $merchant.pid -or -not $merchant.secret) {
        throw 'Expected existing Nile merchant credentials.'
    }
    $depositValues['EPUSDT_PID'] = [string]$merchant.pid
    $depositValues['EPUSDT_SECRET'] = [string]$merchant.secret
} elseif (-not $existing['EPUSDT_PID'] -or -not $existing['EPUSDT_SECRET']) {
    throw 'Existing merchant credentials are incomplete; refusing to replace them.'
}
Update-Settings $depositConfig $depositValues
Update-Settings $gatewayConfig ([ordered]@{ app_uri = $publicRoot })
Write-Host 'HTTPS callback and checkout origin configured; receiving switches were not changed.'
Write-Host 'Reload the Caddyfile from deploy/https and restart the gateway to apply app_uri.'
