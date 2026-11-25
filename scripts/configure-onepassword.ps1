param(
    [ValidateSet('test', 'production', 'jquants')][string]$Environment = 'test',
    [string]$SecretReference,
    [switch]$ReuseToken
)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$directory = Join-Path $projectRoot 'secrets'
$destination = Join-Path $directory 'onepassword.json'
$secureToken = $null
$pointer = [IntPtr]::Zero
$tokenBytes = $null
$temp = $null
try {
    if (-not $SecretReference) { $SecretReference = Read-Host 'APIパスワードの秘密参照 (op://保管庫/項目/フィールド)' }
    $parts = ($SecretReference -replace '^op://', '') -split '/'
    if (-not $SecretReference.StartsWith('op://') -or $SecretReference.Length -gt 4096 -or $SecretReference -match '[\r\n\x00?#\\]' -or $parts.Count -lt 3 -or $parts.Count -gt 4 -or @($parts | Where-Object { [string]::IsNullOrWhiteSpace($_) }).Count -gt 0) { throw 'invalid reference' }
    $references = @{}
    $protected = $null
    if (Test-Path -LiteralPath $destination) {
        $existing = Get-Content -LiteralPath $destination -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($existing.version -ne 1) { throw 'invalid existing configuration' }
        foreach ($property in $existing.references.PSObject.Properties) { $references[$property.Name] = $property.Value }
        if ($ReuseToken) { $protected = $existing.protected_token }
    }
    if (-not $ReuseToken) {
        Add-Type -AssemblyName System.Security
        $secureToken = Read-Host '1Passwordサービスアカウントのトークン（非表示）' -AsSecureString
        $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureToken)
        $plainToken = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
        if ($plainToken.Length -lt 16 -or $plainToken.Length -gt 16384 -or $plainToken -match '[\r\n\x00]') { throw 'invalid token' }
        $tokenBytes = [Text.Encoding]::UTF8.GetBytes($plainToken)
        $plainToken = $null
        $encrypted = [Security.Cryptography.ProtectedData]::Protect($tokenBytes, $null, [Security.Cryptography.DataProtectionScope]::CurrentUser)
        $protected = [Convert]::ToBase64String($encrypted)
    }
    if (-not $protected) { throw 'no token to reuse' }
    $references[$Environment] = $SecretReference
    $configuration = @{ version = 1; references = $references; protected_token = $protected } | ConvertTo-Json -Depth 4
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $temp = Join-Path $directory ([Guid]::NewGuid().ToString('N') + '.tmp')
    [IO.File]::WriteAllText($temp, $configuration, (New-Object Text.UTF8Encoding($false)))
    if (Test-Path -LiteralPath $destination) { [IO.File]::Replace($temp, $destination, [NullString]::Value) }
    else { [IO.File]::Move($temp, $destination) }
    Write-Host ('1Password設定を保存しました: environment=' + $Environment + ' / protection=Windows-DPAPI-CurrentUser')
} catch {
    throw '1Password設定を保存できませんでした。秘密参照・入力・既存設定を確認してください（秘密の値や詳細は出力しません）。'
} finally {
    $plainToken = $null
    if ($tokenBytes) { [Array]::Clear($tokenBytes, 0, $tokenBytes.Length) }
    if ($pointer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
    if ($secureToken) { $secureToken.Dispose() }
    if ($temp -and (Test-Path -LiteralPath $temp)) { Remove-Item -LiteralPath $temp }
}
