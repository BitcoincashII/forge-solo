<#
What the dashboard shows of an install's data, as one sorted JSON file, byte for byte as
scripts/dashboard-snapshot.sh writes it on Umbrel and Linux: the payout settings, the TIDES Gateway
ID, each miner's settings, blocks, payouts and 1175 blocks, and the health answer with the state of
the move from PostgreSQL. What changes from one minute to the next and what differs by platform is
left out, so the same data gives the same file on every platform, before an update and after it.

  scripts\windows\dashboard-snapshot.ps1 -Base http://127.0.0.1:3080 -Out after.json
  scripts\windows\dashboard-snapshot.ps1 -Base http://127.0.0.1:3080 -Out before.json -Save answers
  scripts\windows\dashboard-snapshot.ps1 -From answers -Out out.json

The miners are those of testdata\migrate\seed-1012.sql unless $env:MINERS names others, as
"label=address" pairs separated by spaces. Windows PowerShell 5.1 and PowerShell 7 both run it.
#>
param(
    [string]$Base,
    [Parameter(Mandatory = $true)][string]$Out,
    [string]$Save,
    [string]$From
)
$ErrorActionPreference = 'Stop'
if (($Base -eq '') -eq ($From -eq '')) { throw 'give -Base URL or -From DIR' }

$miners = $env:MINERS
if (-not $miners) {
    $miners = 'A=bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2 ' +
        'B=bitcoincashii:qzet9v4jk2et9v4jk2et9v4jk2et9v4jkg0xty3z5s ' +
        'C=bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4'
}

# Each answer: its name in the file, what kind of answer it is, and its API path.
$endpoints = New-Object System.Collections.ArrayList
foreach ($e in @(@('health', 'health', '/api/v1/health'), @('pool-config', 'pool-config', '/api/v1/pool/config'),
        @('stats', 'stats', '/api/v1/stats'), @('mining-status', 'mining-status', '/api/v1/mining-status'))) {
    [void]$endpoints.Add($e)
}
foreach ($pair in ($miners -split '\s+' | Where-Object { $_ })) {
    $label, $addr = $pair -split '=', 2
    foreach ($k in @(@('miner', ''), @('settings', '/settings'), @('solo-blocks', '/solo-blocks'),
            @('solo-payouts', '/solo-payouts'), @('payouts', '/payouts'), @('blocks', '/blocks'))) {
        [void]$endpoints.Add(@("$label-$($k[0])", $k[0], "/api/v1/miners/$addr$($k[1])"))
    }
}

# What is left out, by kind of answer: flattened keys, one pattern per reason (as in the .sh).
$volatile = @{
    'health'        = @('settings_loaded')
    'pool-config'   = @('platform|password_length|secrets_path', 'merge_mining_available')
    'stats'         = @('hashrate|hashrateRaw|workers|miners|luck', 'currentHeight|networkDifficulty|networkHashrate|bestBlockHash',
        'uptime', 'rentals\..*')
    'mining-status' = @('(?!payout_mode$|tides\.gateway$).*')
    'miner'         = @('hashrate5m|hashrate60m|workers|onlineWorkers|validShares|roundShares|invalidShares',
        'bestDiff|athDiff|totalWork|lastShare', 'currentHeight|balance|matureBalance|immatureBalance|balanceKnown')
    'solo-blocks'   = @('blocks\.\d+\.(confirmations|matures_in|mature)')
}
$fraction = '^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)\.\d+(Z|[+-]\d\d:\d\d)$'
$inv = [Globalization.CultureInfo]::InvariantCulture
$emptyObject = New-Object object
$emptyList = New-Object object

function Add-Flat($prefix, $v, $out) {
    if ($v -is [System.Management.Automation.PSCustomObject]) {
        $props = @($v.PSObject.Properties)
        if ($props.Count -eq 0) { $out[$prefix] = $emptyObject }
        foreach ($p in $props) {
            $k = if ($prefix) { "$prefix.$($p.Name)" } else { $p.Name }
            Add-Flat $k $p.Value $out
        }
    } elseif ($v -is [System.Array]) {
        if ($v.Count -eq 0) { $out[$prefix] = $emptyList }
        for ($i = 0; $i -lt $v.Count; $i++) { Add-Flat "$prefix.$i" $v[$i] $out }
    } else {
        $out[$prefix] = $v
    }
}

# Text as JSON, ASCII only, as Python's json.dumps(ensure_ascii=True) writes it.
function Format-Text([string]$s) {
    $b = New-Object System.Text.StringBuilder
    [void]$b.Append('"')
    foreach ($c in $s.ToCharArray()) {
        $n = [int]$c
        switch ($n) {
            0x22 { [void]$b.Append('\"'); continue }
            0x5c { [void]$b.Append('\\'); continue }
            0x08 { [void]$b.Append('\b'); continue }
            0x0c { [void]$b.Append('\f'); continue }
            0x0a { [void]$b.Append('\n'); continue }
            0x0d { [void]$b.Append('\r'); continue }
            0x09 { [void]$b.Append('\t'); continue }
            default {
                if ($n -lt 0x20 -or $n -gt 0x7f) { [void]$b.Append('\u' + $n.ToString('x4')) }
                else { [void]$b.Append($c) }
            }
        }
    }
    [void]$b.Append('"')
    return $b.ToString()
}

# A value as the file writes it: numbers as integers when they are whole, else to 8 decimals without
# trailing zeros; text as JSON, ASCII only, times to the second.
function Format-Value($v) {
    if ($null -eq $v) { return 'null' }
    if ([object]::ReferenceEquals($v, $emptyObject)) { return '{}' }
    if ([object]::ReferenceEquals($v, $emptyList)) { return '[]' }
    if ($v -is [bool]) { if ($v) { return 'true' } else { return 'false' } }
    if ($v -is [datetime]) { return Format-Text ($v.ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss', $inv) + 'Z') }
    if ($v -is [int] -or $v -is [long] -or $v -is [decimal] -or $v -is [double] -or $v -is [single]) {
        $d = [decimal]$v
        if ($d -eq [decimal]::Truncate($d) -and [math]::Abs($d) -lt 1e15) { return ([long]$d).ToString($inv) }
        return $d.ToString('F8', $inv).TrimEnd('0').TrimEnd('.')
    }
    return Format-Text ([regex]::Replace([string]$v, $fraction, '$1$2'))
}

function Get-Answer($path) {
    $uri = $Base.TrimEnd('/') + $path
    if ($PSVersionTable.PSVersion.Major -ge 7) {
        $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 30 -Uri $uri -SkipHttpErrorCheck
        return @([int]$r.StatusCode, $r.RawContentStream.ToArray())
    }
    try {
        $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 30 -Uri $uri
        return @([int]$r.StatusCode, $r.RawContentStream.ToArray())
    } catch [System.Net.WebException] {
        $resp = $_.Exception.Response
        if ($null -eq $resp) { throw }
        $ms = New-Object System.IO.MemoryStream
        $resp.GetResponseStream().CopyTo($ms)
        return @([int]$resp.StatusCode, $ms.ToArray())
    }
}

if ($Save) { New-Item -ItemType Directory -Force -Path $Save | Out-Null }
$utf8 = New-Object System.Text.UTF8Encoding $false
$flat = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
foreach ($e in $endpoints) {
    $name, $kind, $path = $e
    if ($From) {
        # A kept answer is the status line "HTTP <code>" and the body.
        $raw = [IO.File]::ReadAllBytes((Join-Path $From "$name.json"))
        $nl = [Array]::IndexOf($raw, [byte]10)
        $status = [int](([Text.Encoding]::ASCII.GetString($raw, 0, $nl)) -split ' ')[1]
        $body = $utf8.GetString($raw, $nl + 1, $raw.Length - $nl - 1)
    } else {
        $status, $bytes = Get-Answer $path
        $body = $utf8.GetString($bytes)
        if ($Save) {
            $head = [Text.Encoding]::ASCII.GetBytes("HTTP $status`n")
            [IO.File]::WriteAllBytes((Join-Path $Save "$name.json"), [byte[]]($head + $bytes))
        }
    }
    $flat["$name.http"] = $status
    try { $doc = $body | ConvertFrom-Json } catch { $flat["$name.body"] = 'not JSON'; continue }
    $one = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
    Add-Flat '' $doc $one
    $patterns = $volatile[$kind]
    foreach ($k in $one.Keys) {
        $left = $false
        foreach ($p in $patterns) { if ($k -cmatch "^(?:$p)$") { $left = $true; break } }
        if (-not $left) { $flat["$name.$k"] = $one[$k] }
    }
}
$keys = [string[]]@($flat.Keys)
[Array]::Sort($keys, [StringComparer]::Ordinal)
$lines = foreach ($k in $keys) { (Format-Text $k) + ': ' + (Format-Value $flat[$k]) }
[IO.File]::WriteAllText($Out, "{`n" + ($lines -join ",`n") + "`n}`n", [Text.Encoding]::ASCII)
