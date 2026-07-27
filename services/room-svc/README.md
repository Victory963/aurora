# room-svc

Aurora 房间与聊天服务(M4)+ **AI 进房间提议 Pool 编排(M7)**。社交核心:用户在房间里聊天,
关键时刻 AI 在房内发消息并提议一个群体 Pool。

## 范围(M4)

- **房间 / 成员**:Postgres `aurora_room`(关系型、有约束)。
- **聊天消息**:**ScyllaDB**(Cassandra 兼容,按房间分区、timeuuid 倒序聚簇)。
- **实时信号**:**NATS**(presence / typing / message),subject `aurora.room.<room_id>.<event>`,
  at-most-once、不持久 —— Aurora **第一个 NATS producer**。
- **鉴权**:除 HealthCheck 外所有 RPC 需 `Authorization: Bearer <access_token>`(复用 M2 HS256,共享密钥本地校验)。
- **成员制**:SendMessage / ListMessages / ListMembers 要求是房间成员(否则 403)。

详见 [ADR-0005](../../docs/adr/0005-rooms-chat.md)、[PLAN](../../docs/m4/PLAN.md)。

## AI in Room 编排(M7)

新增 `ProposeAiPool`(房间成员触发):room-svc 编排三个服务把 demo 标志闭环接起来。

```
member → ProposeAiPool(room_id, match_id)
  1. ai-agent-svc.Recommend(mode=ROOM, user_id=caller, match_id, room_id)   [HTTP ~22s]
  2. 取 top 推荐 → bet-svc.CreatePool(room_id, question, YES/NO)            [转发调用者 Bearer ~8s]
  3. Scylla 写一条 AI 聊天消息(user_id="aurora-ai";服务端写入,不走成员校验)
  4. NATS `aurora.room.<id>.ai_proposal` 信号(pool_id + question)+ 常规 message 信号
  5. 返回 {recommendation, pool, message}
```

要点(见 [ADR-0007](../../docs/adr/0007-ai-in-room.md)):
- **Pool 创建者 = 触发的成员**(转发其 token → bet 侧成员校验/结算权限天然成立);AI 只"提议"。
- **半完成最糟**:AI 不可用/无推荐 → 502(不建 Pool、不发消息);bet 不可用 → 502。Pool 建成后聊天/NATS 尽力而为。
- ai/bet URL 未配置(`AURORA_ROOM_AI_URL` / `AURORA_ROOM_BET_URL` 为空)→ `ProposeAiPool` 返回 503 `not_configured`,
  room 其余功能不受影响。**注意**:bet-svc `depends_on` room-svc,故 room-svc 对 bet 是**运行时依赖**(不能反向 `depends_on`,否则成环)。

## 尚不包含

- 比赛事件**自动触发** ProposeAiPool(消费 `match.event.goal.v1`;事件源 M9 才真实,自动化留 M13)、AI 主动巡房。
- WebSocket 客户端实时通道(M10 Edge);M4/M7 只有服务端 NATS producer。
- 消息编辑/删除/已读、踢人/角色细化、房间删除(后续)。
- 共享 auth lib + 非对称 JWT(M10 / 重构期)。

## 端点(均 `Bearer`,除健康检查)

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/aurora.room.v1.RoomService/CreateRoom` | 建房(创建者=owner+member)|
| POST | `/aurora.room.v1.RoomService/ListRooms` | 我加入的房间 |
| POST | `/aurora.room.v1.RoomService/JoinRoom` | 加入(幂等)|
| POST | `/aurora.room.v1.RoomService/LeaveRoom` | 退出 |
| POST | `/aurora.room.v1.RoomService/ListMembers` | 成员列表(需成员)|
| POST | `/aurora.room.v1.RoomService/SendMessage` | 发消息 → Scylla + NATS(需成员)|
| POST | `/aurora.room.v1.RoomService/ListMessages` | 最近消息(需成员)|
| POST | `/aurora.room.v1.RoomService/Typing` | 打字信号 → NATS(需成员)|
| POST | `/aurora.room.v1.RoomService/ProposeAiPool` | **M7**:AI 提议群体 Pool(需成员;编排 ai+bet)|
| POST/GET | `/aurora.room.v1.RoomService/HealthCheck` | 健康检查(PG/Scylla down → 503)|
| GET | `/healthz` | 简易健康检查 |

### 例子

```bash
B=http://localhost:8084/aurora.room.v1.RoomService
# ACCESS 来自 identity 登录(见 identity-svc README)
RID=$(curl -sX POST $B/CreateRoom -H "Authorization: Bearer $ACCESS" \
        -H 'Content-Type: application/json' -d '{"name":"saitama-stand"}' | jq -r .id)
curl -sX POST $B/SendMessage -H "Authorization: Bearer $ACCESS" \
     -H 'Content-Type: application/json' -d "{\"room_id\":\"$RID\",\"body\":\"hello\"}"
curl -sX POST $B/ListMessages -H "Authorization: Bearer $ACCESS" \
     -H 'Content-Type: application/json' -d "{\"room_id\":\"$RID\"}"
```

## NATS subject 约定

`aurora.room.<room_id>.<event>`,event ∈ {`message`,`typing`,`presence`}。payload JSON。
M4 发布即 `Flush`(确保信号到达,利于 smoke 验证);实时网关阶段可放宽为批量。

## 单元测试

```bash
cd services/room-svc
go mod tidy                              # 首次:写 go.sum(gocql + nats.go)
go test ./...                            # 无 DB/Scylla 单测(JWT + handler 用 fake)
```

## 目录

```
services/room-svc/
├── cmd/main.go                          # 入口(PG + Scylla + NATS + HTTP)
├── internal/
│   ├── config/config.go                 # AURORA_ROOM_* 配置
│   ├── auth/jwt.go                       # 校验 identity 签发的 HS256 JWT(共享密钥;拷贝)
│   ├── store/postgres.go                # 房间 + 成员
│   ├── chat/scylla.go                    # 聊天消息(gocql)
│   ├── signals/nats.go                   # NATS publisher(首个 NATS producer)
│   └── server/server.go                 # HTTP handler + 鉴权 + 成员制
├── Dockerfile
├── go.mod
└── README.md (这份)
```

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `AURORA_ROOM_HTTP_ADDR` | `:8084` | HTTP 监听 |
| `AURORA_ROOM_DB_*` | 见 config | Postgres(库 `aurora_room`)|
| `AURORA_ROOM_SCYLLA_HOSTS` | `localhost` | csv;Scylla 主机 |
| `AURORA_ROOM_SCYLLA_PORT` | `9042` | Scylla 端口 |
| `AURORA_ROOM_SCYLLA_KEYSPACE` | `aurora_room` | keyspace |
| `AURORA_ROOM_NATS_URL` | `` (空) | 空=信号禁用(Nop)|
| `AURORA_ROOM_JWT_SECRET` | `aurora_dev_jwt_secret_change_me` | 与 identity 共享的 HS256 密钥 |
| `AURORA_ROOM_AI_URL` | `` (空) | **M7**:ai-agent-svc(空=ProposeAiPool 返回 503)|
| `AURORA_ROOM_BET_URL` | `` (空) | **M7**:bet-svc(空=ProposeAiPool 返回 503)|
