# Fixture-only checks. Dot-sourcing does not invoke the installer.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\install-virtualis.ps1"
function Expect-Failure([scriptblock]$Action) {
    $failed = $false
    try { & $Action } catch { $failed = $true }
    if (-not $failed) { throw 'Expected fail-closed rejection' }
}
$work = Join-Path $env:TEMP ('virtualis-verify-fixture-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $binary = Join-Path $work 'fixture.exe'
    [IO.File]::WriteAllBytes($binary, [byte[]]@(0x4d,0x5a,0x90,0x00,0x46,0x49,0x58))
    $digest = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash
    Assert-Sha256 $binary $digest
    Assert-Pe $binary
    Expect-Failure { Assert-Sha256 $binary ('0' * 64) }
    Expect-Failure { Assert-Sha256 $binary 'invalid' }
    [IO.File]::WriteAllBytes($binary, [byte[]]@(0x00,0x00,0x90,0x00))
    Expect-Failure { Assert-Pe $binary }
    $manifest = Join-Path $work 'SHA256SUMS'
    [IO.File]::WriteAllText($manifest, "$digest  fixture.exe`n")
    if ((Get-ExpectedHash $manifest 'fixture.exe') -ine $digest) { throw 'Manifest digest mismatch' }
    Expect-Failure { Get-ExpectedHash $manifest 'missing.exe' }
    [IO.File]::AppendAllText($manifest, "$digest  fixture.exe`n")
    Expect-Failure { Get-ExpectedHash $manifest 'fixture.exe' }
    [IO.File]::WriteAllText($manifest, "invalid fixture.exe`n")
    Expect-Failure { Get-ExpectedHash $manifest 'fixture.exe' }
    Expect-Failure { Get-ExpectedHash (Join-Path $work 'absent') 'fixture.exe' }
    Expect-Failure { Assert-Transport 'http://127.0.0.1:1' }
    Assert-Transport 'http://127.0.0.1:1' $true
    Assert-Transport 'https://example.invalid'
    Expect-Failure { Assert-Transport 'https://user:fixture@example.invalid' }
    $fixtureAcl = Get-Acl -LiteralPath $binary
    Set-PrivateAcl $binary
    $acl = Get-Acl -LiteralPath $binary
    if (-not $acl.AreAccessRulesProtected) { throw 'Token-file ACL inheritance remains enabled' }
    $allowed = @('S-1-5-18','S-1-5-32-544')
    foreach ($rule in $acl.Access) {
        $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($sid -notin $allowed) { throw 'Unexpected token-file ACL principal' }
    }
    Write-Output 'PASS: SHA-256 match/mismatch, strict/missing/duplicate manifests, two-byte MZ, explicit HTTP opt-in, protected fixture ACLs. No installation or services invoked.'
} finally {
    # The fixture ACL intentionally removed Everyone deletion rights; restore them without privileged APIs.
    & icacls.exe $work /grant '*S-1-1-0:(OI)(CI)F' /T /C /Q | Out-Null
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
}
