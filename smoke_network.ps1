$ErrorActionPreference = 'Continue'
$root = 'C:\Users\CITYCONNECT\Documents\GitHub\Aircoins-Mikrotik-Controller'
$work = Join-Path $env:TEMP ('aircoins-net-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work -Force | Out-Null
$exe = Join-Path $work 'app.exe'
$db  = Join-Path $work 'app.db'

Set-Location $root
go build -o $exe . 2>&1 | Select-Object -First 20
if (-not (Test-Path $exe)) { Write-Output 'BUILD FAILED'; exit 1 }

$env:ADDR = '127.0.0.1:18095'
$env:DB_PATH = $db
$env:AIRCOINS_SECRET_KEY = '3b7f1c9d4e2a6085fb17d3c94a6e80215dc3f9074b1a6e2d85c09f3a4e6b1d72'
$env:PORTAL_NAME = 'Smoke'
$env:API_TIMEOUT = '2s'
$proc = Start-Process -FilePath $exe -WorkingDirectory $work -PassThru -RedirectStandardOutput (Join-Path $work 'out.log') -RedirectStandardError (Join-Path $work 'err.log')
Start-Sleep -Seconds 2

$base = 'http://127.0.0.1:18095'
try {
  # create a router through the CSRF exempt REST API
  $body = '{"name":"demo","host":"192.168.88.1","port":8728,"username":"admin","password":"x"}'
  $r = Invoke-WebRequest -Uri "$base/api/v1/routers" -Method Post -Body $body -ContentType 'application/json' -UseBasicParsing
  Write-Output ("api-create: {0}" -f $r.StatusCode)
} catch { Write-Output ("api-create: FAILED {0}" -f $_.Exception.Message) }

$paths = @(
  '/network',
  '/network/1?tab=servers',
  '/network/1?tab=server-profiles',
  '/network/1?tab=user-profiles',
  '/network/1?tab=walled-garden',
  '/network/1?tab=walled-garden-ip',
  '/network/1?tab=servers&edit=.id1',
  '/network/1?tab=server-profiles&edit=.id1',
  '/network/1?tab=user-profiles&edit=.id1',
  '/network/1?tab=walled-garden&edit=.id1',
  '/network/1?tab=walled-garden-ip&edit=.id1',
  '/network?router=1'
)
foreach ($p in $paths) {
  try {
    $r = Invoke-WebRequest -Uri ($base + $p) -MaximumRedirection 3 -UseBasicParsing
    $len = $r.Content.Length
    $bad = ''
    if ($r.Content -match 'internal server error') { $bad = ' BODY-500' }
    Write-Output ("{0} -> {1} len={2}{3}" -f $p, $r.StatusCode, $len, $bad)
  } catch {
    Write-Output ("{0} -> FAILED {1}" -f $p, $_.Exception.Message)
  }
}

Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

Write-Output '--- server log (errors/template) ---'
$log = (Get-Content (Join-Path $work 'out.log') -ErrorAction SilentlyContinue) + (Get-Content (Join-Path $work 'err.log') -ErrorAction SilentlyContinue)
$log | Select-String -Pattern 'template|error|panic|level=ERROR|500' | Select-Object -First 30
Write-Output '--- log line count ---'
$log.Count
Write-Output ("WORKDIR: {0}" -f $work)
