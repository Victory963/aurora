# M2 实施计划 — identity 完整化(设备 / 会话 / JWT / 登录)

> 配套 [ADR-0003](../adr/0003-sessions-jwt.md)。完成口径:`make up && make smoke-m2` 跑通登录闭环。

## 目标(验收口径)

在 M0/M1 基础上,`scripts/smoke_m2.sh` 真实跑通:

1. CreateUser 带 `password`(可选字段;不传则保持 M0/M1 行为)。
2. **Login**(email+password+device)→ 返回 access JWT + refresh token,落库 device + session。
3. **GetMe**:带 `Authorization: Bearer <access>` → 返回本人;不带/无效 → 401。
4. **ListDevices**(鉴权)→ 至少 1 个登录设备。
5. **RefreshToken**:用 refresh 换新 access(并轮换 refresh);旧 refresh 立即失效。
6. **Logout**:吊销 session;之后 refresh → 401。
7. **DeleteUser**(鉴权,自删)→ 200,GetMe → 404,且 Kafka 收到 `user.lifecycle.deleted.v1`(接 M1)。

## 设计要点(详见 ADR-0003)

- 密码:`bcrypt`(`golang.org/x/crypto/bcrypt`,DefaultCost)。
- Access token:**HS256 JWT**(stdlib 手写,无第三方依赖),claims `sub/sid/iss/iat/exp/country`,TTL 15m。
- Refresh token:32 字节随机 → base64url 明文只在签发时返回一次;DB 存 **SHA-256 hex**;TTL 30d;refresh 时**轮换**。
- 会话吊销:Logout/Delete 置 `revoked_at`;access token 无状态,失效上限 = access TTL(已文档化)。
- 设备:按 `(user_id, platform, user_agent)` upsert,登录刷新 `last_seen`。

## 数据模型(新增 3 张表,`ON DELETE CASCADE` 挂在 users 上)

```
user_credentials(user_id PK→users, password_hash, updated_at_unix_ms)
devices(id PK, user_id→users, platform, user_agent, created_at_unix_ms, last_seen_unix_ms,
        UNIQUE(user_id, platform, user_agent))
sessions(id PK, user_id→users, device_id→devices, refresh_token_hash UNIQUE,
         created_at_unix_ms, expires_at_unix_ms, last_used_at_unix_ms, revoked_at_unix_ms)
```

## 新增 RPC(扩 `libs/proto/aurora/identity/v1/identity.proto`)

`Login`、`RefreshToken`、`Logout`、`GetMe`、`ListDevices`、`DeleteUser`;`CreateUser` 加可选 `password`。
鉴权端点(GetMe/ListDevices/DeleteUser)走 `Authorization: Bearer`。

## 交付物

| # | 文件 |
|---|---|
| 1 | `docs/adr/0003-sessions-jwt.md`、`docs/m2/PLAN.md` |
| 2 | `internal/auth/{jwt,password}.go` + 测试(纯单元,无 DB) |
| 3 | `internal/store/store_auth.go`(类型 + 方法 + migrate) |
| 4 | `internal/events/`:`UserDeleted` + Kafka 方法 + 测试 |
| 5 | `internal/server/server_auth.go`(handler + 鉴权)+ `server_auth_test.go`(fake,无 DB) |
| 6 | `config.go` / `main.go` / `docker-compose.yml` / `go.mod`(+x/crypto) |
| 7 | `scripts/smoke_m2.sh` + Makefile `smoke-m2` |
| 8 | README / CLAUDE / ROADMAP / identity README |

## 不在 M2 范围(留后续)

- ❌ 角色 / 管理员 / 授权策略(M12 合规 + OPA)
- ❌ 邮箱验证 / 找回密码 / OAuth 第三方登录(后续 identity 迭代)
- ❌ 跨服务 JWT 校验下沉到 Edge(M10 Gateway)
- ❌ 即时全局吊销(需 access token 短 TTL 之外的黑名单;M2 用短 TTL 折中)
- ❌ 设备指纹 / 风控(M12)

## 本机限制

无 go/docker,**不能本地构建/验收**;经静态审查 + 对抗复核。真验收在 Docker 机上
`make tidy`(新增 x/crypto)→ `make up` → `make smoke-m2`。
