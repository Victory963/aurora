# ADR-0002:Aurora Event Mesh(M1)

- **状态**:Accepted(M1 实施)
- **日期**:2026-06-30
- **覆盖 milestone**:M1
- **前置**:ADR-0001(M5,未实施)

## 上下文

M0 服务间只有**同步 HTTP**(ai-agent-svc → identity-svc)。这条路在 M2+ 会爆炸:
wallet(M3)、identity-graph(M8)、合规审计(M12)都需要"用户被创建了"这个事实,
但它们**不应该**让 identity-svc 同步等待。我们需要一条**异步事件总线**。

M1 的最小目标:**identity-svc 在 CreateUser 成功后,额外发布一个
`user.lifecycle.created.v1` 事件**,任何下游都能订阅做投影,且**不影响 CreateUser 的成功与延迟**。

## 决策

### 1. 双总线(Kafka + NATS),职责分离

| 总线 | 角色 | 语义 | 例子 |
|---|---|---|---|
| **Kafka** | 持久事件日志(durable log) | 至少一次、可重放、可压实 | `user.lifecycle.*`、`bet.placed`、`audit.action` |
| **NATS** | 瞬时低延迟信号(ephemeral) | 最多一次、不持久 | 房间在线状态、打字中、赔率 tick |

M1 只让 **Kafka 承载第一条真实事件**;NATS 在 compose 里**预置好基础设施**,
第一个 NATS producer 留给 **M4 room-svc**(不在 M1 顺手做,避免范围蔓延)。

### 2. M1 线格式:**JSON(符合 Avro 契约)**,二进制 Avro 延后

- **契约源**:`libs/events/avro/*.avsc`(Avro schema),注册到 Schema Registry 做治理与兼容性门禁。
- **M1 线上字节**:**JSON**,字段与 Avro record 一一对应。
- **为什么不直接上二进制 Avro**:与 M0 "JSON 优先,二进制可选" 一致;JSON 让 `smoke_m1.sh`
  用 `jq` 就能验证,调试成本低。二进制 Avro(Confluent wire format:magic byte + schema id + Avro binary)
  延后到下一个需要高吞吐/强 schema 绑定的里程碑(候选 M6 bet 流)。
- **治理仍然真实**:schema 注册 + `BACKWARD` 兼容模式现在就生效,演进破坏会被 SR 拒绝。

> **已知限制(M1)**:线上是 JSON 而非二进制 Avro;Schema Registry 用于治理而非运行时编解码。见 CLAUDE.md。

### 3. Topic 与 Subject 命名

- **Topic**:`aurora.<domain>.<entity>.v<bus-n>` → 本次 `aurora.identity.user-lifecycle.v1`
- **Subject 策略**:**RecordNameStrategy** — subject = Avro record 全名(`<namespace>.<RecordName>`)。
  本次 `aurora.identity.events.v1.UserLifecycleCreated`。
- **事件类型**(envelope 内):`<entity>.<verb>.v<schema-n>` → `user.lifecycle.created.v1`

为什么用 RecordNameStrategy 而非 Confluent 默认的 TopicNameStrategy:一个 topic 要承载同实体的
多种生命周期事件(created / deleted / …),每种是不同的 record。RecordNameStrategy 让每个 record
类型有**独立 subject 与独立兼容性演进**,互不绑架;消费者用 envelope 的 `event_type` 路由。
`register_schemas.sh` 直接从每个 `.avsc` 的 `namespace + "." + name` 推导 subject。

### 4. 事件信封(envelope)

每个事件是一条扁平 record(便于 jq 校验),含信封字段 + 数据字段:

```
event_id            string  事件自身 UUID(幂等去重用)
event_type          string  "user.lifecycle.created.v1"
occurred_at_unix_ms long    事件发生时间
<payload fields...>         user_id / email / display_name / kyc_country / created_at_unix_ms
```

Kafka message **key = user_id**(同一用户事件落同一分区,为 M3 投影与未来 log compaction 铺路)。

### 5. 写一致性:M1 用**双写**,outbox 延后

CreateUser 流程:

```
1. store.CreateUser  → 写 Postgres(成功才继续)
2. publisher.UserCreated(ctx, 2s timeout) → 发 Kafka
   - 成功:正常返回
   - 失败:slog.Warn 记录,**不回滚 DB、不让请求失败**(用户已创建是事实)
```

- **为什么不严格一致**:严格一致需要 **transactional outbox**(DB 与事件同事务),
  这要引入 outbox 表 + relay,属于 **M3** 的工作量。M1 接受"事件可能丢失"的弱保证,
  下游在 M3+ 通过 outbox + 重放补齐。
- **为什么 publish 不阻塞请求**:2s 硬超时(用 detached context,客户端断开不丢事件)+
  失败降级,Kafka 抖动不拖垮 CreateUser 的 P99。首条消息可能触发 topic 自动创建,
  `smoke_m1.sh` 会预创建 topic 让发布稳定落在超时内。

> **已知限制(M1)**:无 outbox,Kafka 不可用时事件会丢(DB 仍成功)。M3 引入 outbox pattern 修复。

### 6. 兼容性策略

- SR 全局/按 subject 设 `BACKWARD`:新版本能被老消费者读。
- 演进规则:只加**带 default 的可选字段**;不删字段、不改类型、不改字段名。
- 破坏性变更 → 新 schema 版本号 + 新 `event_type`(如 `...created.v2`),老版本并存一个迁移期。

## 后果

**正面**
- 下游解耦:wallet/graph/audit 订阅事件而非同步调用 identity。
- 现在就有 schema 治理与兼容性门禁。
- smoke 可用 jq 验证,CI 友好。

**负面 / 债务(已登记到 CLAUDE.md 已知限制)**
- 线上 JSON 非二进制 Avro(吞吐/体积非最优)。
- 无 outbox,弱投递保证。
- 单 broker、单 ZK、`replication-factor=1`:**仅 dev**,生产需多副本(留给部署阶段)。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| 只用 Kafka,不要 NATS | 房间实时信号(M4)用 Kafka 延迟/开销过高;双总线职责清晰 |
| 直接上二进制 Avro + SR 运行时编解码 | M1 调试成本高、Go 侧要引 SR client + avro codec;JSON 已满足验收 |
| KRaft(去掉 ZooKeeper) | 团队对 ZK 模式更熟,M0_NEXT_STEPS 已按 ZK 规划;迁 KRaft 留作后续清理 |
| 事务 outbox(M1 就做) | 属于 M3 工作量,M1 不顺手做 |
| RabbitMQ / Redis Streams | 与路线图(WP3)既定的 Kafka 生态不符 |
