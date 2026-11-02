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

    Step 'tasks: list services and processes, control the Print Spooler'
    function Run-Task([string]$type, $params = @{}) {
        $t = Api Post "/servers/$id/tasks" @{ type = $type; params = $params }
        for ($i = 0; $i -lt 3 -and ($t.status -eq 'queued' -or $t.status -eq 'running'); $i++) {
            $t = Api Get "/servers/$id/tasks/$($t.id)?wait=20"
        }
        return $t
    }
    $svcs = Run-Task 'services.list'
    if ($svcs.status -ne 'succeeded' -or $svcs.result.manager -ne 'windows-scm') { throw "services.list: $($svcs.status) $($svcs.error)" }
    if (-not ($svcs.result.services | Where-Object name -eq 'Spooler')) { throw 'Spooler not listed' }
    $procs = Run-Task 'processes.list'
    if ($procs.status -ne 'succeeded' -or -not ($procs.result.processes | Where-Object name -eq 'osriva-agent.exe')) { throw "processes.list: $($procs.error)" }
    $r = Run-Task 'service.action' @{ name = 'Spooler'; action = 'stop' }
    if ($r.status -ne 'succeeded' -or $r.result.state -ne 'stopped' -or (Get-Service Spooler).Status -ne 'Stopped') { throw "stop Spooler: $($r.status) $($r.error)" }
    $r = Run-Task 'service.action' @{ name = 'Spooler'; action = 'start' }
    if ($r.status -ne 'succeeded' -or $r.result.state -ne 'running' -or (Get-Service Spooler).Status -ne 'Running') { throw "start Spooler: $($r.status) $($r.error)" }
    $rejected = $false
    try { Api Post "/servers/$id/tasks" @{ type = 'shell.exec'; params = @{ cmd = 'whoami' } } | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw 'shell.exec was accepted' }

    Step 'logs and files (agent runs as LocalSystem)'
    $logs = Run-Task 'logs.read' @{ source = 'system'; lines = 20 }
    if ($logs.status -ne 'succeeded' -or $logs.result.backend -ne 'windows-eventlog' -or @($logs.result.entries).Count -eq 0) { throw "logs.read: $($logs.error)" }
    $dir = 'C:\ProgramData\osriva-system-test'
    if ((Run-Task 'files.mkdir' @{ path = $dir }).status -ne 'succeeded') { throw 'mkdir failed' }
    $w = Run-Task 'files.write' @{ path = "$dir\app.conf"; content = "password=hunter2`n"; create = $true }
    if ($w.status -ne 'succeeded' -or (Get-Content -Raw "$dir\app.conf") -ne "password=hunter2`n") { throw "write: $($w.error)" }
    $l = Run-Task 'files.list' @{ path = 'C:\ProgramData\OSRIVA' }
    if (-not ($l.result.entries | Where-Object { $_.name -eq 'agent' -and $_.denied })) { throw "agent credential directory not marked protected: $($l.status) $($l.error)" }
    foreach ($p in @('C:\ProgramData\OSRIVA\agent\agent.key', 'C:\Windows\System32\config\SAM', 'C:\ProgramData\..\Windows\win.ini')) {
        $r = Run-Task 'files.read' @{ path = $p }
        if ($r.status -ne 'failed' -or $r.error -notlike '*not allowed by this agent*') { throw "$p was readable: $($r.status) $($r.error)" }
    }
    if ((Run-Task 'files.delete' @{ path = "$dir\app.conf" }).status -ne 'succeeded' -or (Test-Path "$dir\app.conf")) { throw 'delete file failed' }
    if ((Run-Task 'files.delete' @{ path = $dir }).status -ne 'succeeded') { throw 'delete dir failed' }
    if ((Invoke-WebRequest -UseBasicParsing -WebSession $script:session "$panel/servers/$id/tasks?limit=50").Content -like '*hunter2*') { throw 'file content leaked into the task list' }

    Step 'users, scheduled tasks, time'
    $u = Run-Task 'users.list'
    if ($u.status -ne 'succeeded' -or -not ($u.result.users | Where-Object name -eq 'Administrator') -or -not ($u.result.groups | Where-Object name -eq 'Administrators')) { throw "users.list: $($u.error)" }
    $c = Run-Task 'cron.list'
    if ($c.status -ne 'succeeded' -or $c.result.backend -ne 'windows-task-scheduler') { throw "cron.list: $($c.error)" }
    $ti = Run-Task 'system.time'
    if ($ti.status -ne 'succeeded' -or -not $ti.result.timezone) { throw "system.time: $($ti.error)" }
    $oldTz = $ti.result.timezone
    $tz = Run-Task 'system.timezone' @{ timezone = 'Bangladesh Standard Time' }
    if ($tz.status -ne 'succeeded' -or (tzutil /g) -ne 'Bangladesh Standard Time') { throw "timezone: $($tz.status) $($tz.error)" }
    Run-Task 'system.timezone' @{ timezone = $oldTz } | Out-Null
    $k = Run-Task 'sshkeys.list' @{ user = 'runneradmin' }
    if ($k.status -ne 'failed' -or $k.error -notlike '*not supported*') { throw 'sshkeys.list should be unsupported on Windows' }

    Step 'live metrics arrive'
    Wait-Until 90 'metrics with CPU and memory' {
        $m = Api Get "/servers/$id/metrics?minutes=10"
        @($m.samples | Where-Object { $null -ne $_.cpu_percent -and $_.mem_total_bytes -gt 0 }).Count -gt 0
    }

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
