# ADR-0005:room-svc 房间、聊天存储与 NATS 实时信号(M4)

- **状态**:Accepted(M4 实施)
- **日期**:2026-06-30
- **覆盖 milestone**:M4
- **前置**:M1(Event Mesh + NATS 预置)、M2(JWT)

## 上下文

社交是 Aurora 的核心:用户在**房间**里聊天,后续(M7)AI 进房间提议 Pool。M4 落地房间 +
成员 + 聊天,并启用 M1 预置但一直空着的 **NATS**(瞬时信号总线)。

## 决策

### 1. 存储分层:Postgres(关系) + ScyllaDB(聊天)

| 数据 | 存储 | 理由 |
|---|---|---|
| 房间、成员 | **Postgres** `aurora_room` | 关系型、低频、需要 JOIN/约束;DB 已由 init SQL 预建 |
| 聊天消息 | **ScyllaDB** | 高写入、append-only、按房间分区的时间序;Cassandra 模型天然契合 |

ScyllaDB(Cassandra 兼容)用 `gocql` 驱动。messages 表:
```
PRIMARY KEY (room_id, message_id timeuuid)  CLUSTERING ORDER BY (message_id DESC)
```
按 `room_id` 分区,`message_id` 倒序聚簇 → "房间最近 N 条"是单分区高效查询。dev 单节点
`SimpleStrategy rf=1`;生产多副本留部署阶段。

### 2. NATS = 瞬时信号(第一个 producer,补 M1)

- room-svc 在 SendMessage / Typing / 加退房时向 **NATS 核心**(非 JetStream)发信号:
  subject `aurora.room.<room_id>.<event>`,event ∈ {`message`,`typing`,`presence`}。
- 语义:**at-most-once、不持久**——纯实时 fan-out;**耐久历史在 Scylla**。
- 这是 ADR-0002 双总线里 NATS 角色的首次落地(Kafka=耐久日志,NATS=瞬时信号)。
- M4 无客户端实时通道(WebSocket 网关留 M10);smoke 通过 NATS 监控 `/varz` 的 `in_msgs`
  增长来验证"信号确实发到了总线"。

### 3. 鉴权:复用 M2 的 Bearer JWT(共享密钥本地校验)

- 除健康检查外所有 RPC 需 `Authorization: Bearer <access_token>`。
- room-svc 用**与 identity 相同的 HS256 密钥**(`AURORA_ROOM_JWT_SECRET`)**本地校验**,
  不回调 identity(低延迟)。`sub` claim = user_id。
- **共享对称密钥**是 M2 已登记的限制(ADR-0003);非对称(各服务持公钥本地验签)留 **M10**。
- 现状代码债:JWT 校验逻辑在 identity 与 room 各有一份拷贝(monorepo 暂无共享 Go module)。
  共享 auth lib 留重构期(候选 M10/M13)。

### 4. 成员制与授权

- CreateRoom:任意已登录用户;创建者 = OWNER + member。
- JoinRoom / LeaveRoom:已登录用户自助加入/退出。
- SendMessage / ListMessages / ListMembers:**必须是房间成员**(否则 403)。
- 房间删除 / 踢人 / 角色细化:留后续。

## 后果

**正面**:聊天写入走 Scylla(可水平扩展);NATS 实时信号闭环;房间关系数据有约束;鉴权低延迟。
**负面 / 债务(登记 CLAUDE.md)**:
- 多存储(PG+Scylla+NATS)运维面变大;两套迁移(PG SQL + CQL)。
- NATS 信号 at-most-once,客户端离线即丢(实时语义如此);耐久靠 Scylla。
- JWT 校验逻辑跨服务拷贝;共享密钥(M10 改非对称)。
- Scylla 单节点 rf=1 仅 dev;启动慢(compose start_period 拉长)。
- room-svc 不发 Kafka 事件(M4 不需要);AI/bet 接入留 M7/M6。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| 聊天也放 Postgres | 高写入/海量历史下 Cassandra 模型更合适;路线图既定 Scylla |
| 聊天放 Kafka 当存储 | Kafka 不是查询型存储;"房间最近消息"难查 |
| NATS JetStream 持久聊天 | 与"NATS=瞬时、Scylla=耐久"职责划分冲突;JetStream 留后续如需 |
| room-svc 回调 identity 校验每个请求 | 每请求一次 RPC,延迟/耦合高;本地验签更好(共享密钥代价已登记)|
| 房间/成员也放 Scylla | 关系约束/JOIN 在 CQL 上别扭;PG 更合适 |
