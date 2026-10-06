<#
What the dashboard shows of an install's data, as one sorted JSON file, byte for byte as
scripts/dashboard-snapshot.sh writes it on Umbrel and Linux: the payout settings, the TIDES Gateway
ID, each miner's settings, blocks, payouts and 1175 blocks, and the health answer with the state of
the move from PostgreSQL. What changes from one minute to the next and what differs by platform
(the server's time zone included) is left out, so the same data gives the same file before an
update and after it, on every platform. Linux, which runs no 1175 node, shows less of 1175 (below).

  scripts\windows\dashboard-snapshot.ps1 -Base http://127.0.0.1:3080 -Out after.json
  scripts\windows\dashboard-snapshot.ps1 -Base http://127.0.0.1:3080 -Out before.json -Save answers
  scripts\windows\dashboard-snapshot.ps1 -From answers -Out out.json
  scripts\windows\dashboard-snapshot.ps1 -Before before.json -After after.json

-Before and -After list the lines that differ: 0 when none does, 1 when one does, 2 when a file
cannot be read. When one of the two was taken on 1.0.12 (only 1.0.13 answers password_required),
what 1.0.13 shows differently of the same data by design is left out.

With testdata\migrate\seed-1012.sql, take the snapshot after an update within two minutes of the
first start of 1.0.13. The seed has 1175 block 5002 found and not yet distributed, and 1.0.13's
miner distributes it in its first round of 1175 payouts, two minutes after it starts; from then
on miner A's solo blocks list block 5002 and its 1175 totals count it. A later snapshot differs
from 1.0.12's in those lines by design: that is not the move.

Linux runs no 1175 node, and there the API says merge-mining is not available. Where it says so,
the 1175 address in the payout settings is left out: 1.0.12 for Linux gives the stored one, 1.0.13
none. So on Linux the 1175 address is not shown, and block 5002 stays undistributed: 1.0.13 for
Linux runs no 1175 payouts, and its snapshot can be taken at any time. 1.0.12 for Linux, in solo
mode with a 1175 address saved, still distributes it after two minutes: take its snapshot right
before the update.

The miners are those of testdata\migrate\seed-1012.sql unless $env:MINERS names others, as
"label=address" pairs separated by spaces; a label is letters, digits and _. Windows PowerShell
5.1 and PowerShell 7 both run it.
#>
param(
    [string]$Base,
    [string]$Out,
    [string]$Save,
    [string]$From,
    [string]$Before,
    [string]$After
)
$ErrorActionPreference = 'Stop'
$inv = [Globalization.CultureInfo]::InvariantCulture

# What 1.0.13's API shows differently from 1.0.12's of the same data, one pattern a change, on the
# lines of a snapshot (as in the .sh).
$known1012 = @(
    '^"[A-Za-z0-9_]+-payouts\.',
    '^"[A-Za-z0-9_]+-solo-(blocks|payouts)\.total',
    '^"[A-Za-z0-9_]+-solo-(blocks|payouts)\.(blocks|payouts)": (null|\[\])\z',
    '^"pool-config\.password_required":'
)

# A snapshot's lines by key, each as '"key": value'.
function Get-Lines($path) {
    $got = New-Object 'System.Collections.Generic.Dictionary[string,string]' ([StringComparer]::Ordinal)
    foreach ($l in [IO.File]::ReadAllText($path, [Text.Encoding]::ASCII).Split("`n")) {
        $m = [regex]::Match($l.TrimEnd("`r"), '\A("(?:[^"\\]|\\.)*"): (.*?),?\z')
        if ($m.Success) { $got[$m.Groups[1].Value] = $m.Groups[1].Value + ': ' + $m.Groups[2].Value }
    }
    if ($got.Count -eq 0) { throw "$path is not a snapshot" }
    return , $got
}

function Exit-Usage([string]$why) {
    [Console]::Error.WriteLine($why)
    exit 2
}

# A relative path is taken from PowerShell's current location, as a cmdlet takes it: the .NET
# calls below would take it from the process's directory, which Set-Location does not move.
function Resolve-Arg([string]$p) {
    if (-not $p) { return $p }
    return $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($p)
}
try {
    $Out = Resolve-Arg $Out
    $Save = Resolve-Arg $Save
    $From = Resolve-Arg $From
    $Before = Resolve-Arg $Before
    $After = Resolve-Arg $After
} catch {
    Exit-Usage "cannot use the path: $($_.Exception.GetBaseException().Message)"
}

if ($Before -or $After) {
    if (-not ($Before -and $After) -or $Base -or $From -or $Out) { Exit-Usage 'give -Before FILE -After FILE alone' }
    try {
        $b = Get-Lines $Before
        $a = Get-Lines $After
    } catch {
        Exit-Usage "cannot read a snapshot: $($_.Exception.Message)"
    }
    $pw = '"pool-config.password_required"'
    $known = @()
    if ($b.ContainsKey($pw) -ne $a.ContainsKey($pw)) { $known = $known1012 }
    $keys = New-Object 'System.Collections.Generic.SortedSet[string]' ([StringComparer]::Ordinal)
    foreach ($k in $b.Keys) { [void]$keys.Add($k) }
    foreach ($k in $a.Keys) { [void]$keys.Add($k) }
    $left = 0
    $differ = 0
    $report = New-Object System.Collections.Generic.List[string]
    foreach ($k in $keys) {
        $lb = $null; $la = $null
        [void]$b.TryGetValue($k, [ref]$lb)
        [void]$a.TryGetValue($k, [ref]$la)
        if ($lb -ceq $la) { continue }
        $byDesign = $false
        foreach ($p in $known) {
            foreach ($l in @($lb, $la)) { if ($null -ne $l -and [regex]::IsMatch($l, $p)) { $byDesign = $true } }
        }
        if ($byDesign) { $left++; continue }
        $differ++
        if ($null -ne $lb) { $report.Add("- $lb") }
        if ($null -ne $la) { $report.Add("+ $la") }
    }
    if ($known.Count) {
        Write-Output "one of the two was taken on 1.0.12: $left lines that 1.0.13 shows differently by design are left out"
    }
    foreach ($l in $report) { Write-Output $l }
    if ($differ) { Write-Output "$differ lines differ"; exit 1 }
    Write-Output 'the same'
    exit 0
}

if (-not $Out -or (($Base -eq '') -eq ($From -eq ''))) { Exit-Usage 'give -Base URL or -From DIR, and -Out FILE' }

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
        'bestDiff|athDiff|totalWork|roundEffort|lastShare', 'currentHeight|balance|matureBalance|immatureBalance|balanceKnown')
    'solo-blocks'   = @('blocks\.\d+\.(confirmations|matures_in|mature)')
}
# Where the answer says merge-mining is not available, as on Linux, the 1175 address is not used
# and is left out too (as in the .sh).
$no1175Node = @{
    'pool-config' = @('payout_address_1175')
}
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

# A time is written in UTC and to the second, as forgesolo.db keeps it: PostgreSQL kept the
# fraction, and wrote the server's time zone (on Windows the PC's). One without a zone keeps none.
# Half a second or more counts as the next second, as the move rounds it (a time in the last second
# of the year 9999, which has no next, keeps its own). PowerShell 7 reads most times as dates itself
# (in the PC's zone); 5.1 leaves them as text.
$time = '\A([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]+))?([Zz]|([+-])([0-9]{2})(?::?([0-9]{2}))?)?\z'
function Format-Time([string]$s) {
    $m = [regex]::Match($s, $time)
    if (-not $m.Success) { return $s }
    $g = $m.Groups
    try {
        $t = [DateTime]::new([int]$g[1].Value, [int]$g[2].Value, [int]$g[3].Value,
            [int]$g[4].Value, [int]$g[5].Value, [int]$g[6].Value, [DateTimeKind]::Utc)
        if ($g[9].Success) {
            $off = [int]$g[10].Value * 60
            if ($g[11].Success) { $off += [int]$g[11].Value }
            if ($g[9].Value -eq '+') { $off = -$off }
            $t = $t.AddMinutes($off)
        }
    } catch {
        return $s
    }
    if ($g[7].Success -and $g[7].Value[0] -ge [char]'5') { $t = Add-Second $t }
    $u = $t.ToString('yyyy-MM-ddTHH:mm:ss', $inv)
    if ($g[8].Success) { return $u + 'Z' }
    return $u
}

# t a second later, or t in the last second of the year 9999, which has no next.
function Add-Second([datetime]$t) {
    if ($t.Ticks -ge [datetime]::MaxValue.Ticks - [timespan]::TicksPerSecond) { return $t }
    return $t.AddSeconds(1)
}

# A value as the file writes it: numbers as integers when they are whole, else to 8 decimals without
# trailing zeros; text as JSON, ASCII only, times in UTC to the second.
function Format-Value($v) {
    if ($null -eq $v) { return 'null' }
    if ([object]::ReferenceEquals($v, $emptyObject)) { return '{}' }
    if ([object]::ReferenceEquals($v, $emptyList)) { return '[]' }
    if ($v -is [bool]) { if ($v) { return 'true' } else { return 'false' } }
    if ($v -is [datetime]) {
        $z = ''
        if ($v.Kind -ne [DateTimeKind]::Unspecified) { $v = $v.ToUniversalTime(); $z = 'Z' }
        if ($v.Ticks % [timespan]::TicksPerSecond -ge [timespan]::TicksPerSecond / 2) { $v = Add-Second $v }
        return Format-Text ($v.ToString('yyyy-MM-ddTHH:mm:ss', $inv) + $z)
    }
    if ($v -is [int] -or $v -is [long] -or $v -is [decimal] -or $v -is [double] -or $v -is [single]) {
        $d = [decimal]$v
        if ($d -eq [decimal]::Truncate($d) -and [math]::Abs($d) -lt 1e15) { return ([long]$d).ToString($inv) }
        return $d.ToString('F8', $inv).TrimEnd('0').TrimEnd('.')
    }
    return Format-Text (Format-Time ([string]$v))
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
    if ($no1175Node.ContainsKey($kind) -and $doc.merge_mining_available -is [bool] -and -not $doc.merge_mining_available) {
        $patterns = $patterns + $no1175Node[$kind]
    }
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
