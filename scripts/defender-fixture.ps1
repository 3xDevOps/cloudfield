# Defender's machine-learning signature sometimes names the fake release
# server, a test fixture built in the job that nothing ships. A detection
# counts unless every resource in it is exactly that one file: one that
# names another file, alone or beside the fixture, counts, and so does one
# that names nothing.

# Get-FixtureResource is how Defender names the file at Path in a
# detection's Resources.
function Get-FixtureResource([string]$Path) {
    return 'file:_' + [IO.Path]::GetFullPath($Path)
}

# Test-FixtureOnlyDetection reports whether every resource of Detection is
# FixtureResource, compared whole and without regard to case, as Windows
# compares paths.
function Test-FixtureOnlyDetection($Detection, [string]$FixtureResource) {
    $resources = @($Detection.Resources)
    if ($resources.Count -eq 0) { return $false }
    foreach ($resource in $resources) {
        if ([string]$resource -ne $FixtureResource) { return $false }
    }
    return $true
}

# Select-CountedDetections returns the detections that count and prints,
# for each one of the fixture alone, the threat's name and the path, so a
# change in what Defender says about the fixture shows in the job's log.
function Select-CountedDetections($Detections, [string]$FixtureResource, [string]$When) {
    foreach ($detection in @($Detections)) {
        if (-not (Test-FixtureOnlyDetection $detection $FixtureResource)) {
            $detection
            continue
        }
        $name = 'a threat Defender no longer lists'
        $threat = Get-MpThreat -ThreatID $detection.ThreatID -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($threat) { $name = $threat.ThreatName }
        Write-Host "note: Defender detection $When of the test fixture alone, not counted: $name (threat $($detection.ThreatID)) in $(@($detection.Resources) -join '; ')"
    }
}
