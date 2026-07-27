# ADR-0003:Identity 会话、设备与 JWT(M2)

- **状态**:Accepted(M2 实施)
- **日期**:2026-06-30
- **覆盖 milestone**:M2
- **前置**:M0(users)、M1(Event Mesh)

## 上下文

M0/M1 的"用户"只有身份记录,没有**凭据**、**登录**、**会话**、**设备**概念。
M3 wallet、M4 room 都假设"有一个已登录的用户"。M2 把这块补齐,产出一个可被
`smoke_m2.sh` 跑通的登录闭环。

## 决策

### 1. 凭据:bcrypt

密码用 `golang.org/x/crypto/bcrypt`(DefaultCost)哈希,存 `user_credentials` 表
(与 `users` 分表,职责清晰)。**绝不**自研口令哈希。

> CreateUser 的 `password` 为**可选**:不传 → 保持 M0/M1 行为(smoke_m0/m1 不受影响);
> 传了 → 校验长度(≥8)后建凭据。

### 2. 双 token 模型:短 access JWT + 长 refresh

| token | 类型 | TTL | 存储 | 用途 |
|---|---|---|---|---|
| **access** | HS256 JWT(自包含) | 15m | 不落库(无状态) | 调鉴权端点 `Authorization: Bearer` |
| **refresh** | 32B 随机 → base64url | 30d | DB 存 **SHA-256 hex** | 换新 access;**轮换** |

- access JWT claims:`sub`(user_id)、`sid`(session_id)、`iss`=`aurora-identity`、`iat`、`exp`、`country`。
- refresh 明文**只在签发时返回一次**;DB 只存哈希(泄库也无法直接用)。
- **轮换**:每次 RefreshToken 生成新 refresh、更新 session 的 hash 与 `last_used`,旧 refresh 立即失效(检测重放/被盗)。

### 3. JWT 自己写(stdlib HS256),不引第三方库

`internal/auth/jwt.go` 用 `crypto/hmac`+`crypto/sha256`+`encoding/base64`+`encoding/json` 实现
签发/校验:
- 只接受 `alg=HS256`(显式校验 header,**拒绝 `alg=none` 降级攻击**)。
- 用 `hmac.Equal` 做**常数时间**签名比较。
- 校验 `exp`。

理由:HS256 实现简单可控、可单测,避免再引一个依赖(本阶段也无法本地 `go mod tidy` 验证新依赖)。
非对称(RS256/EdDSA,给 Edge Gateway 本地验签)留到 **M10**。

### 4. 会话吊销语义(诚实的折中)

- Logout / DeleteUser 置 session `revoked_at`,**refresh 立即失效**。
- access JWT **无状态**:不查库,失效上限 = access TTL(≤15m)。
- 不做即时全局吊销黑名单(需额外存储 + 每请求查库),M2 用**短 access TTL** 折中。

> **已知限制(M2)**:被盗 access token 在 ≤15m 内仍可用;真即时吊销留后续(候选 M10 Edge 校验 + 撤销表)。

### 5. 设备

按 `(user_id, platform, user_agent)` upsert(`ON CONFLICT DO UPDATE last_seen`),避免每次登录堆积行。
M2 不引入客户端设备指纹(留 M12 风控)。

### 6. 账户删除接 M1

DeleteUser(自删,鉴权)→ 删 `users` 行(`ON DELETE CASCADE` 连带 credentials/devices/sessions)
→ 发 `user.lifecycle.deleted.v1`(M1 已是前向契约的 Avro schema,M2 让它**真产出**)。

### 7. 密钥

`AURORA_IDENTITY_JWT_SECRET`(dev 默认值,**生产换 Vault**,M9)。HS256 对称密钥;
多服务若要验签需共享该密钥(M10 改非对称以避免共享密钥扩散)。

## 后果

**正面**:可登录闭环;refresh 轮换抗重放;密码/refresh 都只存哈希;删除接事件总线。
**负面 / 债务(登记到 CLAUDE.md)**:access 无即时吊销;HS256 共享密钥;无邮箱验证/找回/OAuth;无角色。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| 引 `golang-jwt/jwt` | 本阶段无法本地验证新依赖;HS256 自写足够且可控 |
| 纯 session(无 JWT,每请求查库) | 与未来 Edge 本地验签方向不符;每请求查库开销 |
| access 也查库吊销 | M2 复杂度过高;短 TTL 已是业界常见折中 |
| RS256/EdDSA 非对称 | M2 还没有 Edge/多验签方;留 M10 |
| 密码自研哈希 | 安全红线,绝不 |
