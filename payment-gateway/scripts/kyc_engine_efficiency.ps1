param(
  [string]$BaseUrl = "http://localhost:8080",
  [string]$Token = $env:MOBILE_ACCESS_TOKEN,
  [int]$Runs = 10
)

if (-not $Token) {
  Write-Error "Informe -Token ou MOBILE_ACCESS_TOKEN com JWT mobile."
  exit 1
}

$headers = @{ Authorization = "Bearer $Token" }
$samples = @()

for ($i = 0; $i -lt $Runs; $i++) {
  $started = Get-Date
  try {
    $resp = Invoke-RestMethod -Method Get -Uri "$BaseUrl/api/mobile/kyc/engine/metrics" -Headers $headers -TimeoutSec 30
    $elapsed = [int]((Get-Date) - $started).TotalMilliseconds
    $samples += [pscustomobject]@{
      run = $i + 1
      http_latency_ms = $elapsed
      engine_avg_latency_ms = $resp.avg_latency_ms
      engine_p95_latency_ms = $resp.p95_latency_ms
      engine_max_latency_ms = $resp.max_latency_ms
      count = $resp.count
    }
  } catch {
    $samples += [pscustomobject]@{
      run = $i + 1
      error = $_.Exception.Message
    }
  }
}

$okSamples = $samples | Where-Object { $_.http_latency_ms -ne $null }
$avg = 0
$max = 0
if ($okSamples.Count -gt 0) {
  $avg = [int](($okSamples | Measure-Object -Property http_latency_ms -Average).Average)
  $max = [int](($okSamples | Measure-Object -Property http_latency_ms -Maximum).Maximum)
}

$report = [pscustomobject]@{
  base_url = $BaseUrl
  runs = $Runs
  success = $okSamples.Count
  avg_http_latency_ms = $avg
  max_http_latency_ms = $max
  samples = $samples
}

$out = "kyc-engine-efficiency-{0}.json" -f (Get-Date -Format "yyyyMMdd-HHmmss")
$report | ConvertTo-Json -Depth 8 | Set-Content -Encoding UTF8 $out
$report | ConvertTo-Json -Depth 8
Write-Host "Relatorio salvo em $out"
