$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$before = @{}
Get-ChildItem -LiteralPath 'internal/storage/db' -Filter '*.go' | ForEach-Object {
    $before[$_.Name] = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
}
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
if ($LASTEXITCODE -ne 0) { throw 'sqlc generation failed' }
$after = @{}
Get-ChildItem -LiteralPath 'internal/storage/db' -Filter '*.go' | ForEach-Object {
    $after[$_.Name] = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
}
if ($before.Count -ne $after.Count) { throw 'Generated files were out of date; review the regenerated files' }
foreach ($name in $before.Keys) {
    if ($before[$name] -ne $after[$name]) { throw "Generated file was out of date: $name" }
}
Write-Host 'Generated files are up to date.'
