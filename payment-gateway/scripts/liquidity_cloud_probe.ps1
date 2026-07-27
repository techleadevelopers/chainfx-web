param(
  [string]$BaseUrl = "https://api-production-bc748.up.railway.app",
  [int]$TimeoutSec = 20,
  [switch]$FailOnSlow,
  [int]$SlowMs = 2500,
  [switch]$RequirePersistedQuote
)

$ErrorActionPreference = "Stop"

function Invoke-Probe {
  param(
    [string]$Name,
    [string]$Method,
    [string]$Path,
    [object]$Body = $null,
    [int[]]$Accept = @(200)
  )

  $url = $BaseUrl.TrimEnd("/") + $Path
  $sw = [Diagnostics.Stopwatch]::StartNew()
  try {
    $params = @{
      Uri        = $url
      Method     = $Method
      TimeoutSec = $TimeoutSec
      Headers    = @{ Accept = "application/json" }
    }
    if ($null -ne $Body) {
      $params.ContentType = "application/json"
      $params.Body = ($Body | ConvertTo-Json -Compress)
    }

    $resp = Invoke-WebRequest @params
    $sw.Stop()
    $status = [int]$resp.StatusCode
    $ok = $Accept -contains $status
    if ($FailOnSlow -and $sw.ElapsedMilliseconds -gt $SlowMs) {
      $ok = $false
    }
    return [pscustomobject]@{
      name   = $Name
      ok     = $ok
      status = $status
      ms     = $sw.ElapsedMilliseconds
      body   = $resp.Content
    }
  } catch {
    $sw.Stop()
    $status = $null
    $body = $_.Exception.Message
    if ($_.Exception.Response) {
      $status = [int]$_.Exception.Response.StatusCode
      try {
        $reader = New-Object IO.StreamReader($_.Exception.Response.GetResponseStream())
        $body = $reader.ReadToEnd()
      } catch {}
    }
    $ok = $false
    if ($null -ne $status -and ($Accept -contains $status)) {
      $ok = $true
    }
    return [pscustomobject]@{
      name   = $Name
      ok     = $ok
      status = $status
      ms     = $sw.ElapsedMilliseconds
      body   = $body
    }
  }
}

$probes = @(
  @{ Name = "healthz"; Method = "GET"; Path = "/healthz" },
  @{ Name = "readyz"; Method = "GET"; Path = "/readyz" },
  @{ Name = "buy_pairs"; Method = "GET"; Path = "/api/buy/pairs" },
  @{
    Name = "quote_usdt_bsc"
    Method = "POST"
    Path = "/api/quote"
    Body = @{ mode = "buy"; asset = "USDT"; network = "BSC"; fiatCurrency = "BRL"; paymentMethod = "pix"; amountBRL = 100 }
  },
  @{
    Name = "quote_sol_solana"
    Method = "POST"
    Path = "/api/quote"
    Body = @{ mode = "buy"; asset = "SOL"; network = "SOLANA"; fiatCurrency = "BRL"; paymentMethod = "pix"; amountBRL = 100 }
  },
  @{
    Name = "reject_unsupported_pair"
    Method = "POST"
    Path = "/api/quote"
    Body = @{ mode = "buy"; asset = "SOL"; network = "BSC"; fiatCurrency = "BRL"; paymentMethod = "pix"; amountBRL = 100 }
    Accept = @(400)
  }
)

$results = foreach ($p in $probes) {
  Invoke-Probe @p
}

$summary = [pscustomobject]@{
  baseUrl = $BaseUrl
  ok = -not ($results | Where-Object { -not $_.ok })
  generatedAt = (Get-Date).ToUniversalTime().ToString("o")
  checks = [ordered]@{}
  results = $results | ForEach-Object {
    $body = $_.body
    if ($null -ne $body -and $body.Length -gt 1200) {
      $body = $body.Substring(0, 1200) + "...<truncated>"
    }
    [pscustomobject]@{
      name = $_.name
      ok = $_.ok
      status = $_.status
      ms = $_.ms
      body = $body
    }
  }
}

$pairsProbe = $results | Where-Object { $_.name -eq "buy_pairs" } | Select-Object -First 1
if ($pairsProbe -and $pairsProbe.status -eq 200) {
  try {
    $pairsBody = $pairsProbe.body | ConvertFrom-Json
    $summary.checks.routerEnabled = [bool]$pairsBody.routerEnabled
    $summary.checks.pairCount = @($pairsBody.pairs).Count
    $summary.checks.hasSolana = @($pairsBody.networks) -contains "SOLANA"
    $summary.checks.hasBitcoin = @($pairsBody.networks) -contains "BITCOIN"
    $summary.checks.backendEnforced = [bool]$pairsBody.backendEnforced
  } catch {
    $summary.checks.buyPairsParseError = $_.Exception.Message
    $summary.ok = $false
  }
}

foreach ($quoteName in @("quote_usdt_bsc", "quote_sol_solana")) {
  $quoteProbe = $results | Where-Object { $_.name -eq $quoteName } | Select-Object -First 1
  if (-not $quoteProbe -or $quoteProbe.status -ne 200) {
    continue
  }
  try {
    $quote = $quoteProbe.body | ConvertFrom-Json
    $hasQuoteId = -not [string]::IsNullOrWhiteSpace([string]$quote.quoteId)
    $persisted = [bool]$quote.quotePersisted
    $hasFee = [double]$quote.feeFiat -gt 0 -and [double]$quote.totalFiat -gt [double]$quote.subtotalFiat
    $hasRate = [double]$quote.marketRate -gt 0 -and [double]$quote.rate -gt [double]$quote.marketRate
    $summary.checks[$quoteName + "_hasQuoteId"] = $hasQuoteId
    $summary.checks[$quoteName + "_quotePersisted"] = $persisted
    $summary.checks[$quoteName + "_hasChainFXFee"] = $hasFee
    $summary.checks[$quoteName + "_hasSpreadRate"] = $hasRate
    if ($RequirePersistedQuote -and (-not $hasQuoteId -or -not $persisted)) {
      $summary.ok = $false
    }
    if (-not $hasFee -or -not $hasRate) {
      $summary.ok = $false
    }
  } catch {
    $summary.checks[$quoteName + "_parseError"] = $_.Exception.Message
    $summary.ok = $false
  }
}

$summary | ConvertTo-Json -Depth 8

if (-not $summary.ok) {
  exit 1
}
