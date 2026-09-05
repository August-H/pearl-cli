param([Parameter(Mandatory=$true)][string]$Binary)
$ErrorActionPreference = 'Stop'
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$smokeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('pearl-smoke-' + [guid]::NewGuid().ToString('N').Substring(0,8))
$previousConfig = $env:PEARL_CONFIG_DIR
$previousSocket = $env:PEARL_SOCKET
$previousColor = $env:NO_COLOR
New-Item -ItemType Directory -Path $smokeRoot | Out-Null
$env:PEARL_CONFIG_DIR = $smokeRoot
$env:PEARL_SOCKET = Join-Path $smokeRoot 'p.sock'
$env:NO_COLOR = '1'
function Invoke-Pearl {
    param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Arguments)
    $output = & $Binary @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Pearl failed ($LASTEXITCODE): $Arguments" }
    return $output
}
try {
    'OPENROUTER_API_KEY=dummy-smoke-key' | Set-Content -LiteralPath (Join-Path $smokeRoot '.env')
    Invoke-Pearl version
    Invoke-Pearl daemon --help
    Invoke-Pearl daemon start
    Invoke-Pearl job --workspace $smokeRoot -n 'smoke job' 'Synthetic pending job; never execute'
    $jobs = Invoke-Pearl --workspace $smokeRoot jobs --json | ConvertFrom-Json
    if (@($jobs).Count -ne 1 -or $jobs[0].status -ne 'pending') { throw 'Pending job missing from JSON output' }
    $details = Invoke-Pearl jobs view 'smoke job' --json | ConvertFrom-Json
    if ($details.job.id -ne 'smoke job') { throw 'Job details mismatch' }
    Invoke-Pearl cancel 'smoke job'
    $details = Invoke-Pearl jobs view 'smoke job' --json | ConvertFrom-Json
    if ($details.job.status -ne 'cancelled') { throw 'Cancellation failed' }
    Write-Output 'CLI smoke test passed'
} finally {
    & $Binary daemon stop | Out-Null
    $env:PEARL_CONFIG_DIR = $previousConfig
    $env:PEARL_SOCKET = $previousSocket
    $env:NO_COLOR = $previousColor
    # Keep the isolated logs/database for diagnosis; no user data is touched.
}
