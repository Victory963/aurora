# M4 实施计划 — room-svc(房间 + 聊天 + ScyllaDB + NATS)

> 配套 [ADR-0005](../adr/0005-rooms-chat.md)。完成口径:`make up && make smoke-m4` 跑通
> 建房 → 加成员 → 发消息 → 收消息,且首个 **NATS producer**(瞬时信号)生效。

## 目标(验收口径)

`scripts/smoke_m4.sh` 真实跑通:

1. 用户 A 登录(复用 M2)→ **CreateRoom**(A 为 owner+member)。
2. 用户 B 登录 → **JoinRoom** → **ListMembers** 返回 2 人。
3. A **SendMessage** "hello" → 持久化到 **ScyllaDB**,并向 **NATS** 发实时信号。
4. **ListMessages** 返回 "hello"(从 Scylla 读)。
5. **Typing** 信号发到 NATS;通过 NATS 监控 `/varz` 的 `in_msgs` 增长**证明 NATS producer 生效**。
6. 鉴权:无 Bearer → 401;非成员发消息 → 403。

## 设计要点(详见 ADR-0005)

- **存储分层**:房间/成员(关系型)→ Postgres `aurora_room`(compose 已预建);聊天消息(高写入、append-only)→ **ScyllaDB**(gocql)。
- **NATS = 瞬时信号**(补 M1 预置的 NATS,**第一个 producer**):`message`/`typing`/`presence`,核心 NATS(at-most-once,不持久);耐久历史在 Scylla。
- **鉴权复用 M2**:room-svc 用**共享 HS256 密钥**本地校验 Bearer access JWT(`sub`=user_id)。
  跨服务共享密钥是 M2 已登记的限制(M10 改非对称)。
- **成员制**:SendMessage / ListMessages / ListMembers 要求调用者是房间成员(否则 403)。

## 数据模型

Postgres `aurora_room`:
```
rooms(id PK, name, owner_user_id, created_at_unix_ms)
room_members(room_id→rooms, user_id, role[OWNER|MEMBER], joined_at_unix_ms, PRIMARY KEY(room_id,user_id))
```

ScyllaDB keyspace `aurora_room`:
```
messages(room_id text, message_id timeuuid, user_id text, body text, created_at_unix_ms bigint,
         PRIMARY KEY (room_id, message_id)) WITH CLUSTERING ORDER BY (message_id DESC)
```
按 `room_id` 分区,`message_id`(timeuuid)倒序聚簇 → "房间最近消息"高效。

## NATS subject 约定

`aurora.room.<room_id>.<event>`,event ∈ {`message`,`typing`,`presence`}。payload JSON。

## 新增 RPC(`libs/proto/aurora/room/v1/room.proto`)

`CreateRoom`、`ListRooms`、`JoinRoom`、`LeaveRoom`、`ListMembers`、`SendMessage`、`ListMessages`、
`Typing`、`HealthCheck`。除 HealthCheck 外**均需 Bearer**。

## 交付物

| # | 文件 |
|---|---|
| 1 | `docs/adr/0005-rooms-chat.md`、`docs/m4/PLAN.md` |
| 2 | `libs/proto/aurora/room/v1/room.proto` |
| 3 | `services/room-svc/`:cmd / config / auth / store(postgres) / chat(scylla) / signals(nats) / server / Dockerfile / go.mod |
| 4 | 单测:JWT 校验、handler(fake store/chat/nats),无 DB/Scylla |
| 5 | `docker-compose.yml`(+scylla +room-svc 8084)、`Makefile`(smoke-m4)、`scripts/smoke_m4.sh` |
| 6 | README / CLAUDE / ROADMAP / room README |

## 不在 M4 范围(留后续)

- ❌ AI 进房间提议 Pool(M7,接 M5+M6)
- ❌ 下注 / 钱包扣减(M6 bet-svc;room 只聊天)
- ❌ WebSocket 网关 / 客户端实时推送通道(M10 Edge);M4 NATS 仅服务端 producer + 验证
- ❌ 消息已读回执 / 编辑 / 删除 / 表情;房间权限角色细化(后续)
- ❌ 跨服务 JWT 改非对称、共享 auth lib(M10 / 重构期)

## 本机限制

本机仅 Windows go.exe(不能操作 WSL 文件)+ docker 守护未启,**无法本地构建/验收**;
代码经静态审查 + 对抗复核。真验收:`make tidy`(room 新增 gocql/nats.go)→ `make up` → `make smoke-m4`。
