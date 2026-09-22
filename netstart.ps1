$ErrorActionPreference = 'Stop'
$root = 'C:\Users\CITYCONNECT\Documents\GitHub\Aircoins-Mikrotik-Controller'
Get-Process -Name 'aircoins*' -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 1
$out = Join-Path $env:TEMP 'aircoins-demo.exe'
$db  = Join-Path $env:TEMP 'aircoins-demo.db'
$key = Join-Path $env:TEMP 'aircoins-demo.key'
$res = Join-Path $env:TEMP 'aircoins-demo-result.txt'
Remove-Item $out,$db,$key,$res -Force -ErrorAction SilentlyContinue
Push-Location $root
& go build -o $out .
if (-not (Test-Path $out)) { 'BUILD_FAIL' | Out-File $res -Encoding ascii; exit 1 }
'BUILD_OK' | Out-File $res -Encoding ascii
$env:ADDR='127.0.0.1:19200'
$env:DB_PATH=$db
$env:API_TIMEOUT='4s'
$env:SECRET_KEY_PATH=$key
$proc = Start-Process -FilePath $out -RedirectStandardOutput (Join-Path $env:TEMP 'aircoins-demo.log') -WindowStyle Hidden -Pass
Start-Sleep -Seconds 2
function Call($m,$u,$b='') {
  try {
    $h = @{}
    if ($b) { $h['Content-Type']='application/json' }
    if ($b) { $r = Invoke-WebRequest -Uri $u -Method $m -Headers $h -Body $b -UseBasicParsing -TimeoutSec 10 } else { $r = Invoke-WebRequest -Uri $u -Method $m -UseBasicParsing -TimeoutSec 10 }
    return $r.StatusCode
  } catch { return "ERR:$($_.Exception.Message)" }
}
"health=$(Call GET 'http://127.0.0.1:19200/healthz')" | Out-File $res -Append -Encoding ascii
"picker=$(Call GET 'http://127.0.0.1:19200/network')" | Out-File $res -Append -Encoding ascii
"create=$(Call POST 'http://127.0.0.1:19200/api/v1/routers' '{\"name\":\"demo\",\"host\":\"192.168.88.1\",\"port\":8728,\"username\":\"admin\",\"password\":\"x\",\"portal_tag\":\"hotspot1\"}')" | Out-File $res -Append -Encoding ascii
$tags = @('servers','server-profiles','user-profiles','walled-garden','walled-garden-ip')
foreach ($t in $tags) {
  $u = "http://127.0.0.1:19200/network/1?tab=$t"
  $s = "$t=$(Call GET $u)"
  $s | Out-File $res -Append -Encoding ascii
}
"portal=$(Call GET 'http://127.0.0.1:19200/portal/login?mac=AA:BB:CC:DD:EE:FF&ip=192.168.88.10&link-login=https://x/login&link-orig=https://example.com')" | Out-File $res -Append -Encoding ascii
"pid=$($proc.Id)" | Out-File $res -Append -Encoding ascii
