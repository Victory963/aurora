# bet-svc

Aurora 群体下注服务(M6)。房间内的 **Closed Pool**(封闭彩池)+ **parimutuel(彩池)结算**。

端口 **8086**,数据库 `aurora_bet`(Postgres)。见 [ADR-0006](../../docs/adr/0006-bet-pools.md)、[PLAN](../../docs/m6/PLAN.md)。

## 范围(M6)

- **Pool**:房间成员针对一个问题开一个封闭池,**恰好 2 个选项**(如 YES/NO)。选项 `odds_x100`
  仅作**指示/展示**(结算是 parimutuel,不是固定赔率)。
- **下注(PlaceBet)**:先走 wallet **幂等 Debit**(同 `idempotency_key` 重放不重复扣),再落 bet 行;
  池已关闭时冲正(void Credit)。回放同 key → `replayed: true`。
- **结算(SettlePool)**:池创建者充当 oracle 指定 winning option。**parimutuel 分配**:
  输家池扣 rake(默认 5%)后,按赢家注额 pro-rata 分配 + 退还本金;无赢家 → 全额退款(免 rake)。
  每笔赢/退用**幂等 Credit**(`settle-<betID>` / `refund-<betID>`),崩溃后重跑 SettlePool 可续、不重复付。
- **事件**:直发 Kafka `aurora.bet.lifecycle.v1`(`bet.placed.v1` / `bet.pool.settled.v1`,JSON 符合 Avro 契约)。
- **鉴权**:除 HealthCheck 外所有 RPC 需 `Authorization: Bearer <access>`(复用 M2 HS256,共享密钥)。
- **成员制**:room-scoped 的 Pool,CreatePool / PlaceBet 会**转发调用者 token** 反调 room-svc 校验成员(非成员 403)。

## 结算数学(parimutuel)

赢家 `i` 的赔付(整数 minor 单位,`big.Int` 防溢出):

```
losers_pool   = Σ(输家注额)
distributable = losers_pool - floor(losers_pool * rake_bps / 10000)
payout_i      = stake_i + floor(distributable * stake_i / winners_pool)
```

尘埃(除不尽的余数)归庄家。无赢家 → 每人退回 `stake_i`(rake=0)。
例:A 押 YES 1000、B 押 NO 600,YES 胜,rake 5% → A 收回 `1000 + floor(600*0.95)=570` = **1570**;B 得 0;庄家留 30。

## 端点(Connect-RPC URL 风格)

| Method | 说明 |
|---|---|
| `POST /aurora.bet.v1.BetService/CreatePool` | 建池(room_id 可选;2 选项) |
| `POST /aurora.bet.v1.BetService/GetPool` | 查池(含每选项聚合注额/人数) |
| `POST /aurora.bet.v1.BetService/ListRoomPools` | 列某房间的池(需成员) |
| `POST /aurora.bet.v1.BetService/PlaceBet` | 下注(幂等键必填) |
| `POST /aurora.bet.v1.BetService/SettlePool` | 结算(仅创建者;幂等/可续) |
| `GET/POST /aurora.bet.v1.BetService/HealthCheck`、`GET /healthz` | 健康 |

## 环境变量

| Var | 默认 | 说明 |
|---|---|---|
| `AURORA_BET_HTTP_ADDR` | `:8086` | 监听地址 |
| `AURORA_BET_DB_*` | postgres/aurora_bet/aurora | Postgres 连接 |
| `AURORA_BET_WALLET_URL` | `http://localhost:8083` | wallet-svc(扣款/赔付) |
| `AURORA_BET_ROOM_URL` | `""` | room-svc(成员校验;空=禁用,dev only) |
| `AURORA_BET_KAFKA_BROKERS` | `""` | 空=事件禁用(Nop) |
| `AURORA_BET_KAFKA_TOPIC` | `aurora.bet.lifecycle.v1` | 生命周期 topic |
| `AURORA_BET_JWT_SECRET` | dev 默认 | HS256 共享密钥 |
| `AURORA_BET_RAKE_BPS` | `500` | rake 基点(500 = 5%) |

## 测试

```bash
make test-bet        # 单测:poolmath(含尘埃守恒、1e15 不溢出)+ 全流程结算(fake wallet/room/store)
make smoke-m6        # 集成:下注/幂等/parimutuel/Kafka(需 make up)
make tidy-bet        # 加依赖后生成并提交 go.sum
```

## 尚不包含(留后续里程碑)

- 固定赔率玩法 / 部分撤单 / 提前平仓(cash-out)—— parimutuel 之外的产品形态。
- 真实赛果 **oracle**(现在是创建者手动结算)—— M9 Pragmatic 数据源 / M13 联调。
- 3+ 选项 Pool、跨房间 Pool、Pool 生命周期定时自动关闭。
- 服务身份体系(现靠转发用户 token 做成员校验)—— M10。
- outbox(现直发 Kafka;wallet.transaction 事件仍是钱的 source of truth)。
