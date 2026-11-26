# Atlas 公网演示环境

- `scripts/Caddyfile.demo` 只转发后台登录、Atlas 查询、CSV 和审批查询；不转发 Bearer 凭证、业务 API 或业务写请求。Go API、PostgreSQL 和 Redis 仍只监听回环地址。
- 部署机用 `NEXUS_PUBLIC_HOST` 指定公网 IP。云防火墙放行 `80/tcp`（证书签发和续期、HTTPS 跳转）及 `443/tcp`（HTTPS）。IP 证书使用 ACME `shortlived` 配置，由 Caddy 自动续期。
- 在服务器私有 `.env` 中配置 `ADMIN_DEMO_USERNAME`、`ADMIN_DEMO_PASSWORD_HASH`（bcrypt，成本 10–12）、`ADMIN_DEMO_TENANT_ID` 和 `ADMIN_SESSION_TTL=12h`，更新后重启应用。账号固定为指定租户的只读角色。密码保存到密码管理器，不把明文或哈希提交到 Git。停用账号时清除以上三项账号配置、轮换 `ADMIN_SESSION_SECRET` 并撤下公网反代。
- 绑定域名时，先将域名的 A 记录指向 VPS，随后替换 `NEXUS_PUBLIC_HOST` 并验证 Caddy 配置和证书。切换主机名后需登录一次，原 IP 的 Cookie 不迁移。
- 此方案只适用于隔离的合成数据和 Mock 支付环境，不用于真实交易。真实生产需单独配置实际支付通道、KMS 和正式管理员身份认证。
