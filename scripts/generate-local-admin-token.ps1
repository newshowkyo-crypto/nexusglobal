param(
    [string]$Secret = "nexusglobal-local-jwt-secret-change-me-2026",
    [string]$TenantId = "00000000-0000-0000-0000-000000000001",
    [ValidateSet("viewer", "operator", "approver", "security-admin")]
    [string]$Role = "security-admin",
    [int]$LifetimeMinutes = 15
)

$ErrorActionPreference = "Stop"

function ConvertTo-Base64Url {
    param([byte[]]$Bytes)
    return [Convert]::ToBase64String($Bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

$now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
$header = @{ alg = "HS256"; typ = "JWT" } | ConvertTo-Json -Compress
$payload = @{
    iss       = "nexusglobal-admin"
    aud       = "nexusglobal-admin-api"
    sub       = "local-admin"
    username  = "local-admin"
    role      = $Role
    tenant_id = $TenantId
    # Allow for small host/container clock differences in local Docker setups.
    iat       = $now - 60
    nbf       = $now - 60
    exp       = $now + ($LifetimeMinutes * 60)
} | ConvertTo-Json -Compress

$headerPart = ConvertTo-Base64Url ([Text.Encoding]::UTF8.GetBytes($header))
$payloadPart = ConvertTo-Base64Url ([Text.Encoding]::UTF8.GetBytes($payload))
$unsigned = "$headerPart.$payloadPart"

$hmac = [Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($Secret))
try {
    $signaturePart = ConvertTo-Base64Url ($hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($unsigned)))
} finally {
    $hmac.Dispose()
}

Write-Output "$unsigned.$signaturePart"
