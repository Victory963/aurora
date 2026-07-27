# identity-svc

Aurora 用户身份管理服务。

## 范围(M0 + M1 + M2)

- 用户的创建与查询(M0)
- HTTP + JSON(Connect-RPC URL 约定)(M0)
- Postgres 存储,自动迁移(M0)
- **CreateUser 发布 `user.lifecycle.created.v1` 到 Kafka(M1)**
- **密码登录、JWT access + 轮换 refresh、设备追踪、会话吊销、账户自删(M2)**
- **DeleteUser 发布 `user.lifecycle.deleted.v1`(M2)**

## 尚不包含

- 邮箱验证 / 找回密码 / OAuth 第三方登录(后续 identity 迭代)
- 角色 / 授权策略(M12 合规 + OPA)
- access token 即时全局吊销(M2 用短 TTL 折中;见 ADR-0003)
- 事务 outbox / 严格投递保证(M3)
- Identity Graph 投影到 Neo4j(M8)
- KYC 集成(M9)

## M1 事件

CreateUser 成功后,**双写**一条事件到 Kafka(失败仅告警,不回滚、不让请求失败 —— 见
[ADR-0002](../../docs/adr/0002-event-mesh.md))。

| 项 | 值 |
|---|---|
| Topic | `aurora.identity.user-lifecycle.v1` |
| 事件类型 | `user.lifecycle.created.v1` |
| Schema | [libs/events/avro/user.lifecycle.created.v1.avsc](../../libs/events/avro/user.lifecycle.created.v1.avsc) |
| Subject | `aurora.identity.events.v1.UserLifecycleCreated`(RecordNameStrategy) |
| Message key | `user_id` |
| 线格式 | JSON(符合 Avro 字段契约;二进制 Avro 延后) |

Kafka 未配置(`AURORA_IDENTITY_KAFKA_BROKERS` 为空)时,使用 `Nop` publisher,服务仍按
M0 方式运行。

## 端点

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/aurora.identity.v1.IdentityService/CreateUser` | - | 创建用户(可选 `password`)|
| POST | `/aurora.identity.v1.IdentityService/GetUser` | - | 按 ID 查询 |
| POST | `/aurora.identity.v1.IdentityService/Login` | - | 密码登录 → access+refresh(M2)|
| POST | `/aurora.identity.v1.IdentityService/RefreshToken` | - | refresh 换新 access(轮换)(M2)|
| POST | `/aurora.identity.v1.IdentityService/Logout` | - | 吊销会话(M2)|
| POST | `/aurora.identity.v1.IdentityService/GetMe` | Bearer | 取本人(M2)|
| POST | `/aurora.identity.v1.IdentityService/ListDevices` | Bearer | 列设备(M2)|
| POST | `/aurora.identity.v1.IdentityService/DeleteUser` | Bearer | 自删账户 + 发事件(M2)|
| POST/GET | `/aurora.identity.v1.IdentityService/HealthCheck` | - | 健康检查 |
| GET | `/healthz` | - | 简易健康检查(DB down → 503)|

鉴权端点用 `Authorization: Bearer <access_token>`。access 为 HS256 JWT(15m);refresh 不落明文,
DB 存 SHA-256,30d,**每次 refresh 轮换**。详见 [ADR-0003](../../docs/adr/0003-sessions-jwt.md)。

### 登录闭环例子

```bash
B=http://localhost:8081/aurora.identity.v1.IdentityService
# 1. 建带密码的用户
curl -sX POST $B/CreateUser -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.com","display_name":"Alice","kyc_country":"GB","password":"hunter2hunter"}'
# 2. 登录
TOKENS=$(curl -sX POST $B/Login -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.com","password":"hunter2hunter","device":{"platform":"web"}}')
ACCESS=$(echo "$TOKENS" | jq -r .access_token)
# 3. 取本人
curl -sX POST $B/GetMe -H "Authorization: Bearer $ACCESS" -d '{}'
```

返回:

```json
{
  "user": {
    "id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "email": "alice@example.com",
    "display_name": "Alice",
    "kyc_country": "GB",
    "created_at_unix_ms": 1715284800000
  }
}
```

## 本地运行

```bash
# 从 monorepo 根目录
make up                                  # 启 Postgres + 所有服务
curl http://localhost:8081/healthz
```

## 单元测试

```bash
cd services/identity-svc
go mod download                          # 首次:拉依赖并写 go.sum(或 `make tidy` 提交)
go test ./...                            # 无 DB 单测(handler + error mapping + 事件编码)
AURORA_TEST_DB=1 go test ./...           # 含 DB 集成测试,要求 `make up` 已启
```

无 DB 单测覆盖:CreateUser happy/重复 email→409/GetUser not-found→404/非 UUID→400、
健康检查 503 不泄露 DB 错误、事件编码符合 Avro、JWT 签验(含拒 `alg=none`/过期)、bcrypt、
登录闭环(login→GetMe→refresh 轮换→logout→delete)与未授权 401。

## 目录

```
services/identity-svc/
├── cmd/main.go                          # 入口(接线 publisher + auth)
├── internal/
│   ├── config/config.go                 # 环境变量配置(含 JWT/TTL)
│   ├── auth/                            # M2:JWT(HS256,stdlib)+ bcrypt + refresh token
│   ├── store/
│   │   ├── store.go                     # users + 迁移
│   │   └── store_auth.go                # M2:credentials/devices/sessions
│   ├── events/                          # Publisher + Created/Deleted 事件 + Kafka 实现
│   └── server/
│       ├── server.go                    # users + 路由 + 健康检查
│       └── server_auth.go               # M2:login/refresh/logout/getme/devices/delete
├── Dockerfile
├── go.mod
└── README.md (这份)
```

## 配置

所有运行时配置通过 `AURORA_IDENTITY_*` 环境变量,默认值见 `internal/config/config.go`。

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `AURORA_IDENTITY_HTTP_ADDR` | `:8081` | HTTP 监听 |
| `AURORA_IDENTITY_DB_*` | 见 config | Postgres 连接 |
| `AURORA_IDENTITY_KAFKA_BROKERS` | `` (空) | csv;空=禁用事件发布(Nop) |
| `AURORA_IDENTITY_KAFKA_TOPIC` | `aurora.identity.user-lifecycle.v1` | 事件 topic |
| `AURORA_IDENTITY_JWT_SECRET` | `aurora_dev_jwt_secret_change_me` | HS256 签名密钥(生产换 Vault)|
| `AURORA_IDENTITY_ACCESS_TTL` | `15m` | access JWT 有效期 |
| `AURORA_IDENTITY_REFRESH_TTL` | `720h` | refresh token 有效期(30d)|
