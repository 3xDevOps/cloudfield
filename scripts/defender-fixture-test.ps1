# Checks scripts/defender-fixture.ps1 on made-up detections. The update
# smoke test runs it before it trusts the filter, and it runs anywhere
# PowerShell does: pwsh scripts/defender-fixture-test.ps1
$ErrorActionPreference = 'Stop'
# As strict as the smoke test that uses the filter.
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'defender-fixture.ps1')

$fixture = 'file:_D:\a\_temp\fake-release.exe'
$cases = @(
    @{ Name = 'the fixture alone'; Resources = @($fixture); Ignored = $true },
    @{ Name = 'the fixture alone, twice'; Resources = @($fixture, $fixture); Ignored = $true },
    @{ Name = 'the fixture in another case'; Resources = @('file:_d:\A\_temp\FAKE-RELEASE.exe'); Ignored = $true },
    @{ Name = 'another file'; Resources = @('file:_D:\a\_temp\aether-new.exe'); Ignored = $false },
    @{ Name = 'the fixture and another file'; Resources = @($fixture, 'file:_D:\a\_temp\aether-new.exe'); Ignored = $false },
    @{ Name = 'another file and the fixture'; Resources = @('file:_D:\a\_temp\aether-new.exe', $fixture); Ignored = $false },
    @{ Name = 'a file whose path starts with the fixture''s'; Resources = @($fixture + '.bak'); Ignored = $false },
    @{ Name = 'a file whose path ends with the fixture''s'; Resources = @('file:_E:\copy\D:\a\_temp\fake-release.exe'); Ignored = $false },
    @{ Name = 'a file of the same name elsewhere'; Resources = @('file:_C:\Users\runner\fake-release.exe'); Ignored = $false },
    @{ Name = 'the fixture as a process'; Resources = @('process:_pid:4242,ProcessStart:1'); Ignored = $false },
    @{ Name = 'the fixture as a wildcard'; Resources = @('file:_D:\a\_temp\*'); Ignored = $false },
    @{ Name = 'no resource'; Resources = @(); Ignored = $false }
)
foreach ($case in $cases) {
    $detection = [pscustomobject]@{ DetectionID = '{test}'; ThreatID = 1; Resources = $case.Resources }
    $ignored = Test-FixtureOnlyDetection $detection $fixture
    if ($ignored -ne $case.Ignored) {
        throw "A detection that names $($case.Name) is ignored: $ignored; want $($case.Ignored)."
    }
}
# What the smoke test's assertion sees: the detections that count, and a
# line in the log for each one that does not.
function Get-MpThreat([long]$ThreatID) { if ($ThreatID -eq 7) { [pscustomobject]@{ ThreatName = 'Trojan:Win32/Test.A!ml' } } }
$other = 'file:_D:\a\_temp\aether-new.exe'
$alone = [pscustomobject]@{ DetectionID = '{alone}'; ThreatID = 7; Resources = @($fixture) }
$elsewhere = [pscustomobject]@{ DetectionID = '{elsewhere}'; ThreatID = 8; Resources = @($other) }
$both = [pscustomobject]@{ DetectionID = '{both}'; ThreatID = 9; Resources = @($fixture, $other) }
$sets = @(
    @{ Name = 'the fixture alone'; Detections = @($alone); Counted = @(); Notes = 1 },
    @{ Name = 'another file'; Detections = @($elsewhere); Counted = @('{elsewhere}'); Notes = 0 },
    @{ Name = 'the fixture together with another file'; Detections = @($both); Counted = @('{both}'); Notes = 0 },
    @{ Name = 'the fixture alone and, apart, another file'; Detections = @($alone, $elsewhere); Counted = @('{elsewhere}'); Notes = 1 },
    @{ Name = 'nothing'; Detections = @(); Counted = @(); Notes = 0 }
)
foreach ($set in $sets) {
    $out = @(Select-CountedDetections $set.Detections $fixture 'in a test' 6>&1)
    $counted = @($out | Where-Object { $_ -isnot [Management.Automation.InformationRecord] } | ForEach-Object { $_.DetectionID })
    $notes = @($out | Where-Object { $_ -is [Management.Automation.InformationRecord] } | ForEach-Object { [string]$_ })
    if (($counted -join ',') -ne ($set.Counted -join ',') -or $notes.Count -ne $set.Notes) {
        throw "Detections of $($set.Name): counted '$($counted -join ',')' with $($notes.Count) notes; want '$($set.Counted -join ',')' with $($set.Notes)."
    }
    foreach ($note in $notes) {
        if ($note -notlike '*Trojan:Win32/Test.A!ml (threat 7) in file:_D:\a\_temp\fake-release.exe') {
            throw "The note for an ignored detection does not name the threat and the path: $note"
        }
    }
}
$unlisted = [pscustomobject]@{ DetectionID = '{unlisted}'; ThreatID = 11; Resources = @($fixture) }
$note = [string](Select-CountedDetections @($unlisted) $fixture 'in a test' 6>&1)
if ($note -notlike '*a threat Defender no longer lists (threat 11) in file:_D:\a\_temp\fake-release.exe') {
    throw "The note for a threat Defender does not list: $note"
}
if ((Get-FixtureResource (Join-Path ([IO.Path]::GetTempPath()) 'fake-release.exe')) -notmatch '^file:_.*fake-release\.exe$') {
    throw 'Get-FixtureResource does not name the file as Defender does.'
}
Write-Host "ok: the Defender fixture filter ignores a detection of the fixture alone and counts the other $($cases.Count - 3) kinds"
