#Requires -Version 5.1
# Keep this script ASCII so Windows PowerShell 5.1 can read it without a BOM.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# Check native exit codes explicitly, including when called from PowerShell 7.
$PSNativeCommandUseErrorActionPreference = $false

if ($args.Count -gt 2) {
    [Console]::Error.WriteLine('Usage: start.ps1 [manual|qr [--rebind]]')
    exit 2
}

$mode = if ($args.Count -ge 1) { $args[0] } else { '' }
$rebind = if ($args.Count -ge 2) { $args[1] } else { '' }
if (-not $mode) {
    Write-Host 'QQ connection: 1) Scan QR code  2) Manual AppID/AppSecret'
    switch (Read-Host 'Choose [1/2]') {
        '1' { $mode = 'qr' }
        '2' { $mode = 'manual' }
        default { exit 2 }
    }
}
if ("${mode}:$rebind" -cnotin @('manual:', 'qr:', 'qr:--rebind')) {
    [Console]::Error.WriteLine('Usage: start.ps1 [manual|qr [--rebind]]')
    exit 2
}

function Invoke-Compose {
    # Run directly to preserve the interactive QR terminal and Docker output.
    & $dockerCommand.Source compose @composeFiles @args
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
}

$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $projectRoot
try {
    if (-not (Test-Path -LiteralPath (Join-Path $projectRoot '.env') -PathType Leaf)) {
        throw 'Run Copy-Item .env.example .env, then set the model settings and INTERNAL_API_TOKEN in .env.'
    }
    $dockerCommand = Get-Command docker -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $dockerCommand) {
        throw 'Docker was not found. Install and start Docker Desktop with Linux containers, then reopen your terminal.'
    }

    $composeFiles = @('-f', 'compose.yaml')
    if ($mode -eq 'qr') {
        $composeFiles += @('-f', 'compose.qr.yaml')
    }
    Invoke-Compose config --quiet
    if ($mode -eq 'qr') {
        # Stop both consumers before writing a new shared bot identity.
        Invoke-Compose stop gateway worker
        if ($rebind -eq '--rebind') {
            Invoke-Compose run --rm --no-deps --build -e QQ_LOGIN_FORCE=true qq-login
        } else {
            Invoke-Compose run --rm --no-deps --build qq-login
        }
    }
    # Fail before bringing up a partially configured stack.
    Invoke-Compose run --rm --no-deps --build -e ROLE=gateway gateway check
    Invoke-Compose run --rm --no-deps -e ROLE=worker worker check
    Invoke-Compose up -d --build redis gateway worker
    Invoke-Compose ps
} catch {
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
} finally {
    Pop-Location
}
