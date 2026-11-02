# System test on a disposable Windows host (the GitHub Actions Windows runner, elevated).
# Runs the real API natively (in-memory store), installs the agent with the
# one-line PowerShell installer as a Windows service, checks it reports, that its
# credential directory is locked down, that revocation stops it, and uninstalls it.
# NEVER run this on a machine you care about.
$ErrorActionPreference = 'Stop'
if ($env:CI -ne 'true') { throw 'refusing to run outside CI' }

$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$work = Join-Path $env:RUNNER_TEMP 'osriva-system'
$dist = Join-Path $work 'dist'
New-Item -ItemType Directory -Force -Path $dist | Out-Null
function Step($m) { Write-Host ''; Write-Host "######## $m" }
function Wait-Until([int]$seconds, [string]$what, [scriptblock]$cond) {
    for ($i = 0; $i -lt $seconds; $i++) {
        try { if (& $cond) { Write-Host "ok: $what"; return } } catch {}
        Start-Sleep -Seconds 1
    }
    throw "timed out waiting for: $what"
}

Step 'build API and agent'
Push-Location $root
$env:CGO_ENABLED = '0'
go build -o (Join-Path $work 'osriva-api.exe') ./services/api/cmd/osriva-api
if ($LASTEXITCODE) { throw 'api build failed' }
go build -o (Join-Path $dist 'osriva-agent-windows-amd64.exe') ./agent/cmd/osriva-agent
if ($LASTEXITCODE) { throw 'agent build failed' }
Pop-Location

Step 'start the API (development memory store, agent endpoint on loopback)'
$password = [Convert]::ToBase64String((1..24 | ForEach-Object { Get-Random -Maximum 256 }))
Write-Host "::add-mask::$password"
$keyBytes = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($keyBytes)
$env:OSRIVA_ENV = 'development'
$env:OSRIVA_DEV_MEMORY_STORE = 'true'
$env:OSRIVA_SECRET_KEY = [Convert]::ToBase64String($keyBytes)
$env:OSRIVA_DEV_ADMIN_EMAIL = 'ci-owner@example.com'
$env:OSRIVA_DEV_ADMIN_PASSWORD = $password
$env:OSRIVA_PUBLIC_URL = 'http://127.0.0.1:8080'
$env:OSRIVA_AGENT_ADDR = '127.0.0.1:8443'
$env:OSRIVA_AGENT_PUBLIC_URL = 'https://127.0.0.1:8443'
$env:OSRIVA_AGENT_DIST_DIR = $dist
$env:OSRIVA_LOG_FORMAT = 'text'
$api = Start-Process -FilePath (Join-Path $work 'osriva-api.exe') -ArgumentList 'serve' -PassThru -NoNewWindow `
    -RedirectStandardError (Join-Path $work 'api.log')
Wait-Until 60 'API ready' { (Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8080/readyz).StatusCode -eq 200 }

$panel = 'http://127.0.0.1:8080/api/v1'
$script:session = $null
$script:csrf = ''
function Login {
    $body = @{ email = 'ci-owner@example.com'; password = $password } | ConvertTo-Json
    $r = Invoke-RestMethod -Method Post -Uri "$panel/auth/login" -ContentType 'application/json' -Body $body -SessionVariable s
    $script:session = $s
    $script:csrf = $r.csrf_token
}
function Api([string]$method, [string]$path, $body = $null) {
    $p = @{ Method = $method; Uri = "$panel$path"; WebSession = $script:session; Headers = @{ 'X-CSRF-Token' = $script:csrf } }
    if ($null -ne $body) { $p.ContentType = 'application/json'; $p.Body = ($body | ConvertTo-Json) }
    Invoke-RestMethod @p
}

try {
    Step 'add a server and run the one-line PowerShell installer'
    Login
    $created = Api Post '/servers' @{ name = 'ci-windows' }
    $id = $created.server.id
    $cmd = $created.enrollment.install_windows
    if (-not $cmd.StartsWith('& ([scriptblock]::Create(')) { throw "no windows install command: $cmd" }
    Invoke-Expression $cmd

    Step 'service runs and reports'
    $svc = Get-Service -Name 'osriva-agent'
    if ($svc.Status -ne 'Running') { throw "service status: $($svc.Status)" }
    if ($svc.StartType -ne 'Automatic') { throw "start type: $($svc.StartType)" }
    Wait-Until 90 'server online' { (Api Get "/servers/$id").server.connection -eq 'online' }
    $d = Api Get "/servers/$id"
    if ($d.inventory.os.family -ne 'windows') { throw 'inventory not from windows' }
    if (($d.capabilities | Where-Object key -eq 'agent.inventory').status -ne 'SUPPORTED') { throw 'windows/amd64 should be SUPPORTED' }
    if (-not $d.inventory.privileged) { throw 'service should run elevated (LocalSystem)' }

    Step 'credential directory is restricted to SYSTEM and Administrators'
    $acl = Get-Acl 'C:\ProgramData\OSRIVA\agent'
    if (-not $acl.AreAccessRulesProtected) { throw 'DACL inherits from parent' }
    $sids = $acl.Access | ForEach-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value } | Sort-Object -Unique
    if (($sids -join ',') -ne 'S-1-5-18,S-1-5-32-544') { throw "unexpected ACL principals: $($sids -join ',')" }

    Step 'revoking stops the service with exit code 3'
    Login
    Api Post "/servers/$id/revoke" | Out-Null
    Wait-Until 150 'service stopped' { (Get-Service -Name 'osriva-agent').Status -eq 'Stopped' }
    $exit = (Get-CimInstance Win32_Service -Filter "Name='osriva-agent'").ExitCode
    $specific = (sc.exe query osriva-agent | Select-String 'SERVICE_EXIT_CODE').ToString()
    Write-Host "Win32 exit code: $exit; $specific"

    Step 'uninstall'
    & 'C:\Program Files\OSRIVA\osriva-agent.exe' service uninstall
    if ($LASTEXITCODE) { throw 'service uninstall failed' }
    if (Get-Service -Name 'osriva-agent' -ErrorAction SilentlyContinue) { throw 'service still present' }
    Write-Host ''; Write-Host 'WINDOWS SYSTEM TEST PASSED'
}
finally {
    Stop-Process -Id $api.Id -Force -ErrorAction SilentlyContinue
    Write-Host '---- api log (tail) ----'
    Get-Content (Join-Path $work 'api.log') -Tail 40 -ErrorAction SilentlyContinue
    Write-Host '---- agent log (tail) ----'
    Get-Content 'C:\ProgramData\OSRIVA\agent\agent.log' -Tail 40 -ErrorAction SilentlyContinue
}
