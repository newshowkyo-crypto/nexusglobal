# CODING_STANDARDS — Go 编码规范

> 版本：V1.0
> 用途：统一 AI 产出代码风格，保证一致、可审查、资金安全。
> 优先级：违反 CONSTRAINTS.md 红线的代码一律不合规，本规范在红线之下补充风格与工程细节。

---

## 1. 项目与包

1. module 名：`github.com/nexusglobal/mamr`（go.mod）。
2. 包名小写、单数、无下划线：`mae`、`ledger`、`payout`、`money`。
3. 目录即包，遵循 TDD §2 结构，不新增顶层目录。
4. `internal/<module>/domain` **零 I/O**：不 import `pgx`、`redis`、`net/http`、任何 external client。
5. 跨模块只能 `service → 另一模块 service 接口`，禁止 import 别的模块的 `repository`/`domain` 内部类型做持久化。

---

## 2. 命名

| 对象 | 规则 | 示例 |
| --- | --- | --- |
| 导出类型/函数 | 大驼峰 | `PayoutService`、`FreezeBalance` |
| 非导出 | 小驼峰 | `recalcAvailable` |
| 接口 | 能力名 + er，或领域 `Client`/`Repository` | `Repository`、`lp.Client` |
| 常量枚举 | 类型 + 值，字符串与 DB 一致 | `MAEStatusActive = "Active"` |
| 错误变量 | `Err` 前缀 | `ErrInsufficientBalance` |
| 测试 | `Test<Type>_<Method>_<场景>` | `TestPayoutService_Freeze_InsufficientBalance` |
| 文件 | 小写下划线 | `payout_service.go`、`mae_state_machine.go` |

- 缩写全大写：`maeID`→ 写 `maeID`（变量）/ `MAEID`（导出字段），HTTP/URL/ID/LP 保持惯例大写。
- 避免 stutter：`mae.MAEConfiguration` 可接受（领域明确），但 `mae.MAEService` 优先写 `mae.Service`。

---

## 3. 金额处理（最关键，违反即资金事故）

1. **全局禁用 float 参与任何金额**。`float32/float64` 不得出现在金额计算、存储、传输。
2. 两套金额类型，泾渭分明：
   - **minor unit 计数（MAE 指标、Redis 计数器、vmax）**：`int64`。封装 `platform/money.Minor`。
   - **账本余额（decimal(20,4)）**：`github.com/shopspring/decimal.Decimal`。
3. JSON 进出一律**字符串**：

```go
type PayoutRequestDTO struct {
    Amount       string `json:"amount"`        // "1000.0000"
    FiatCurrency string `json:"fiat_currency"`
}
// 入口解析
amt, err := decimal.NewFromString(dto.Amount)
if err != nil { return apperr.Invalid("amount", err) }
```

4. decimal 统一精度：落库前 `amt.Round(4)`；比例计算 `amt.Mul(decimal.NewFromInt(bps)).Div(decimal.NewFromInt(10000))`。
5. minor unit 比例：`amount * bps / 10000` 用整数运算，避免溢出时先校验范围。
6. **禁止** `decimal` 与 `float64` 互转参与金额；`decimal.NewFromFloat` 在金额路径禁用（只允许从 string/int 构造）。

❌：`total := price * 1.05` ✅：`total := price.Mul(decimal.NewFromFloat(1.05))` 也不行 → ✅ `total := price.Mul(decimal.RequireFromString("1.05"))`。

---

## 4. 错误处理

1. **error wrapping**：用 `fmt.Errorf("...: %w", err)` 保留链；上层用 `errors.Is/As` 判定。

```go
if err := repo.Freeze(ctx, tx, id); err != nil {
    return fmt.Errorf("freeze balance merchant=%s: %w", merchantID, err)
}
```

2. **哨兵错误**集中在各包 `errors.go` 或 `platform/apperr`：

```go
var (
    ErrInsufficientBalance = apperr.New("INSUFFICIENT_BALANCE", 422)
    ErrIllegalTransition   = apperr.New("ILLEGAL_STATE_TRANSITION", 409)
    ErrVersionConflict     = apperr.New("VERSION_CONFLICT", 409)
)
```

3. handler 统一把 error 映射成 API.md §3 的错误码（用 `apperr.AsAPIError`）。
4. **禁止静默吞错**：资金路径不得 `_ = err`、不得 `if err != nil {}` 空体。非资金的可忽略错误也要注释说明。
5. panic 仅用于不可恢复的程序 bug；`httpx` 有 recover 中间件兜底转 500。
6. 外部调用错误必须区分 `ErrTimeout` 与 `ErrExplicitFailure`（CONSTRAINTS RED-03）。

---

## 5. Context 传递

1. 所有 I/O 函数第一参数 `ctx context.Context`。禁止 `context.TODO()`（除 main 顶层）。
2. 禁止把 ctx 存进 struct 字段。
3. 外部调用设超时：`ctx, cancel := context.WithTimeout(ctx, cfg.LPTimeout); defer cancel()`。
4. ctx 透传 `request_id`、`tenant_id`（用 typed key，不用裸 string key）。
5. DB 事务的 ctx 不得跨越外部调用（呼应 RED-01）。

---

## 6. 事务边界（service 层唯一可开事务处）

1. 事务只在 `service` 层开启，通过 `platform/postgres.TxManager`：

```go
func (s *Service) Freeze(ctx context.Context, cmd FreezeCmd) (Result, error) {
    var res Result
    err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
        bal, err := s.repo.LockBalanceForUpdate(ctx, tx, cmd.MerchantID, cmd.Currency) // FOR UPDATE
        if err != nil { return err }
        if bal.RecalcAvailable().LessThan(cmd.Amount) { return ErrInsufficientBalance }
        if err := s.repo.ApplyFreeze(ctx, tx, ...); err != nil { return err }
        if err := s.repo.InsertLedgerTxn(ctx, tx, ...); err != nil { return err }
        if err := s.repo.InsertOutbox(ctx, tx, ...); err != nil { return err }
        res = ...
        return nil
    })
    return res, err
}
```

2. `WithinTx` 回调返回 error 即自动 Rollback，nil 即 Commit。回调内**只做 DB 操作**。
3. 事务回调内**严禁**：外部 client 调用、HTTP、阻塞 Redis 业务调用、`time.Sleep`、长循环（RED-01��。
4. repository 方法接收 `tx pgx.Tx`（事务内）或 `pool`（只读查询），不自行开事务。
5. 乐观锁更新统一 `WHERE id=$ AND version=$old`，0 行 → 返回 `ErrVersionConflict`。

---

## 7. Repository 与 SQL

1. SQL 写在 `internal/<module>/queries.sql`，`sqlc generate` 生成类型安全代码。
2. 复杂资金 SQL（`FOR UPDATE`、`SKIP LOCKED`、`ON CONFLICT`）必须显式、有注释说明意图。
3. 所有幂等键列建 UNIQUE 索引；插入用 `ON CONFLICT (idempotency_key) DO NOTHING`/`DO UPDATE` 并据 rows affected 判断重放。
4. 禁止字符串拼接 SQL（防注入）；一律参数化 `$1,$2`。
5. 金额列 `numeric(20,4)` ↔ `decimal.Decimal`；计数 `bigint` ↔ `int64`。

---

## 8. 日志（log/slog 结构化）

1. 用 `platform/logger` 初始化的 `*slog.Logger`，JSON handler。
2. 结构化字段，禁止 `fmt.Sprintf` 拼大字符串：

```go
log.Info("payout frozen",
    "payout_id", id, "merchant_id", merchantID,
    "amount", amount.String(), "currency", cur)
```

3. **绝不记录**：明文密钥、明文地址、API Key、原始 payload、PII。地址用 masked，密钥用 `secret_version`。
4. 日志级别：`Debug` 开发细节、`Info` 业务里程碑、`Warn` 可恢复异常、`Error` 需关注失败。
5. 每条请求日志带 `request_id`；每条资金动作日志带 `idempotency_key`。
6. 错误日志记 `err`（wrap 后），不记敏感上下文。

---

## 9. 依赖注入（手工构造函数注入，无框架）

1. 每个 service：`func New(repo Repository, ext lp.Client, clk clock.Clock, log *slog.Logger) *Service`。
2. 依赖以**接口**注入，便于 mock（`Repository`、`lp.Client`、`clock.Clock`）。
3. 装配集中在 `cmd/api/main.go` / `cmd/worker/main.go`，显式 new，自上而下传递。
4. 不用全局变量持有依赖（除 `slog.Default` 兜底）；不用 `init()` 做有副作用初始化。
5. 时间通过 `clock.Clock` 接口取（`clk.Now()`），测试注入固定时钟；**禁止业务代码直接 `time.Now()`**（仅 clock 实现内可用）。

---

## 10. 并发

1. 优先用 DB 行级锁 + 唯一约束保证资金正确性（RED-05/14），而非内存锁。
2. Worker 并发用 `errgroup` 或固定 worker pool，受 `OUTBOX_WORKER_CONCURRENCY` 控制。
3. 共享可变状态用 `sync.Mutex` 保护或改为不可变传递；`go vet`/`-race` 必须干净。
4. channel 关闭遵循"发送方关闭"；context 取消优先于自定义停止信号。

---

## 11. 测试

1. 框架：`testify`（require 用于前置断言，assert 用于结果断言）。
2. 命名：`Test<Type>_<Method>_<场景>`；表驱动用 `tests := []struct{name string;...}`。
3. 分层覆盖：
   - `domain`：纯单测，`-short` 可跑，**覆盖率 ≥85%**，状态机全迁移 + 金额规则 + 地址校验全分支。
   - `service`：testcontainers 起真 PG/Redis，验事务/乐观锁/幂等/并发。
   - `handler`：`httptest`，验状态码/错误码/幂等头/鉴权。
   - 端到端：`test/integration`。
4. 集成测试用 `testing.Short()` 守卫：`if testing.Short() { t.Skip("integration") }`，使 `make test-short` 快速回路。
5. **必备并发测试**：并发 5 笔相同余额 Payout，断言无超卖、无负余额（PRD §12.4、RED-05）。
6. Mock：external client 用 `pkg/external/*/mock.go` 的确定性 Mock；可注入 success/explicit_failure/timeout。
7. 每个 bug 修复前先写复现测试（红→绿），见 AGENT_WORKFLOW。

---

## 12. 格式与静态检查

1. 提交前必过：`gofmt -l .`（无输出）、`go vet ./...`、`go build ./...`、`go test ./... -short -race`。
2. import 分组：标准库 / 第三方 / 本项目，三段空行分隔。
3. 行宽不强制，但 SQL/长表达式合理换行。
4. 不留 `TODO` 无主；如必须，写 `// TODO(taskID): ...` 并在 MEMORY 登记。
5. 不留被注释掉的死代码；删除即删除（呼应"避免兼容性 hack"）。

---

## 13. 安全编码

1. 敏感字段 struct tag `json:"-"`，入库前 `crypto.Encrypt`，展示用 masked/hash。
2. 所有外部输入在 handler 边界校验（地址、金额、枚举值）；内部模块间信任。
3. SQL 参数化、URL 校验、避免命令注入。
4. 错误信息不回传内部细节给外部（包装成标准错误码）。

---

## 14. 提交规范

1. 一个原子任务（TASKS 的 T0xx）对应一组提交；提交信息：`T0xx: <简述>`。
2. 提交前更新 `PROGRESS.md`、`MEMORY.md`（AGENT_WORKFLOW 规定）。
3. commit 必须能通过 `make ci`；不通过不提交。

---

## 15. 速查清单（写完即对）

```text
[ ] 金额无 float；minor=int64，账本=decimal；JSON 用 string
[ ] error 全 %w wrap，无静默吞错，外部超时/失败区分
[ ] ctx 首参数，外部调用有超时，事务 ctx 不跨外呼
[ ] 事务只在 service，回调内纯 DB，乐观锁 version
[ ] SQL 参数化，幂等键 UNIQUE，FOR UPDATE/SKIP LOCKED 有注释
[ ] 日志结构化，无明文敏感信息
[ ] 依赖接口注入，时间走 clock，无业务 time.Now()
[ ] 测试命名规范，domain≥85%，含并发双花测试
[ ] gofmt/vet/build/test-short -race 全过
```
