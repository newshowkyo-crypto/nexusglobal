# NexusGlobal 3.0 回滚点说明

创建时间：2026-06-16  
回滚点名称：preprod100-checkpoint-2026-06-16  
创建前分支：master  
创建前 HEAD：1e44829  
Checkpoint 分支：codex/preprod100-checkpoint-20260616  
Checkpoint commit：dab05b5  
Checkpoint tag：preprod100-checkpoint-2026-06-16  

## 一、回滚点目的

本回滚点用于在后续 staging 凭证联调、真实 provider 接入、生产门禁强化或 Dashboard 继续改造时，能够回到当前“mock/local + pre-production 框架验收通过”的状态。

当前状态的准确口径：

```text
mock/local 100% 通过。
pre-production 框架层面 100% 可验收。
真实 Stripe/Nxtapay/VCC sandbox 因缺少凭证标记为 SKIPPED_MISSING_CREDENTIAL。
```

## 二、当前已验证门禁

```text
gofmt -l .
go build ./...
go vet ./...
go test ./internal/platform/config -count=1 -v
go test ./internal/platform/httpx -count=1 -v
go test ./internal/admin -run "TestV3(Channels|IssuingPools)" -count=1 -v
go test ./internal/acquirer/... -count=1
go test ./internal/issuing/... -count=1
go test ./internal/treasury/... -count=1
go test ./internal/worker/vcc_funding/... -count=1
go test ./test/integration -count=1 -v
go test ./... -count=1 -p 1
```

## 三、回滚方式

如果需要回到本 checkpoint，可使用：

```bash
git log --oneline --decorate -5
git reset --hard preprod100-checkpoint-2026-06-16
```

如果只需要查看当前文档基线：

```bash
git show preprod100-checkpoint-2026-06-16:PROJECT_PROGRESS_AND_FUNCTIONS_2026-06-16.md
git show preprod100-checkpoint-2026-06-16:PROJECT_CORE_LOGIC_2026-06-16.md
git show preprod100-checkpoint-2026-06-16:PROJECT_PANORAMA_TOPOLOGY_2026-06-16.md
```

注意：`git reset --hard` 会丢弃目标提交之后的工作区改动。执行前必须确认没有需要保留的新改动。

## 四、后续变更前建议

每次接入真实 provider 或修改资金状态机前，先执行：

```bash
git status --short
go test ./... -count=1 -p 1
```

再创建新的 checkpoint：

```bash
git add <changed_files>
git commit -m "checkpoint: describe verified state"
```

## 五、不得回滚丢失的数据

生产或 staging 数据库回滚必须区分代码回滚和数据迁移回滚：

- 代码回滚可以使用 Git commit。
- 数据库回滚必须按 migration down/up 演练结果执行。
- 涉及资金、ledger、审计、RFI、webhook 的历史数据不得硬删除。
- 任何数据修复必须写审计记录。
