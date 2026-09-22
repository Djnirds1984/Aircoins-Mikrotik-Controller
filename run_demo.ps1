$ErrorActionPreference = 'Stop'
$root = 'C:\Users\CITYCONNECT\Documents\GitHub\Aircoins-Mikrotik-Controller'
$out  = Join-Path $env:TEMP 'aircoins-demo.exe'
$db   = Join-Path $env:TEMP 'aircoins-demo.db'
$key  = Join-Path $env:TEMP 'aircoins-demo.key'
$log  = Join-Path $env:TEMP 'aircoins-demo.log'
$res  = Join-Path $env:TEMP 'aircoins-demo-result.txt'
Remove-Item $out,$db,$key,$log,$res -Force -ErrorAction SilentlyContinue
Push-Location $root
& go build -o $out .
if (-not (Test-Path $out)) { 'BUILD_FAIL' | Out-File $res; exit }
'BUILD_OK' | Out-File $res
$env:ADDR='127.0.0.1:18102'; $env:DB_PATH=$db; $env:API_TIMEOUT='4s'; $env:SECRET_KEY_PATH=$key
$p = Start-Process -FilePath $out -RedirectStandardOutput $log -WindowStyle Hidden
Start-Sleep -Seconds 2
function Hit([string]$m,[string]$u,[string]$b=''){ try { $h=@{}; if($b){$h['Content-Type']='application/json'}; $r = if($b){Invoke-WebRequest -Uri $u -Method $m -Headers $h -Body $b -UseBasicParsing -TimeoutSec 10}else{Invoke-WebRequest -Uri $u -Method $m -UseBasicParsing -TimeoutSec 10}; return "OK|$($r.StatusCode)|$($u)" } catch { return "ERR|$($_.Exception.Message)|$u" } }
"health=$(Hit GET 'http://127.0.0.1:18102/healthz')" | Out-File $res -Append
"create=$(Hit POST 'http://127.0.0.1:18102/api/v1/routers' '{\"name\":\"demo\",\"host\":\"192.168.88.1\",\"port\":8728,\"username\":\"admin\",\"password\":\"x\",\"portal_tag\":\"hotspot1\"}')" | Out-File $res -Append
"pid=$($p.Id)" | Out-File $res -Append
