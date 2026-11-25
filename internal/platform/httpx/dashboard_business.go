package httpx

import (
	_ "embed"
	"strings"
)

// Keep the operational presentation separate from API identifiers and permissions.
//
//go:embed dashboard_business.css
var dashboardBusinessCSS string

//go:embed dashboard_business.js
var dashboardBusinessJS string

//go:embed dashboard_operations.js
var dashboardOperationsJS string

//go:embed dashboard_credentials.js
var dashboardCredentialsJS string

//go:embed dashboard_workbench.js
var dashboardWorkbenchJS string

//go:embed dashboard_workbench.css
var dashboardWorkbenchCSS string

//go:embed dashboard_atlas.css
var dashboardAtlasCSS string

func businessDashboardHTML(html, nonce string) string {
	html = strings.NewReplacer(
		"NexusGlobal MAMR 管理后台", "NexusGlobal 支付管理中心",
		"NexusGlobal MAMR</span>", "NexusGlobal<span class=\"brand-caption\">支付管理中心</span></span>",
		"通用适配器", "支付机构与接入",
		"法人实体", "经营主体",
		"资产桥配置", "币种兑换",
		"钱包白名单", "收款钱包",
		"风控运维", "风险与待办",
		"管道隔离", "业务风险隔离",
		"收单通道Tier", "收款通道",
		"发卡卡池Tier", "虚拟卡管理",
		"银行/VBA", "银行账户与入账",
		"商户入网", "商户管理",
		"审批inbox", "审批中心",
		"收单通道 Tier 管理", "收款通道",
		"新增收单通道", "新增收款通道",
		"发卡卡池 Tier 管理", "虚拟卡管理",
		"审批 Inbox（Maker-Checker）", "审批中心",
		"VA 账号池管理", "银行收款账户",
		"录入 VA 账号", "新增银行收款账户",
		"收款意图监控", "待收款订单",
		"适配器提供商", "支付服务机构",
		"适配器通道", "支付接入通道",
		"新增实体", "新增经营主体",
		"新增桥接配置", "新增兑换规则",
		"资产桥", "币种兑换",
		"费用摊销", "费用明细",
		"Provider Tier", "通道类别",
		"Provider Code", "支付机构代码",
		"Channel Code", "通道代码",
		"Credential Vault Key", "安全凭证引用",
		"Strategy Mode", "处理规则",
		"Funding Mode", "资金来源",
		"Issuing Tier", "发卡方式",
		"Pool Code", "卡池代码",
		"Adapter Type", "接入方式",
		"Adapter Provider", "发卡服务机构",
		"Base URL (HTTPS)", "机构接口地址",
		"Provider Health", "机构连接状态",
		"Webhook Secret Vault Key", "通知签名凭证引用",
		"Webhook URL", "支付结果通知地址",
		"MAE Config ID", "收款路由配置",
		"Crypto Pool", "加密资产资金池",
		"TIER_2_COMPLIANT + DEFAULT<br>资金路径可选", "常规业务收款<br>可选择资金来源",
		"TIER_1_HIGH_RISK + BURN_MODE<br>CRYPTO_DESENSITIZED 锁定", "需要额外风控的业务<br>固定使用隔离资金池",
		"高危模板已锁定：Tier=HIGH_RISK, Strategy=BURN_MODE, Funding=CRYPTO_DESENSITIZED。", "此模板使用固定的通道类别、隔离处理规则和资金来源。",
		"高危通道强制 CRYPTO_DESENSITIZED，禁止 FIAT_DIRECT。", "此类通道仅使用隔离资金池，不能直接使用银行资金充值。",
		"从 CARD_ISSUER Provider 列表加载", "仅显示支持发卡业务的支付机构",
		"NEXUS_ISSUING_HTTP_V1 必填", "选择机构接口接入时必填",
		"适配器类型", "业务类型",
		"<label>租户 ID</label>", "<label>所属商户</label>",
		"<label>商户ID</label>", "<label>商户</label>",
		"<label>实体ID</label>", "<label>经营主体</label>",
		"<label>银行账户ID</label>", "<label>结算银行账户</label>",
		"<label>银行账户 ID</label>", "<label>结算银行账户</label>",
		"<label>通道 ID</label>", "<label>收款通道</label>",
		"<label>司法辖区</label>", "<label>注册地区</label>",
		"<label>IBAN</label>", "<label>国际银行账号（欧洲等地区使用）</label>",
		"<label>SWIFT</label>", "<label>银行国际识别码</label>",
		"<th>ID</th>", "<th>记录编号</th>",
		"<th>实体ID</th>", "<th>经营主体</th>",
		"<th>商户ID</th>", "<th>商户</th>",
		"<th>租户ID</th>", "<th>所属商户</th>",
		"<th>通道ID</th>", "<th>收款通道</th>",
		"<th>订单ID</th>", "<th>订单编号</th>",
		"<th>意图ID</th>", "<th>收款编号</th>",
		"<th>银行账户ID</th>", "<th>结算银行账户</th>",
		"<label>版本锁</label>", "<label>配置版本</label>",
		"<th>版本锁</th>", "<th>配置版本</th>",
		"<th>Outbox</th>", "<th>同步进度</th>",
		"<label>目标ID</label>", "<label>关联记录</label>",
		"<label>Request ID</label>", "<label>操作追踪编号</label>",
		"已验证 Payload（变更后）", "变更内容（已校验）",
		"Before → After 对比", "变更前后对比",
		"Outbox 事件", "后台同步任务",
		"Outbox 状态", "同步状态",
		"(bps)", "（万分比）",
		"输入租户 ID", "请选择所属商户",
		"租户 UUID（后端必填校验）", "请选择所属商户",
		"输入实体ID（可选）", "请选择经营主体（可选）",
		"请选择商户（需有ACTIVE路由策略）", "请选择已配置收款规则的商户",
		"仅接受 vault:// 引用，禁止明文密钥", "填写已保存的安全凭证引用，请勿粘贴密钥原文。",
	).Replace(html)
	html = strings.Replace(html, "</style>", dashboardBusinessCSS+"\n"+dashboardWorkbenchCSS+"\n"+dashboardAtlasCSS+"\n</style>", 1)
	return strings.Replace(html, "</body>", "<script nonce=\""+nonce+"\">\n"+dashboardBusinessJS+"\n"+dashboardOperationsJS+"\n"+dashboardCredentialsJS+"\n"+dashboardWorkbenchJS+"\n</script>\n</body>", 1)
}
