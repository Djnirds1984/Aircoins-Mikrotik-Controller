$ErrorActionPreference = 'Continue'
$root = 'C:\Users\CITYCONNECT\Documents\GitHub\Aircoins-Mikrotik-Controller'
Get-Process -Name 'aircoins*' -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 1
$out  = Join-Path $env:TEMP 'aircoins-demo.exe'
$db   = Join-Path $env:TEMP 'aircoins-demo.db'
$key  = Join-Path $env:TEMP 'aircoins-demo.key'
$log  = Join-Path $env:TEMP 'aircoins-demo.log'
$res  = Join-Path $env:TEMP 'aircoins-demo-result.txt'
Remove-Item $out,$db,$key,$log,$res -Force -ErrorAction SilentlyContinue
Push-Location $root
& go build -o $out .
'BUILD_OK' | Out-File $res -Encoding ascii
$env:ADDR='127.0.0.1:18103'; $env:DB_PATH=$db; $env:API_TIMEOUT='4s'; $env:SECRET_KEY_PATH=$key
$p = Start-Process -FilePath $out -RedirectStandardOutput $log -WindowStyle Hidden
Start-Sleep -Seconds 2
function Hit([string]$m,[string]$u,[string]$b=''){
    try {
        $h=@{}; if($b){$h['Content-Type']='application/json'}
        $r = if($b){Invoke-WebRequest -Uri $u -Method $m -Headers $h -Body $b -UseBasicParsing -TimeoutSec 10}else{Invoke-WebRequest -Uri $u -Method $m -UseBasicParsing -TimeoutSec 10}
        return "$($r.StatusCode)|$($r.Content)"
    } catch {
        $msg = $_.Exception.Message
        if ($_.Exception.Response -and $_.Exception.Response.GetResponse()) {
            try { $sr = $_.Exception.Response.GetResponse().GetResponseStream(); $br = New-Object System.IO.StreamReader($sr); $msg = $br.ReadToEnd() } catch {}
        }
        return "ERR|$msg"
    }
}
"health=$(Hit GET 'http://127.0.0.1:18103/healthz')" | Out-File $res -Append -Encoding ascii
"create=$(Hit POST 'http://127.0.0.1:18103/api/v1/routers' '{\"name\":\"demo\",\"host\":\"192.168.88.1\",\"port\":8728,\"username\":\"admin\",\"password\":\"x\",\"portal_tag\":\"hotspot1\"}')" | Out-File $res -Append -Encoding ascii
foreach ($t in @('servers','server-profiles','user-profiles','walled-garden','walled-garden-ip')) {
    "$t=$(Hit GET \"http://127.0.0.1:18103/network/1?tab=$t\")" | Out-File $res -Append -Encoding ascii
}
"pid=$($p.Id)" | Out-File $res -Append -Encoding ascii
