# ADR-0006:bet-svc 群体 Closed Pool 数学与结算(M6)

- **状态**:Accepted(M6 实施)
- **日期**:2026-07-05
- **前置**:M2(JWT)、M3(wallet)、M4(room)

## 上下文

demo 的核心闭环是"房间内群体 Pool → 下注 → 结算"。需要一个 bet-svc:创建 Pool、成员下注
(扣 wallet)、结算(给赢家发钱),钱不能凭空多或少。

## 决策

### 1. Closed Pool = **parimutuel(彩池制)**,盘口赔率仅指示性

- 一个 Pool 归属一个房间(room_id),两个及以上选项(M6 固定两项足够 demo 闭环)。
- 结算:输家彩池扣 **rake**(默认 5%,`AURORA_BET_RAKE_BPS`)后,按赢家注额**按比例分配**;
  赢家拿回本金 + 分成。整数 minor units,除法向下取整,**尘差归庄**(mint 吸收)。
- 边界:无人押中 → **全员退款**;无输家 → 赢家仅退本金。
- 选项上的 `odds_x100` 只是**指示性显示**(AI 建议的市场赔率),不参与结算数学。

### 2. 钱走 wallet-svc HTTP(幂等键闭环)

- PlaceBet:先 `wallet.Debit(idempotency_key=客户端键)`,成功后插 bets 行(UNIQUE key 兜底重放)。
  崩溃重试同键 → wallet 重放不双扣、bets 插入继续。余额不足 → 422 透传。
- 插入用 `INSERT ... WHERE EXISTS(pool OPEN)` 原子守卫;若 Pool 已关(结算竞态)→
  立即 `wallet.Credit("void-"+key)` 退款并返回 409。
- **退款恢复语义(评审加固)**:
  - void 退款**不是** fire-and-forget:Credit 失败 → 502 `refund_pending`,同键重试补完退款,
    没退成功前**不告知**"已退款"。
  - 对**已关 Pool** 且无 bet 行的同键请求走恢复路径:重放幂等 Debit(真重放不动钱;新键取了立退)
    → `void-<key>` Credit。这样"Debit 成功但插入前崩溃、Pool 又被结算"的孤儿扣款**必然被退回**,
    不再依赖 Pool 仍 OPEN。
  - wallet 重放校验**全部参数**(user/amount/type,不匹配 → 409 `idempotency_conflict`),
    杜绝"扣款后未插入窗口"里同键换参数蒙混。
  - 彩池聚合(rake / 总额 / 分成)全程 **big.Int**:仅按单注 1e15 上限约束时,注数不设限,
    int64 的 `loserPool×rakeBps` 乘积会溢出成负 rake 而**凭空铸币** —— 已修复 + 回归测试。
- 结算三段式:①`OPEN→SETTLING`(带 winning_option 守卫,可幂等重入)②按 `settle-<bet_id>` /
  `refund-<bet_id>` 幂等键逐笔 Credit ③落 payout + `SETTLED`。任一步崩溃,重跑 Settle 安全
  (wallet 幂等重放兜底)。

### 3. 授权与成员制

- 全部 RPC(除健康检查)Bearer JWT(与 identity 共享 HS256,校验 `iss`;第三份拷贝,债务同 ADR-0005)。
- room_id 非空时,CreatePool/PlaceBet **转发调用者 token** 调 room-svc `ListMembers` 验成员
  (403 即非成员;room 不可达 → 503)。SettlePool 仅 Pool 创建者(结果判定 oracle 留 M9/M13)。

### 4. 事件

- `bet.placed.v1`(schema 已有前向契约)+ 新增 `bet.pool.settled.v1`,topic `aurora.bet.lifecycle.v1`。
- M6 **直发** Kafka(同 identity 模式,失败仅告警);bet 侧 outbox 迁移留后续 —— 钱的权威事件
  已由 wallet outbox 保证,bet 事件仅供投影/审计。

## 后果 / 债务(登记 CLAUDE.md)

- 结算由创建者手动触发(无赛果 oracle,M9/M13);SETTLING 中途崩溃需重调 Settle 恢复。
- bet 事件直发可丢(下游以 wallet 事件为准);两项固定选项;无撤单;JWT 第三份拷贝。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| 固定赔率对庄 | 需要庄家风控与流动性管理,超 M6 范围;parimutuel 自平衡 |
| 结算与 wallet 同库同事务 | 跨服务禁止共库(仓库红线);幂等键补偿已足够 |
| bet 也上 outbox | 钱的权威事件在 wallet;M6 控范围,债务显式登记 |
