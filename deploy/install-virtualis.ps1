# Download validation is usable independently for fixtures; installation is explicit.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-Transport([string]$Url, [bool]$AllowInsecure = $false) {
    $uri = [Uri]$Url
    if (-not $uri.IsAbsoluteUri -or $uri.UserInfo -or ($uri.Scheme -ne 'https' -and -not ($AllowInsecure -and $uri.Scheme -eq 'http'))) {
        throw 'HTTPS required; HTTP needs explicit --allow-insecure. Certificate verification remains enabled.'
    }
}
function Receive-File([string]$Url, [string]$OutFile, [bool]$AllowInsecure = $false) {
    Assert-Transport $Url $AllowInsecure
    $protocols = '=https'
    if ($AllowInsecure) { $protocols = '=http,https' }
    & curl.exe --fail --silent --show-error --location --retry 3 --tlsv1.2 --proto $protocols --proto-redir $protocols --output $OutFile $Url
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $OutFile) -or (Get-Item -LiteralPath $OutFile).Length -eq 0) { throw 'Download failed' }
}
function Resolve-Release([string]$Repo, [string]$Version) {
    if ($Version -eq 'latest') {
        $resolved = & curl.exe --fail --silent --show-error --location --head --tlsv1.2 --proto '=https' --proto-redir '=https' --output NUL --write-out '%{url_effective}' "https://github.com/$Repo/releases/latest"
        if ($LASTEXITCODE -ne 0 -or -not $resolved.StartsWith("https://github.com/$Repo/releases/tag/")) { throw 'Cannot resolve immutable release version' }
        $Version = $resolved.Substring($resolved.LastIndexOf('/') + 1)
    }
    if ($Version -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$' -or $Version -eq 'latest') { throw 'Invalid release version' }
    return $Version
}
function Get-ExpectedHash([string]$Manifest, [string]$Asset) {
    if (-not (Test-Path -LiteralPath $Manifest -PathType Leaf) -or (Get-Item -LiteralPath $Manifest).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Missing or linked checksum manifest' }
    $found = @()
    foreach ($line in [IO.File]::ReadAllLines($Manifest)) {
        if (-not $line.Trim()) { continue }
        if ($line -notmatch '^([0-9a-fA-F]{64})\s+\*?([^\s]+)\s*$') { throw 'Malformed checksum manifest' }
        $digest = $Matches[1]; $name = $Matches[2]
        if ($name -ceq $Asset) { $found += $digest.ToLowerInvariant() }
    }
    if ($found.Count -ne 1) { throw 'Missing or duplicate checksum entry' }
    return $found[0]
}
function Assert-Sha256([string]$File, [string]$Expected) {
    if ($Expected -notmatch '^[0-9a-fA-F]{64}$') { throw 'Exact SHA-256 required' }
    $item = Get-Item -LiteralPath $File
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Linked binary rejected' }
    $actual = (Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash
    if ($actual -ine $Expected) { throw 'SHA-256 mismatch; installation refused' }
}
function Assert-Pe([string]$File) {
    $stream = [IO.File]::OpenRead($File)
    try { if ($stream.ReadByte() -ne 0x4d -or $stream.ReadByte() -ne 0x5a) { throw 'Not a Windows PE binary' } }
    finally { $stream.Dispose() }
}
function Receive-VerifiedRelease([string]$Repo, [string]$Version, [string]$Asset, [string]$File, [string]$Expected, [string]$Work) {
    # Only a failed binary transfer is eligible for another source; hash errors are fatal.
    try { Receive-File "https://github.com/$Repo/releases/download/$Version/$Asset" $File }
    catch { return $false }
    if (-not $Expected) {
        $manifest = Join-Path $Work 'SHA256SUMS'
        Receive-File "https://github.com/$Repo/releases/download/$Version/SHA256SUMS" $manifest
        $Expected = Get-ExpectedHash $manifest $Asset
        Write-Warning 'TLS same-release checksums verify integrity, not an independent publisher signature.'
    }
    Assert-Sha256 $File $Expected
    return $true
}
function Set-PrivateAcl([string]$Path, [bool]$IsDirectory = $false) {
    $acl = New-Object Security.AccessControl.FileSecurity
    if ($IsDirectory) { $acl = New-Object Security.AccessControl.DirectorySecurity }
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sidText in @('S-1-5-18', 'S-1-5-32-544')) {
        $sid = New-Object Security.Principal.SecurityIdentifier($sidText)
        if ($IsDirectory) {
            $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        } else {
            $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'Allow')
        }
        $acl.AddAccessRule($rule)
    }
    # Apply with explicit owner rights preserved; SeSecurityPrivilege is never assumed.
    $item = Get-Item -LiteralPath $Path
    $item.SetAccessControl($acl)
}
function Invoke-Installer([string[]]$Arguments) {
    $role = 'master'; $master = ''; $tokenFile = ''; $name = "node-$env:COMPUTERNAME"
    $version = 'latest'; $agentVersion = 'latest'; $expected = ''; $allowInsecure = $false; $mode = '1'; $agentChecksums = ''
    for ($i = 0; $i -lt $Arguments.Count; $i++) {
        switch -Exact ($Arguments[$i]) {
            '--agent' { $role = 'agent' }
            '--master' { $role = 'master' }
            '--master-url' { $i++; $master = $Arguments[$i] }
            '--token-file' { $i++; $tokenFile = $Arguments[$i] }
            '--token' { throw 'Use --token-file; command-line secrets are rejected' }
            '--name' { $i++; $name = $Arguments[$i] }
            '--version' { $i++; $version = $Arguments[$i] }
            '--agent-version' { $i++; $agentVersion = $Arguments[$i] }
            '--expected-sha256' { $i++; $expected = $Arguments[$i] }
            '--agent-checksum-file' { $i++; $agentChecksums = $Arguments[$i] }
            '--mode' { $i++; $mode = $Arguments[$i] }
            '--allow-insecure' { $allowInsecure = $true }
            '--no-start' { } # This installer never claims that Go binaries implement Windows SCM services.
            '--update' { }
            '--help' { Write-Output 'Usage: --master [--version vX.Y.Z] [--expected-sha256 digest]; --agent --master-url https://MASTER --token-file file [--mode 1] [--allow-insecure]'; return }
            default { throw 'Unknown installer option' }
        }
    }
    if ($expected -and $expected -notmatch '^[0-9a-fA-F]{64}$') { throw 'Invalid expected SHA-256' }
    if ($mode -notin @('1', '2', '3', '4')) { throw 'Unknown mode' }
    if ($mode -eq '3') { throw 'Mode 3 LXD-compatible runtime is unsupported; traditional LXC CLI is not compatible' }
    if ($role -eq 'agent' -and $mode -ne '1') { throw 'Automatic backend installation is Linux-only' }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    if (-not (New-Object Security.Principal.WindowsPrincipal($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Installation requires Administrator; hash validation functions do not' }
    if ($role -eq 'agent') {
        Assert-Transport $master $allowInsecure
        if (-not (Test-Path -LiteralPath $tokenFile -PathType Leaf) -or (Get-Item -LiteralPath $tokenFile).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'A non-linked token file is required' }
        if ([IO.File]::ReadAllText($tokenFile).Trim() -notmatch '^[A-Za-z0-9._~-]+$') { throw 'Invalid token file' }
        if ($name -notmatch '^[A-Za-z0-9._-]{1,63}$') { throw 'Invalid node name' }
    }
    $work = Join-Path $env:TEMP ('virtualis-install-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    Set-PrivateAcl $work $true
    try {
        $repo = 'SakuraOpenSource/virtualis'; $asset = 'virtualis-windows-amd64.exe'
        if ($role -eq 'agent') { $repo += '-agent'; $asset = 'virtualis-agent-windows-amd64.exe' }
        $version = Resolve-Release $repo $version
        $binary = Join-Path $work $asset
        if (-not (Receive-VerifiedRelease $repo $version $asset $binary $expected $work)) {
            if ($role -ne 'agent') { throw 'Required master transfer failed' }
            if ($master.StartsWith('http://') -and -not $expected) { throw 'HTTP fallback requires an independently supplied expected SHA-256' }
            Receive-File "$master/api/agent/binary?os=windows&arch=amd64" $binary $allowInsecure
            if (-not $expected) {
                $manifest = Join-Path $work 'master-SHA256SUMS'
                Receive-File "$master/api/agent/binary?os=windows&arch=amd64&checksum=1" $manifest $allowInsecure
                $expected = Get-ExpectedHash $manifest $asset
                Write-Warning 'Same-master TLS checksum is not an independent publisher signature.'
            }
            Assert-Sha256 $binary $expected
        }
        Assert-Pe $binary
        $dest = "C:\opt\virtualis\$role"
        $stagedPackages = Join-Path $work 'agent-packages'
        if ($role -eq 'master') {
            New-Item -ItemType Directory -Path $stagedPackages | Out-Null
            $agentVersion = Resolve-Release 'SakuraOpenSource/virtualis-agent' $agentVersion
            $manifest = Join-Path $work 'agent-SHA256SUMS'
            if ($agentChecksums) { Copy-Item -LiteralPath $agentChecksums -Destination $manifest }
            else { Receive-File "https://github.com/SakuraOpenSource/virtualis-agent/releases/download/$agentVersion/SHA256SUMS" $manifest }
            foreach ($target in @('linux-amd64','linux-arm64','darwin-amd64','darwin-arm64','windows-amd64')) {
                $package = "virtualis-agent-$target"; if ($target.StartsWith('windows')) { $package += '.exe' }
                $digest = Get-ExpectedHash $manifest $package
                $file = Join-Path $stagedPackages $package
                Receive-File "https://github.com/SakuraOpenSource/virtualis-agent/releases/download/$agentVersion/$package" $file
                Assert-Sha256 $file $digest
                if ($target.StartsWith('windows')) { Assert-Pe $file }
                Add-Content -LiteralPath (Join-Path $stagedPackages 'SHA256SUMS') -Encoding Ascii -Value "$digest  $package"
            }
        }
        New-Item -ItemType Directory -Force -Path $dest | Out-Null
        Set-PrivateAcl $dest $true
        New-Item -ItemType Directory -Force -Path (Join-Path $dest 'data') | Out-Null
        $exe = 'virtualis.exe'; if ($role -eq 'agent') { $exe = 'virtualis-agent.exe' }
        Copy-Item -LiteralPath $binary -Destination (Join-Path $dest $exe) -Force
        if ($role -eq 'agent') { Copy-Item -LiteralPath $tokenFile -Destination (Join-Path $dest 'token') -Force; Set-PrivateAcl (Join-Path $dest 'token') }
        else { Copy-Item -LiteralPath $stagedPackages -Destination $dest -Recurse -Force }
        Write-Output "Verified binary installed at $dest\$exe. Windows SCM service integration is unsupported; nothing was started."
        if ($role -eq 'agent') { Write-Output 'Run manually with --token-file C:\opt\virtualis\agent\token; append --allow-insecure only for explicitly accepted HTTP.' }
    } finally { Remove-Item -LiteralPath $work -Recurse -Force }
}
if ($MyInvocation.InvocationName -ne '.') { Invoke-Installer $args }
