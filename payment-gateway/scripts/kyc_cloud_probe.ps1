param(
  [string]$BaseUrl = $env:KYC_ENGINE_PROVIDER_URL,
  [string]$ApiKey = $env:KYC_ENGINE_PROVIDER_API_KEY
)

$ErrorActionPreference = "Stop"

if (-not $BaseUrl) {
  Write-Error "Informe -BaseUrl ou defina KYC_ENGINE_PROVIDER_URL."
  exit 1
}

$root = $BaseUrl.TrimEnd("/")
$healthUrl = $root
if ($healthUrl.EndsWith("/analyze")) {
  $healthUrl = $healthUrl.Substring(0, $healthUrl.Length - "/analyze".Length)
}
$analyzeUrl = if ($root.EndsWith("/analyze")) { $root } else { "$root/analyze" }

$headers = @{ "Content-Type" = "application/json" }
if ($ApiKey) {
  $headers["Authorization"] = "Bearer $ApiKey"
}

Write-Host "Health:  $healthUrl/health"
$health = Invoke-RestMethod -Method Get -Uri "$healthUrl/health" -TimeoutSec 30
$health | ConvertTo-Json -Depth 8

$payload = @{
  RequestID = "cloud-probe-" + [guid]::NewGuid().ToString()
  UserID = "00000000-0000-0000-0000-000000000000"
  Level = 1
  DocumentURL = ""
  DocumentBackURL = ""
  SelfieURL = ""
  FacialVideoURL = ""
  DeviceFingerprint = "cloud-probe"
  IPAddress = "127.0.0.1"
  UserAgent = "chainfx-kyc-cloud-probe"
}

Write-Host "Analyze: $analyzeUrl"
$started = Get-Date
$response = Invoke-RestMethod -Method Post -Uri $analyzeUrl -Headers $headers -Body ($payload | ConvertTo-Json -Depth 8) -TimeoutSec 60
$latency = [int]((Get-Date) - $started).TotalMilliseconds

$report = [pscustomobject]@{
  ok = $true
  base_url = $root
  analyze_url = $analyzeUrl
  http_latency_ms = $latency
  provider = $response.provider
  model_version = $response.model_version
  decision = $response.decision
  score = $response.score
  flags = $response.flags
  has_embedding = ($response.embedding -ne $null -and $response.embedding.Count -gt 0)
  has_embedding_hash = [bool]$response.embedding_hash
  raw = $response
}

$out = "kyc-cloud-probe-{0}.json" -f (Get-Date -Format "yyyyMMdd-HHmmss")
$report | ConvertTo-Json -Depth 12 | Set-Content -Encoding UTF8 $out
$report | ConvertTo-Json -Depth 12
Write-Host "Relatorio salvo em $out"
