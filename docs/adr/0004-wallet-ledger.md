# ADR-0004:wallet-svc 账本、幂等与 Transactional Outbox(M3)

- **状态**:Accepted(M3 实施)
- **日期**:2026-06-30
- **覆盖 milestone**:M3
- **前置**:M1(Event Mesh)、M2(identity)

## 上下文

需要一个**虚拟币**钱包(非真钱),支撑后续 bet(M6)。核心要求:**钱不能凭空多或少**、
**重试不能双扣**、**事件不能丢**。M1 的事件投递是弱保证(Kafka 挂则丢),M3 必须补上。

## 决策

### 1. 双记账本(double-entry),整数 minor units

- 每个用户一个 `USER` 账户;每币种一个 `SYSTEM` `mint` 账户。
- 每笔交易写**两条** `ledger_entries`(USER ±N、SYSTEM ∓N),金额和恒为 0 —— 账本自洽、可审计。
- `accounts.balance_minor` 物化余额,**与分录在同一事务**更新,读余额 O(1)。
- 金额一律 **整数 minor units**(`BIGINT`),**绝不用浮点**表示钱。
- 单币种 `AUC`(Aurora Coin),虚拟币;真钱与 Pragmatic 留 **M9**。

### 2. 幂等(idempotency)

- `Credit`/`Debit` **必填 `idempotency_key`**;`transactions.idempotency_key` 唯一。
- 事务内顺序:`BEGIN` → 对 USER 账户 `SELECT ... FOR UPDATE`(按钱包串行化)→ 查幂等键:
  - **命中**(且 user 一致)→ 返回原交易结果(**重放**,不再记账)。
  - 命中但 user 不一致 → `idempotency_key` 冲突(409)。
  - 未命中 → 校验 + 记账 + 写 outbox → `COMMIT`。
- 先锁账户再查幂等键,使"查-插"对同钱包并发**原子**;UNIQUE 约束兜底跨钱包键复用。
- **余额不足**的 Debit:返回 `insufficient_funds`,**不写 transactions 行**(不占用 key,允许日后充值再试)。
  > 取舍:幂等键只保护"已成功的写"不被重复应用;失败的业务校验不消费 key。已文档化。

### 3. Transactional Outbox(本 ADR 的重点,补 M1)

- 记账事务**同时**向 `outbox` 表插入事件行(topic + key + JSON payload),与账本**同一事务提交**。
- 独立 **relay 协程**轮询 `outbox WHERE published_at=0`,发到 Kafka,成功后标记 `published_at`。
- 保证:**至少一次**投递(事件与账本一起持久化,Kafka 抖动只是延迟,不丢)。
- 重复投递可能(relay 发了但标记前崩溃)→ 下游需按 `event_id`/`transaction_id` 幂等消费(已在 payload 里给）。

### 4. 第一个事件消费者:开钱包

- wallet-svc 跑一个 Kafka **consumer group**,订阅 `aurora.identity.user-lifecycle.v1`。
- 收到 `user.lifecycle.created.v1` → **幂等开钱包**(`INSERT account ... ON CONFLICT DO NOTHING`)。
- 这是 M1/M2 事件的**第一个消费者**,验证 Event Mesh 闭环。
- `deleted` 事件的钱包清算留后续(M3 只消费 created)。

### 5. 事件契约

- 出站事件 `wallet.transaction.completed.v1`(Avro,RecordNameStrategy),topic `aurora.wallet.transaction.v1`。
- 字段:event_id / event_type / occurred_at_unix_ms / transaction_id / idempotency_key / user_id /
  tx_type / amount_minor / currency / resulting_balance_minor。
- 线格式 JSON(同 M1 约定)。

## 后果

**正面**:账本自洽可审计;重试安全;事件不丢(outbox);Event Mesh 有了真实消费者。
**负面 / 债务(登记 CLAUDE.md)**:
- outbox relay 轮询有**投递延迟**(轮询间隔);至少一次 → 下游需幂等。
- 单 relay 实例(无 leader 选举),多副本会重复发(下游幂等兜底);生产需加锁/分片。
- 余额不足不占用幂等键(见 §2 取舍)。
- SYSTEM mint 账户余额可为负(设计如此,代表已铸总量),无铸币上限控制。
- 账本无 **DB 级 CHECK** 保证 `balance_minor == SUM(ledger_entries)`,靠应用层同事务更新保证;
  对账/触发器留后续。单笔金额上限 `1e15` minor units + 余额加法溢出检测防 int64 wrap。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| 单边记账(只更用户余额)| 不可审计、对不上账;双记是行业标准 |
| 浮点表示钱 | 精度灾难;整数 minor units 是红线 |
| 直接在 handler 里发 Kafka(同 M1)| 弱投递、会丢;outbox 才是 M3 的目的 |
| CDC(Debezium)做 outbox | 基础设施过重;M3 用轮询 relay 足够 |
| 幂等键也记录失败交易 | 同一 key 不同时刻不同结果,语义混乱;失败不消费 key 更清晰 |
