#Requires -Version 7.0
<#
.SYNOPSIS
    NexusGlobal 生产环境密钥配置脚本。

.DESCRIPTION
    生成密码学安全的 32 字节 base64 密钥并写入 env 文件（默认 .env）：
      - MASTER_KEY_BASE64       主加密密钥（config required；mock 模式信封加密用）
      - JWT_SECRET              JWT 签名密钥（生产 >=32 字节）
      - INTERNAL_TOKEN          内部 API 认证令牌（生产 >=32 字节）
      - ADMIN_SESSION_SECRET    管理台会话 Cookie 签名密钥（生产 >=32 字节）
      - WEBHOOK_SIGNING_KEY     Webhook HMAC-SHA256 签名密钥（生产 >=32 字节；
                                STATEMENT_SIGNING_KEY 未配置时自动回退此值，无需单独生成）
    KMS_MODE=real 时同时写入 VAULT_BASE_URL / VAULT_TOKEN（需自备 HashiCorp Vault）。

    行为：
      - 只替换/追加上述密钥行与 VAULT 行，env 文件其余内容原样保留；
      - 修改前自动备份为 <envfile>.bak-yyyyMMdd-HHmmss；
      - 密钥明文不回显到控制台，仅显示掩码摘要；
      - 写入后执行生产门禁自检（对齐 internal/platform/config validateProductionConfig），
        发现 mock 模式或缺失门禁开关时黄字警告（不擅自修改业务模式项）。

.EXAMPLE
    # 交互式（默认目标 .env，逐项确认）
    .\scripts\gen_prod_secrets.ps1

.EXAMPLE
    # 非交互（CI / 自动化）
    .\scripts\gen_prod_secrets.ps1 -EnvFile .env.production `
        -KmsMode real -VaultBaseUrl https://vault.prod.example.com:8200 -VaultToken hvs.xxxx -Force
#>
[CmdletBinding()]
param(
    # 目标 env 文件（默认当前目录 .env；不存在时基于 .env.example 创建）
    [string]$EnvFile = ".env",

    # KMS 模式：real（生产默认，走 HashiCorp Vault）/ mock（仅本地联调，生产启动会被拒绝）
    [ValidateSet("real", "mock")]
    [string]$KmsMode = "real",

    # KMS_MODE=real 必填
    [string]$VaultBaseUrl = "",
    [string]$VaultToken = "",

    # 跳过写入前确认
    [switch]$Force
)

$ErrorActionPreference = "Stop"

# ---------- 工具函数 ----------

# 生成 n 字节密码学安全随机数的 base64（对齐服务端 32 字节要求）
function New-SecretBase64 {
    param([int]$Bytes = 32)
    $buf = New-Object byte[] $Bytes
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($buf)
    } finally {
        $rng.Dispose()
    }
    return [Convert]::ToBase64String($buf)
}

# 掩码显示：只露首尾各 4 字符
function Get-Masked {
    param([string]$Value)
    if ($Value.Length -le 8) { return "****" }
    return $Value.Substring(0, 4) + "****" + $Value.Substring($Value.Length - 4)
}

# 在 env 行数组中原地替换 KEY=VALUE；不存在则追加
function Set-EnvLine {
    param(
        [System.Collections.Generic.List[string]]$Lines,
        [string]$Key,
        [string]$Value
    )
    $pattern = '^(?#注释容忍)(\s*)' + [regex]::Escape($Key) + '\s*=.*$'
    for ($i = 0; $i -lt $Lines.Count; $i++) {
        if ($Lines[$i] -match $pattern) {
            $Lines[$i] = "$Key=$Value"
            return
        }
    }
    $Lines.Add("$Key=$Value")
}

# 读取 env 文件中 KEY 的现值（未找到返回 $null）
function Get-EnvValue {
    param(
        [System.Collections.Generic.List[string]]$Lines,
        [string]$Key
    )
    foreach ($line in $Lines) {
        if ($line -match '^\s*' + [regex]::Escape($Key) + '\s*=\s*(.*)$') {
            # 剥离行内注释（" # " 之后的内容），再取纯值
            return (($Matches[1] -split '\s+#')[0].Trim())
        }
    }
    return $null
}

# ---------- 前置检查 ----------

if (Test-Path -LiteralPath $EnvFile) {
    $envPath = (Convert-Path -LiteralPath $EnvFile)
} else {
    $dir = Split-Path -Parent $EnvFile
    if ([string]::IsNullOrEmpty($dir)) { $dir = (Get-Location).Path }
    $envPath = Join-Path $dir (Split-Path -Leaf $EnvFile)
    $example = Join-Path (Split-Path -Parent $envPath) ".env.example"
    if (Test-Path -LiteralPath $example) {
        Copy-Item -LiteralPath $example -Destination $envPath
        Write-Host "[初始化] 目标文件不存在，已基于 .env.example 创建: $envPath" -ForegroundColor Cyan
    } else {
        Write-Host "[错误] 目标文件 $envPath 不存在，且找不到 .env.example 模板" -ForegroundColor Red
        exit 1
    }
}

$lines = [System.Collections.Generic.List[string]](Get-Content -LiteralPath $envPath)

# ---------- KMS 参数收集 ----------

if ($KmsMode -eq "real") {
    if (-not $VaultBaseUrl) {
        $VaultBaseUrl = Read-Host "请输入 Vault 地址 (VAULT_BASE_URL，如 https://vault.prod.example.com:8200)"
    }
    if (-not $VaultToken) {
        $VaultToken = Read-Host "请输入 Vault Token (VAULT_TOKEN)"
    }
    if (-not $VaultBaseUrl -or -not $VaultToken) {
        Write-Host "[错误] KMS_MODE=real 要求 VAULT_BASE_URL 与 VAULT_TOKEN 均非空（config.go 强制校验）" -ForegroundColor Red
        exit 1
    }
} else {
    Write-Warning "KMS_MODE=mock 在 APP_ENV=production 下会被拒绝启动（validateProductionConfig）。仅用于本地联调。"
}

# ---------- 生成密钥 ----------

$secrets = [ordered]@{
    "MASTER_KEY_BASE64"    = New-SecretBase64
    "JWT_SECRET"           = New-SecretBase64
    "INTERNAL_TOKEN"       = New-SecretBase64
    "ADMIN_SESSION_SECRET" = New-SecretBase64
    "WEBHOOK_SIGNING_KEY"  = New-SecretBase64
}

# 已知禁止值（示例/占位 key，生产校验拒绝）
$forbidden = @(
    "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",       # docker-compose / CI 示例
    "dGVzdGtleTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=",           # 测试值
    "<BASE64_32BYTE_KEY_REQUIRED>"                        # .env.example 占位
)
foreach ($v in $secrets.Values) {
    if ($forbidden -contains $v) {
        Write-Host "[错误] 随机数生成异常（命中已知示例值），终止" -ForegroundColor Red
        exit 1
    }
}

# ---------- 确认与备份 ----------

Write-Host ""
Write-Host "=== 将写入 $envPath ===" -ForegroundColor White
Write-Host "KMS_MODE            = $KmsMode"
if ($KmsMode -eq "real") { Write-Host "VAULT_BASE_URL      = $VaultBaseUrl" }
foreach ($k in $secrets.Keys) {
    Write-Host ("{0,-20}= {1}  (32 字节 base64)" -f $k, (Get-Masked $secrets[$k]))
}
Write-Host ""

if (-not $Force) {
    $answer = Read-Host "确认写入？旧文件将自动备份 (yes/no)"
    if ($answer -notin @("yes", "y")) {
        Write-Host "已取消，未做任何修改。" -ForegroundColor Yellow
        exit 0
    }
}

$backup = "$envPath.bak-$(Get-Date -Format 'yyyyMMdd-HHmmss')"
Copy-Item -LiteralPath $envPath -Destination $backup
Write-Host "[备份] $backup" -ForegroundColor DarkGray

# ---------- 写入 ----------

Set-EnvLine -Lines $lines -Key "KMS_MODE" -Value $KmsMode
if ($KmsMode -eq "real") {
    Set-EnvLine -Lines $lines -Key "VAULT_BASE_URL" -Value $VaultBaseUrl
    Set-EnvLine -Lines $lines -Key "VAULT_TOKEN" -Value $VaultToken
}
foreach ($k in $secrets.Keys) {
    Set-EnvLine -Lines $lines -Key $k -Value $secrets[$k]
}

Set-Content -LiteralPath $envPath -Value $lines -Encoding utf8
Write-Host "[完成] 密钥已写入 $envPath" -ForegroundColor Green

# ---------- 生产门禁自检（只警告，不擅自修改业务模式） ----------

Write-Host ""
Write-Host "=== 生产门禁自检（对齐 internal/platform/config/validateProductionConfig） ===" -ForegroundColor White
$warnings = @()

if ((Get-EnvValue $lines "APP_ENV") -ne "production") {
    $warnings += "APP_ENV 未设为 production（生产校验仅在 APP_ENV=production 时触发）"
}

# 生产禁 mock 项
foreach ($k in @("EXTERNAL_MODE", "ACQUIRER_MODE", "ACQUIRER_PROVIDER", "CHECKOUT_ORDER_PROVIDER", "ISSUING_PROVIDER")) {
    $v = Get-EnvValue $lines $k
    if ($v -eq "mock" -or [string]::IsNullOrWhiteSpace($v)) {
        $warnings += "${k}=${v} — 生产环境禁止 mock，请接入真实 Provider"
    }
}

# EXTERNAL_MODE=real 需要连接器地址
if ((Get-EnvValue $lines "EXTERNAL_MODE") -eq "real") {
    foreach ($p in @("LP", "BANK", "MAE")) {
        if ([string]::IsNullOrWhiteSpace((Get-EnvValue $lines "${p}_BASE_URL"))) {
            $warnings += "EXTERNAL_MODE=real 要求 ${p}_BASE_URL 非空"
        }
    }
}

# 生产门禁开关
foreach ($k in @("REQUIRE_VAULT", "REQUIRE_WEBHOOK_SECRET", "REQUIRE_ADMIN_AUTH")) {
    if ((Get-EnvValue $lines $k) -ne "true") {
        $warnings += "${k} 应为 true（生产强制）"
    }
}

if ((Get-EnvValue $lines "DB_MIGRATION_MODE") -eq "auto") {
    $warnings += "DB_MIGRATION_MODE=auto — 生产建议 manual（手工控制迁移窗口）"
}

if ($warnings.Count -eq 0) {
    Write-Host "自检通过，未发现问题。" -ForegroundColor Green
} else {
    foreach ($w in $warnings) {
        Write-Warning $w
    }
}

# ---------- 收尾提示 ----------

Write-Host ""
Write-Host "=== 后续步骤 ===" -ForegroundColor White
Write-Host "1. 重启服务使配置生效:  docker compose up -d（compose 自动读取同目录 .env）"
Write-Host "2. .env 已被 .gitignore 忽略，严禁提交仓库；备份文件 $backup 用后请删除"
Write-Host "3. 密钥另存至密码管理器/KMS；丢失 MASTER_KEY_BASE64 将无法解密已入库的凭证信封"
if ($KmsMode -eq "mock") {
    Write-Host "4. 上生产前重跑本脚本并使用 -KmsMode real" -ForegroundColor Yellow
}
