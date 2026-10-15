param([Parameter(Mandatory = $true)][int]$PreviousProcessId)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$binary = Join-Path $root 'bin/fcard-nile-server.exe'
$ready = Invoke-RestMethod -Uri 'http://127.0.0.1:18082/health/deposits' -TimeoutSec 5
if (-not $ready.data.available -or -not $ready.data.ready -or $ready.data.enabled -or $ready.data.max_amount -ne '1') {
    throw 'Staged runtime must be caught up, with creation closed and a one-token test limit.'
}
$listeners = @(Get-NetTCPConnection -LocalPort 8081 -State Listen -ErrorAction Stop)
if (-not $listeners.Count -or @($listeners | Where-Object { $_.OwningProcess -ne $PreviousProcessId }).Count) {
    throw 'The original listener changed; refusing to stop an unexpected process.'
}
$original = Get-Process -Id $PreviousProcessId -ErrorAction Stop
if ($original.ProcessName -ne 'server' -or -not $original.Path.StartsWith('D:\GoWorkspace\cache\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Original listener does not match the inspected Go development server.'
}
$stagedId = [int](Get-Content -LiteralPath (Join-Path $root '.cache/nile-server/server-18082.pid'))
$staged = Get-Process -Id $stagedId -ErrorAction Stop
if ($staged.Path -ne $binary) { throw 'Staged process differs from the expected binary.' }

# There are no payment instructions until the new runtime has re-established
# catch-up admission on the original port. Preserve the stable database cursor.
Stop-Process -Id $stagedId
$staged.WaitForExit(10000) | Out-Null
Stop-Process -Id $PreviousProcessId
$original.WaitForExit(10000) | Out-Null
$config = Join-Path $root '.env.deposits'
$text = [System.IO.File]::ReadAllText($config)
if ($text -notmatch '(?m)^DEPOSIT_CREATE_ENABLED=false\r?$') { throw 'Unexpected creation switch.' }
$text = [regex]::Replace($text, '(?m)^DEPOSIT_CREATE_ENABLED=false\r?$', 'DEPOSIT_CREATE_ENABLED=true')
[System.IO.File]::WriteAllText($config, $text, (New-Object System.Text.UTF8Encoding($false)))
try {
    & (Join-Path $PSScriptRoot 'start-fcard-nile.ps1') -Port 8081
} catch {
    $text = [regex]::Replace($text, '(?m)^DEPOSIT_CREATE_ENABLED=true\r?$', 'DEPOSIT_CREATE_ENABLED=false')
    [System.IO.File]::WriteAllText($config, $text, (New-Object System.Text.UTF8Encoding($false)))
    throw
}
Write-Host 'Nile runtime moved to 8081; admission remains closed until its scanner catches up again.'
