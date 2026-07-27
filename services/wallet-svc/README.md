# wallet-svc

Aurora **OSS 虚拟币**钱包(M3)。非真钱(真钱 + Pragmatic 见 M9)。

## 范围(M3)

- 双记账本(double-entry),金额一律**整数 minor units**(不用浮点),单币种 `AUC`。
- `Credit` / `Debit`(**必填 idempotency_key**,重放不双扣)、`GetBalance`。
- **Transactional outbox**:账本写入与事件同一 DB 事务;relay 协程投递到 Kafka(至少一次)。
- **消费 `user.lifecycle.created.v1`**:为新用户自动开钱包(M1/M2 事件的第一个消费者)。

详见 [ADR-0004](../../docs/adr/0004-wallet-ledger.md)、[PLAN](../../docs/m3/PLAN.md)。

## 尚不包含

- 真钱 / Pragmatic Adapter(M9)
- 跨账户转账(等 M6 结算)
- 多币种 / 汇率;反欺诈(M12)
- 账户删除清算钱包(M3 只消费 created)

## 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/aurora.wallet.v1.WalletService/Credit` | 入账(幂等)|
| POST | `/aurora.wallet.v1.WalletService/Debit` | 扣减(幂等,余额不足→422)|
| POST | `/aurora.wallet.v1.WalletService/GetBalance` | 查余额(未开钱包→404)|
| POST/GET | `/aurora.wallet.v1.WalletService/HealthCheck` | 健康检查(DB down→503)|
| GET | `/healthz` | 简易健康检查 |

### 例子

```bash
B=http://localhost:8083/aurora.wallet.v1.WalletService
# 用户在 identity 创建后,wallet-svc 会自动开钱包;然后:
curl -sX POST $B/Credit -H 'Content-Type: application/json' \
  -d '{"user_id":"<uid>","amount_minor":1000,"idempotency_key":"k1"}'
curl -sX POST $B/Debit  -H 'Content-Type: application/json' \
  -d '{"user_id":"<uid>","amount_minor":300,"idempotency_key":"k2"}'
curl -sX POST $B/GetBalance -H 'Content-Type: application/json' -d '{"user_id":"<uid>"}'
```

## 事件

| 项 | 值 |
|---|---|
| 出站 topic | `aurora.wallet.transaction.v1` |
| 事件类型 | `wallet.transaction.completed.v1` |
| Schema | [libs/events/avro/wallet.transaction.completed.v1.avsc](../../libs/events/avro/wallet.transaction.completed.v1.avsc) |
| 投递 | transactional outbox + relay(至少一次;下游按 `transaction_id` 幂等)|
| 入站 topic | `aurora.identity.user-lifecycle.v1`(消费 created → 开钱包)|

## 单元测试

```bash
cd services/wallet-svc
go mod tidy                              # 首次:写 go.sum
go test ./...                            # 无 DB 单测(handler 映射 + 幂等重放 + 事件编码)
AURORA_TEST_DB=1 go test ./...           # 账本集成测试(双记平衡/余额不足/幂等),要求 `make up`
```

## 目录

```
services/wallet-svc/
├── cmd/main.go                          # 入口(store + relay + consumer + HTTP)
├── internal/
│   ├── config/config.go                 # AURORA_WALLET_* 配置
│   ├── store/store.go                   # 账本事务 + 幂等 + outbox(核心)
│   ├── events/events.go                 # wallet.transaction.completed.v1 编码
│   ├── kafkapub/                        # Kafka publisher(relay 用)
│   ├── kafkautil/                       # broker 列表解析
│   ├── outbox/relay.go                  # outbox → Kafka relay
│   ├── consumer/consumer.go             # user.created → 开钱包
│   └── server/server.go                 # HTTP handler
├── Dockerfile
├── go.mod
└── README.md (这份)
```

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `AURORA_WALLET_HTTP_ADDR` | `:8083` | HTTP 监听 |
| `AURORA_WALLET_DB_*` | 见 config | Postgres(库 `aurora_wallet`)|
| `AURORA_WALLET_CURRENCY` | `AUC` | 币种 |
| `AURORA_WALLET_KAFKA_BROKERS` | `` (空) | csv;空=relay+consumer 关闭 |
| `AURORA_WALLET_OUTBOX_TOPIC` | `aurora.wallet.transaction.v1` | 出站事件 topic |
| `AURORA_WALLET_USER_EVENTS_TOPIC` | `aurora.identity.user-lifecycle.v1` | 入站 topic |
| `AURORA_WALLET_CONSUMER_GROUP` | `wallet-svc` | 消费组 |
| `AURORA_WALLET_RELAY_INTERVAL` | `1s` | outbox 轮询间隔 |
