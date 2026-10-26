param(
    [string]$Sport = "nba",
    [string]$PMEvent = "1215089",
    [string]$FDEvent = "36229335",
    [string]$Out = "out/sync",
    [int]$MaxSkewSeconds = 120
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$outPath = Join-Path $root $Out
New-Item -ItemType Directory -Force $outPath | Out-Null
$startedAt = [DateTime]::UtcNow.ToString("o")

$jobs = @()
$jobs += Start-Job -ArgumentList $root, $outPath, $Sport -ScriptBlock {
    param($root, $out, $sport)
    Set-Location $root
    & .\go\oddsfeed.exe dk-discover -sport $sport -out (Join-Path $out "dk")
}
$jobs += Start-Job -ArgumentList $root, $outPath, $FDEvent -ScriptBlock {
    param($root, $out, $eventId)
    Set-Location $root
    & .\go\oddsfeed.exe fd-event -event $eventId -mode both -prices -out (Join-Path $out "fd")
}
$jobs += Start-Job -ArgumentList $root, $outPath, $PMEvent -ScriptBlock {
    param($root, $out, $eventId)
    Set-Location $root
    & .\go\oddsfeed.exe pm-discover -event $eventId -out (Join-Path $out "pm.json")
}
$jobs | Wait-Job | Out-Null
$jobs | Receive-Job
$jobs | Remove-Job
$endedAt = [DateTime]::UtcNow.ToString("o")

$meta = [ordered]@{
    sport = $Sport
    pmEvent = $PMEvent
    fdEvent = $FDEvent
    startedAt = $startedAt
    endedAt = $endedAt
    files = @{
        dk = (Join-Path $outPath "dk/board.json")
        fd = (Join-Path $outPath "fd/board.json")
        pm = (Join-Path $outPath "pm.json")
    }
}
$meta | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $outPath "meta.json")

& (Join-Path $root "normalizer\pmctl.exe") `
    -pm (Join-Path $outPath "pm.json") `
    -sport $Sport `
    -dk (Join-Path $outPath "dk/board.json") `
    -fd (Join-Path $outPath "fd/board.json") `
    -max-skew "$($MaxSkewSeconds)s" `
    -out (Join-Path $outPath "report")
