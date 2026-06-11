# AGENT_WORKFLOW — AI 循环开发流程（自驱动闭环）

> 版本：V1.0
> 读者：Codex 类自主开发 Agent。
> 目标：让 AI 在**不丢上下文**的前提下，自己 选任务 → 写测试 → 写代码 → 构建 → 找 bug → 修复 → 继续，直到全部任务完成。
> 强约束闭环：每轮**强制**先读 `MEMORY.md` + `CONSTRAINTS.md` + 本文件，再开工。

---

## 0. 第一性原则（任何时候都成立）

1. **资金安全 > 功能完成 > 速度**。任何与 `CONSTRAINTS.md` 红线冲突的做法一律不做。
2. **小步闭环**：一次只做一个任务（TASKS 的一个 T0xx），做完即验证即提交，不囤积。
3. **测试先行**：先写测试（红），再写实现（绿），最后重构。
4. **上下文落盘**：决策、踩坑、进度指针写进 `MEMORY.md`，不靠记忆；换会话/被压缩也能续传。
5. **不臆造**：不清楚就读文档/读代码，不编造库行为；新依赖先登记 MEMORY。

---

## 1. 每轮标准闭环（THE LOOP）

```
┌─────────────────────────────────────────────────────────────┐
│ STEP 0  READ      读 MEMORY.md → CONSTRAINTS.md → 本文件         │
│ STEP 1  SELECT    从 PROGRESS.md 选下一个「未阻塞」最低编号任务      │
│ STEP 2  PLAN      读 TASKS.md 该任务 + 关联 TDD/API 段落，列子步骤  │
│ STEP 3  TEST      先写失败测试（覆盖 DoD 与红线场景）              │
│ STEP 4  CODE      写最小实现让测试通过，遵守 CODING_STANDARDS      │
│ STEP 5  BUILD     make build                                   │
│ STEP 6  VERIFY    make test-short（涉 DB/Redis 跑 make test）   │
│ STEP 7  SELF-FIX  失败→定位→修复→回 STEP 5（按 §3 卡住协议）       │
│ STEP 8  REDLINE   跑 CONSTRAINTS §17 自查 / make redlines       │
│ STEP 9  RECORD    更新 PROGRESS.md + MEMORY.md                  │
│ STEP 10 COMMIT    git commit "T0xx: ..."（make ci 通过才提交）   │
│ STEP 11 NEXT      回 STEP 0 选下一个任务                         │
└─────────────────────────────────────────────────────────────┘
```

**任何一步失败都不得跳过进入下一任务。** 全部任务完成 → 执行 T038 收尾 → 停止并输出总结。

---

## 2. 各步骤细则

### STEP 0 READ（强制，不可省）
- 完整读 `MEMORY.md`：当前进度指针、已定架构决策、已踩坑与解法、未决问题。
- 扫 `CONSTRAINTS.md` 红线标题（RED-01..15 + NF），明确本轮任务相关红线。
- 若 MEMORY 的"进度指针"与 PROGRESS 不一致，以 PROGRESS 看板为准并在 MEMORY 修正。

### STEP 1 SELECT
- 在 `PROGRESS.md` 找状态=待办、且依赖全部=已完成的**最低编号**任务，置为「进行中」。
- 同时只允许一个任务「进行中」。

### STEP 2 PLAN
- 读 `TASKS.md` 对应 T0xx 的 依赖/涉及文件/DoD/测试/红线核对。
- 读相关 `TDD.md`（结构/伪代码）、`API.md`（契约）、`CODING_STANDARDS.md`（风格）段落。
- 在 MEMORY 临时区写 3–6 条子步骤清单。

### STEP 3 TEST（测试先行）
- 按 DoD 写测试：成功路径 + 错误码 + 边界 + **红线场景**（如并发双花、超时不释放、exposure 不递减）。
- domain 任务用 `-short` 表驱动；service 任务用 testcontainers。
- 先运行确认**测试失败**（红），证明测试有效。

### STEP 4 CODE
- 写**最小**实现通过测试。不过度设计、不加未要求的抽象（见全局工程纪律）。
- 严格遵守事务边界、金额类型、错误 wrap、依赖注入规范。

### STEP 5 BUILD
- `make build`。编译错误优先修，直到通过。

### STEP 6 VERIFY
- 默认 `make test-short`（快）。涉及 DB/Redis/集成 → `make up` 后 `make test`。
- `-race` 必须干净（并发任务尤其）。

### STEP 7 SELF-FIX（自查修复）
- 读失败输出 → 定位根因（不是改测试迁就 bug）→ 最小修复 → 回 STEP 5。
- 修 bug 前若无复现测试，先补一个复现测试（红→绿）。
- **禁止**：删测试、`t.Skip` 绕过、`--no-verify`、注释掉断言、降低覆盖率来"通过"。

### STEP 8 REDLINE
- 跑 `make redlines`（CONSTRAINTS §17 grep 脚本）+ 人工核对本任务"红线核对"项。
- 命中且非误报 → 回 STEP 4 修复。

### STEP 9 RECORD
- `PROGRESS.md`：任务置「已完成」，记完成时间与提交号。
- `MEMORY.md`：更新进度指针=下一个任务；追加本轮新架构决策/踩坑解法/未决问题。

### STEP 10 COMMIT
- `make ci` 全过（build+lint+test-short）。
- `git add -A && git commit -m "T0xx: <简述>"`。
- 若用户未要求 push，则不 push。

### STEP 11 NEXT
- 回 STEP 0。

---

## 3. 卡住协议（Stuck Protocol）

> 「卡住」= 同一任务的 STEP 5/6/7 连续失败。按尝试次数升级处理：

| 尝试次数 | 动作 |
| --- | --- |
| 第 1–2 次 | 读完整错误栈，定位根因，最小修复，重试。 |
| 第 3 次 | 退一步：重读该任务 TDD/API/CONSTRAINTS 相关段，检查是否方向错误；在 MEMORY「踩坑」记现象与已试方案。 |
| 第 4 次 | 缩小范围：把任务拆成更小步（先让 domain 单测过，再 service），逐块验证；用最小复现隔离问题。 |
| 第 5 次 | 检查环境：`make down && make up && make migrate`；确认依赖版本、sqlc 是否重新生成、`.env` 是否正确。 |
| ≥6 次 | **停止该任务**：在 PROGRESS 置「受阻」，在 MEMORY「未决问题」详写：现象、错误、已试 N 种方案、怀疑原因、需要的决策；跳到下一个**无依赖关系**的任务继续，避免空转。受阻任务留待复盘或用户介入。 |

**绝不允许**：为了"过测试"而删除/弱化测试、绕过红线、提交不能编译或测试不过的代码。宁可标「受阻」也不交假绿。

---

## 4. 构建 / 测试命令清单（背下来）

| 目的 | 命令 |
| --- | --- |
| 起依赖（PG+Redis） | `make up` |
| 停依赖并清卷 | `make down` |
| 迁移 | `make migrate` |
| 生成 sqlc（改了 queries.sql 后） | `make sqlc` |
| 编译 | `make build` |
| 快速单测（开发循环默认） | `make test-short` |
| 全量测试（含集成，需 up） | `make test` |
| 静态检查 | `make lint`（gofmt + go vet） |
| 红线自查 | `make redlines` |
| 提交前总检 | `make ci`（build+lint+test-short） |
| 跑 API | `make run-api` |
| 跑 Worker | `make run-worker` |

> 改了 `migrations/*` → 重跑 `make migrate`（必要时 `make down && make up && make migrate`）。
> 改了 `queries.sql` → 必跑 `make sqlc` 再 `make build`。

---

## 5. 自检 Checklist（每次提交前逐条）

```text
功能
[ ] 任务 DoD 全部满足（逐条对 TASKS.md）
[ ] 接口符合 API.md 契约（handler 任务）
[ ] 状态机非法迁移被拒并有测试

红线（CONSTRAINTS §17 全条）
[ ] 事务内无外部 I/O；外呼只在 worker
[ ] 超时→UnknownExternalState，不盲重试/误释放
[ ] Payout 先冻结(FOR UPDATE)后外呼；并发无超卖
[ ] exposure 只增；Cooldown 不直达 Active；当日锁生效
[ ] webhook handler 不连账务，只投 Streams；重复不重账
[ ] Sweep 有窗口判断；受益人四分支；非窗口不调银行 API
[ ] 余额变更必有成对借贷分录；敏感字段加密+json:"-"
[ ] 金额无 float

工程
[ ] gofmt/vet 干净；make build 通过
[ ] make test-short 通过（涉 DB 跑 make test）；-race 干净
[ ] domain 覆盖率 ≥85%
[ ] 无死代码/无主 TODO/无被注释代码块

记录
[ ] PROGRESS.md 更新
[ ] MEMORY.md 更新（指针+决策+坑）
[ ] commit 信息规范 T0xx
```

---

## 6. 上下文保护规则（防丢失）

1. **每轮开头读 MEMORY，每轮结尾写 MEMORY**——这是续传命脉。
2. MEMORY 写"为什么"，不只"做了什么"：架构取舍、坑的根因与解法、约定。
3. 工具结果/长输出里的关键结论，立刻摘抄进 MEMORY（原始输出可能被清理）。
4. 进度指针永远指向"下一个要做的任务"，保证任何时候 STEP 1 可恢复。
5. 不在脑内保存跨轮状态；一切跨轮信息落 PROGRESS/MEMORY。

---

## 7. 完成判定与收尾

- 当 PROGRESS 中 T001–T037 全「已完成」→ 执行 T038（文档校验 + `make ci && make redlines` + 对照 PRD 第 12 章逐条验收）。
- 全绿后：在 MEMORY 写「PROJECT COMPLETE」，输出总结（已实现模块、测试结果、遗留未决问题）。
- 若有「受阻」任务，不算完成：在总结中列出受阻清单与所需决策，交回用户。

---

## 8. 红线-工作流-记忆 三者闭环图

```
        ┌──────────────┐
        │ CONSTRAINTS  │  红线（不可违反）
        └──────┬───────┘
               │ 每轮 READ / 每次提交 SELF-CHECK
               ▼
   ┌──────────────────────┐
   │   AGENT_WORKFLOW      │  闭环（怎么做）
   │  READ→TEST→CODE→...   │
   └──────┬────────────┬──┘
          │ 写          │ 读
          ▼            ▲
        ┌──────────────┴┐
        │    MEMORY      │  上下文（不丢失）
        └────────────────┘
```

> 三份文件每轮强制联动：先用 MEMORY 恢复状态、用 CONSTRAINTS 划红线，再按 WORKFLOW 推进，最后把结果写回 MEMORY。任何一环缺失都视为流程违规。
