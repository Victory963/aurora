# M3 实施计划 — wallet-svc(OSS 虚拟币)

> 配套 [ADR-0004](../adr/0004-wallet-ledger.md)。完成口径:`make up && make smoke-m3` 跑通
> 充值 → 扣减 → 余额 → **幂等重放不双扣** → 余额不足拒绝 → 事件经 outbox 落 Kafka。

## 目标(验收口径)

`scripts/smoke_m3.sh` 真实跑通:

1. 建用户(identity)→ wallet-svc **消费 `user.lifecycle.created.v1` 自动开钱包**(余额 0)。
2. **Credit** 用户 1000 → 余额 1000。
3. **Debit** 300 → 余额 700。
4. **幂等**:用相同 idempotency_key 重放 Credit → 余额仍 700(不双记)。
5. **余额不足**:Debit 99999 → `insufficient_funds`,余额不变,幂等键不被占用。
6. **outbox**:每笔成功交易经 outbox relay 落 Kafka `aurora.wallet.transaction.v1`,符合 Avro 契约。

## 设计要点(详见 ADR-0004)

- **虚拟币**(非真钱,M9 才接 Pragmatic),单币种 `AUC`,**整数 minor units**(不用浮点)。
- **双记账本**:每笔交易两条 `ledger_entries`(USER 账户 + SYSTEM mint 账户)金额和为 0;
  `accounts.balance_minor` 物化余额,与分录同事务更新。
- **幂等**:`transactions.idempotency_key` UNIQUE;先对 USER 账户 `SELECT ... FOR UPDATE` 串行化,
  再查幂等键命中则返回原结果(重放);余额不足**不占用**幂等键。
- **Transactional outbox**(补 M1 弱投递):账本写入与 outbox 事件**同一 DB 事务**;
  独立 relay 协程轮询 outbox → Kafka → 标记已发(至少一次)。
- **第一个事件消费者**:消费 `user.lifecycle.created.v1` 幂等开钱包(M1/M2 事件首次被消费)。

## 数据模型(DB `aurora_wallet`,compose 已预建)

```
accounts(id PK, owner_type[USER|SYSTEM], owner_id, currency, balance_minor,
         created_at_unix_ms, UNIQUE(owner_type, owner_id, currency))
transactions(id PK, idempotency_key UNIQUE, type[CREDIT|DEBIT], user_id, amount_minor,
             currency, resulting_balance_minor, created_at_unix_ms)
ledger_entries(id PK, transaction_id→transactions, account_id→accounts, amount_minor[signed],
               created_at_unix_ms)
outbox(id PK, topic, msg_key, payload JSONB, created_at_unix_ms, published_at_unix_ms[0=未发])
```

迁移时 upsert 一个 SYSTEM `mint` 账户(余额可为负,代表已铸总量)。

## 新增 RPC(`libs/proto/aurora/wallet/v1/wallet.proto`)

`Credit`、`Debit`(均**必填 idempotency_key**)、`GetBalance`、`HealthCheck`。

## 交付物

| # | 文件 |
|---|---|
| 1 | `docs/adr/0004-wallet-ledger.md`、`docs/m3/PLAN.md` |
| 2 | `libs/proto/aurora/wallet/v1/wallet.proto` + `libs/events/avro/wallet.transaction.completed.v1.avsc` |
| 3 | `services/wallet-svc/`:cmd / config / store / server / outbox relay / consumer / Dockerfile / go.mod |
| 4 | 单测:幂等重放、余额不足、双记平衡、outbox 入库(无 DB,fake store)|
| 5 | `docker-compose.yml`(+wallet-svc,8083)、`Makefile`(smoke-m3)、`scripts/smoke_m3.sh` |
| 6 | README / CLAUDE / ROADMAP / wallet README |

## 不在 M3 范围(留后续)

- ❌ 真钱 / Pragmatic Adapter(M9)
- ❌ 跨账户转账(等 M6 结算需求)
- ❌ 多币种 / 汇率(后续)
- ❌ 反欺诈 ML(M12)
- ❌ 账户删除时清算钱包(M3 消费 created;deleted 处理留后续)

## 本机限制

本机仅 Windows go.exe(不能操作 WSL 文件)+ docker 守护未启,**无法本地构建/验收**;
代码经静态审查 + 自审。真验收在你的交互 shell:`make tidy`(wallet 也需)→ `make up` → `make smoke-m3`。
