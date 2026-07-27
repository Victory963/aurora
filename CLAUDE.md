# CLAUDE.md — Aurora Monorepo

> 这个文件给 **Claude Code** 读。它告诉 Claude 在这个仓库工作时的约定、命令、和"不要踩的坑"。每个 M 阶段实现完后,把对应规则补进这里,Claude Code 在下次对话开始即可拿到完整上下文。

---

## 项目一句话

DAZN 下一代社交 + AI 体育博彩平台。**M0–M8 已完成**(含 demo 控制台 http://localhost:8090);路线图见 `docs/ROADMAP.md`。

## 当前阶段

- **M0**:identity-svc + ai-agent-svc 两服务跑通 happy path ✅
- **M1**:Aurora Event Mesh ✅ — Kafka + Schema Registry + NATS;CreateUser 发 `user.lifecycle.created.v1`。
- **M2**:identity 完整化 ✅ — 密码、登录、HS256 JWT + 轮换 refresh、设备、会话吊销、账户自删。
- **M3**:wallet-svc ✅ — 虚拟币 `AUC`、双记账本、幂等 Credit/Debit、**transactional outbox**、消费 user.created 开钱包。
- **M4**:room-svc ✅ — 房间/成员(Postgres `aurora_room`)、聊天(**ScyllaDB**)、**NATS 实时信号**
  (presence/typing/message,**首个 NATS producer**)、Bearer JWT 鉴权(共享密钥)、成员制(非成员 403)。
  `smoke_m4.sh` 验收。详见 `docs/adr/0005-rooms-chat.md`、`docs/m4/PLAN.md`。
- **M5**:ai-agent-svc 真实化 ✅ — **LangGraph 状态机 + 真 Claude API**(默认 `claude-opus-4-8`);
  数字只来自工具(player stats / odds / RG / room sentiment),LLM 只选市场 + 写推理;质量门重算 EV、丢弃幻觉市场;
  **Redis 检查点**(降级 MemorySaver);无 API key / LLM 超时 → 降级模板(smoke 仍 200)。
  `generate_recommendations` 签名不变(server.py 零改)。`smoke_m5.sh` 验收。详见 `docs/adr/0001-ai-agent-langgraph.md`、`docs/m5/PLAN.md`。
- **M6**:bet-svc ✅(Go,端口 8086,DB `aurora_bet`)— 房间内 **Closed Pool** + **parimutuel(彩池)结算**;
  下注走 wallet **幂等 Debit**、结算走 wallet **幂等 Credit**(崩溃可重放不重复扣/付);创建者充当 oracle 结算(M9/M13 换真);
  直发 Kafka `aurora.bet.lifecycle.v1`(`bet.placed.v1` / `bet.pool.settled.v1`)。`smoke_m6.sh` 验收。
  详见 `docs/adr/0006-bet-pools.md`、`docs/m6/PLAN.md`。
- **M7**:AI in Room ✅ — room-svc 新增 `ProposeAiPool`(成员触发):编排 ai-agent(推荐)→ bet-svc(建 Pool,
  **转发调用者 Bearer** 让其成为 Pool 创建者)→ Scylla 写 `aurora-ai` 聊天消息 → NATS `ai_proposal` 信号。
  demo 标志闭环落地:**AI 提议 → 群体下注 → 结算**。`smoke_m7.sh` 验收。详见 `docs/adr/0007-ai-in-room.md`、`docs/m7/PLAN.md`。
- **M8**:Identity Graph ✅ — graph-svc(Go,端口 8087)+ **Neo4j**(单节点 dev,堆 256m):
  纯事件投影(消费 identity + bet 生命周期,at-least-once + MERGE 幂等,可删库重建);
  查询 RPC:CoBettors(共现)/ SybilCandidates(同向共注启发式)/ Influence(KOL 跟注信号)/ GetUserGraph;
  **PII 边界**(图中只有 user_id/kyc_country/created_at,email/display_name 投影层丢弃)+
  **GDPR**(deleted 事件 → DETACH DELETE)。`smoke_m8.sh` 验收。详见 `docs/adr/0008-identity-graph.md`、`docs/m8/PLAN.md`。
- **Demo 控制台**(dev-only,非里程碑):`demo/` + nginx 容器(端口 **8090**)同源反代全部服务;
  浏览器打开 http://localhost:8090 可视化跑全闭环(注册/钱包/房间/聊天/AI 提议/下注/结算/图查询)。
- 下个阶段:**M9**(Pragmatic 接真钱)/ **M10**(Edge Gateway)/ **M12**(合规层)/ **M13**(端到端联调)

## 命令速查

```bash
make help                    # 看所有命令
make up                      # 启动 stack(含 Kafka/Schema-Registry/NATS/ScyllaDB)
make smoke                   # M0 验收
make smoke-m1                # M1 Event Mesh 验收
make smoke-m2                # M2 登录闭环验收
make smoke-m3                # M3 钱包验收(幂等/双记/outbox)
make smoke-m4                # M4 房间/聊天验收(Scylla/NATS)
make smoke-m5                # M5 AI 推荐验收(工具审计/降级安全,无 key 也过)
make smoke-m6                # M6 bet 验收(下注/parimutuel 结算/幂等/Kafka)
make smoke-m7                # M7 AI in Room 验收(ProposeAiPool 全闭环)
make smoke-m8                # M8 Identity Graph 验收(投影/共现/Sybil/影响力/GDPR)
make register-schemas        # 注册 Avro schema 到 Schema Registry
make test                    # 所有单元测试(identity + ai + wallet + room + bet + graph)
make tidy                    # 生成/更新各 Go 服务的 go.sum(加依赖后跑,提交结果)
make logs                    # 看日志
make down                    # 停止
make clean                   # 停止 + 删除 volume
```

## 目录约定

```
services/<service-name>/     # 一个服务,自包含
  ├── cmd/  或  <pkg>/       # 入口
  ├── internal/              # Go 服务私有代码
  ├── tests/                 # Python 服务测试
  ├── Dockerfile
  ├── README.md              # 服务级文档(必须有)
  └── go.mod / pyproject.toml
libs/proto/aurora/<domain>/v<n>/   # 跨服务 proto
infra/                       # docker / sql / k8s manifests
docs/adr/NNNN-<topic>.md     # 重大决策
scripts/                     # 维护 + 测试脚本
```

## 命名约定

| 类型 | 规则 | 例 |
|---|---|---|
| Go 服务 | `<domain>-svc` | `identity-svc` |
| Python 服务 | 同上 | `ai-agent-svc` |
| Python 包 | `aurora_<domain>` | `aurora_ai` |
| Proto package | `aurora.<domain>.v<n>` | `aurora.identity.v1` |
| HTTP URL | Connect-RPC 风格 | `/aurora.identity.v1.IdentityService/CreateUser` |
| Env var | `AURORA_<DOMAIN>_<KEY>` | `AURORA_IDENTITY_DB_HOST` |
| 数据库名 | `aurora_<domain>` | `aurora_identity` |
| Docker container | `aurora-<service>` | `aurora-identity-svc` |

## 跨服务通信约定(M0)

- 协议:HTTP + JSON
- URL:`POST /<proto-package>.<Service>/<Method>`
- 错误:`{ "code": "<machine-readable>", "message": "<human>" }`
- 健康检查:每个服务 `/healthz`(简易)+ `/<proto-package>.<Service>/HealthCheck`(完整)

**M1 升级**:URL 不变,加 buf 工具链,启用 application/proto 二进制协议作为可选 Content-Type。

## 提交规范

约束式 commit 信息:
```
<type>(<scope>): <短描述>

<可选正文>
```
type ∈ {feat, fix, refactor, docs, test, chore, perf}
scope ∈ {identity, ai-agent, infra, docs, ci, ...} 或服务名

例:
- `feat(identity): add CreateUser endpoint`
- `chore(infra): bump postgres to 16-alpine`

## 测试约定

- **单元测试**:不依赖外部服务(Go 用 httptest,Python 用 monkeypatch),`make test` 跑
- **集成测试**:需要 `make up`,标记或环境变量门控
  - Go:`AURORA_TEST_DB=1 go test`
  - Python:在 `scripts/smoke_m*.sh` 里
- **不要**写"自报通过"的虚假测试。如果不会过,就让它失败 + 标记 skip。

## 改动行为

### 添加一个新服务

1. 在 `services/` 创建新目录
2. 写 `README.md` 说明范围与边界
3. 写 `Dockerfile`
4. 在 `docker-compose.yml` 加 service
5. 加 `Makefile` 测试 target
6. 更新本文件的命令清单
7. 更新 `docs/ROADMAP.md` 标记 milestone

### 添加新的 RPC method

1. 改对应 `libs/proto/aurora/<domain>/v<n>/<file>.proto`
2. 在服务的 `internal/server/` 或 `aurora_*/server.py` 实现 handler
3. 加单元测试
4. 在 README 更新端点表
5. 在 `scripts/smoke_*.sh` 加 happy path 验证

### 添加跨服务依赖

1. 永远走 HTTP/RPC,不直连对方数据库
2. 调用方维护 timeout、retry、fallback
3. 调用结果加进 HealthCheck 的 `details` 里

## 禁止行为

- ❌ **不要**生成"自报跑通"的测试结果。任何"X 测试全过"必须可由我们 `make` 一键复现。
- ❌ **不要**写超过单文件 1500 行的代码。拆出 internal/ 子包。
- ❌ **不要**跨服务直接读对方数据库。所有通信走 HTTP/RPC。
- ❌ **不要**绕过 `go mod` / `pyproject.toml` 安装依赖。
- ❌ **不要**把密钥写进代码或 git。M0 用环境变量 + dev 默认值;M1 上 Vault。

## 当前已知限制

- Pragmatic PAM facade 没有实现;wallet 是 **OSS 虚拟币**(非真钱),真钱接入留 M9。
- 没有 Edge Gateway,客户端直连服务端口。M10 加 Cloudflare Workers。
- **M1 Event Mesh 限制**(见 ADR-0002):
  - 线上是 **JSON**(符合 Avro 字段契约),**不是二进制 Avro**;Schema Registry 仅做治理。延后(候选 M6)。
  - identity-svc 的事件仍是**直发**(无 outbox),Kafka 挂会丢;outbox 已在 wallet-svc(M3)落地,identity 迁移留后续。
  - 单 broker / 单 ZK / `replication-factor=1`,**仅 dev**;生产多副本留部署阶段。
  - NATS 已在 compose 预置,但**还没有 NATS producer**(第一个留给 M4 room-svc)。
- **M2 auth 限制**(见 ADR-0003):
  - access JWT **无状态**,无即时全局吊销;被盗 token 失效上限 = access TTL(≤15m)。
  - HS256 **对称共享密钥**;M10 改非对称,M9 上 Vault。无邮箱验证 / 找回密码 / OAuth / 角色授权。
- **M3 wallet 限制**(见 ADR-0004):
  - 幂等重放校验**全部参数**(user/amount/type;不匹配 → 409 `idempotency_conflict`,M6 评审加固)。
  - outbox relay 轮询有**投递延迟**;**至少一次**投递,下游需按 `transaction_id` 幂等消费。
  - **单 relay 实例**(无 leader 选举),wallet 多副本会重复发(下游幂等兜底);生产需加锁/分片。
  - 余额不足的 Debit **不占用**幂等键(可日后充值再试);SYSTEM mint 账户余额可为负,无铸币上限。
  - 只消费 `user.lifecycle.created.v1`(开钱包);`deleted` 清算钱包留后续。无跨账户转账(M6)。
- **M4 room 限制**(见 ADR-0005):
  - NATS 信号 **at-most-once、不持久**(客户端离线即丢);耐久聊天历史在 ScyllaDB。
  - JWT 校验逻辑在 identity 与 room **各有一份拷贝**(monorepo 无共享 Go module);共享密钥。共享 lib + 非对称留 M10/重构期。
  - Scylla 单节点 `rf=1` 仅 dev,启动慢(compose start_period 60s);无 WebSocket 客户端实时通道(M10 Edge)。
- **M5 ai-agent 限制**(见 ADR-0001):
  - 工具是**确定性 mock**(sha256 播种,非真 Sportradar/Pragmatic 数据源);真数据源留 M9,RG 真实接 compliance-svc 留 M12。
  - **无 key / 无 SDK / LLM 超时 / 质量门全丢 → 降级模板**;推荐 `data_sources` 带 `engine:` 标记区分 `llm:` vs `fallback:template`。
  - Claude API 规则:默认 `claude-opus-4-8`,**不发** temperature/top_p/thinking(4.7+ 会 400);结构化输出走 `output_config.format=json_schema`。
  - Redis 检查点仅存执行状态;**无跨请求会话记忆 / 无多轮对话**(单发推荐);KOL persona 仅 2 张卡(sakamoto/yamada),完整 MVP 留 M11。
- **M6 bet 限制**(见 ADR-0006):
  - 结算是 **parimutuel(彩池)**:输家池扣 rake 后按注额 pro-rata 分给赢家 + 退本;**无固定赔率**(option `odds_x100` 仅指示/展示)。
  - **创建者充当 oracle**(自己结算自己的 Pool);真实赛果 oracle 留 M9/M13。整数 minor 单位,大数用 `big.Int` 防溢出,**尘埃(除不尽)归庄家**。
  - 成员校验靠**转发调用者 token** 反调 room-svc(无服务身份);M6 只支持**恰好 2 个选项**、单房间 Pool、无部分撤单/无提前平仓。
  - 直发 Kafka(**无 outbox**),bet-svc 挂在 Debit 之后可用同 key 重放自愈;多副本重复发由下游幂等兜底。JWT 校验是**第 3 份拷贝**。
- **M8 graph 限制**(见 ADR-0008):
  - 投影**秒级延迟**、无 DLQ/lag 指标;bet 事件直发可丢 → 图是尽力而为视图,**钱以 wallet 为准**。
  - Sybil/KOL 是**启发式 demo 查询**,不是风控判定(真风控 M12);无设备/IP 信号(M9+ 数据源真实化后加)。
  - Neo4j 单节点 dev(堆 256m);JWT 校验是**第 4 份拷贝**(共享 lib M10)。
- **M7 AI in Room 限制**(见 ADR-0007):
  - 触发是**手动 RPC**(成员点按钮);比赛事件自动触发(消费 `match.event.goal.v1`)留 M13(事件源 M9 才真实)。
  - `aurora-ai` 是**保留 user_id**(不在 identity 注册,不是房间成员);AI 只"提议",Pool 创建者/结算权归**触发的成员**。
  - room-svc 对 ai/bet 是**运行时依赖**(带超时;URL 未配 → `ProposeAiPool` 返回 503,其余功能不受影响);NO 选项指示赔率 = 公平互补 `B=A/(A-1)`(仅显示)。
  - **注意 compose 依赖方向**:bet-svc `depends_on` room-svc,故 room-svc **不能** `depends_on` bet-svc(会成环)——靠客户端弹性(502)兜底。
- `go.mod` 的 indirect requires + `go.sum` 需本地 `make tidy`(`go mod tidy`)生成并提交;
  `make test-*` 与镜像构建都会先跑 `go mod tidy` 自愈,但**提交 go.sum** 才能复现构建。
  依赖:identity = `kafka-go`+`x/crypto`;wallet = `kafka-go`;room = `gocql`+`nats.go`+`pgx/v5`;bet = `pgx/v5`+`kafka-go`+`uuid`;graph = `neo4j-go-driver/v5`+`kafka-go`。
  Python(ai-agent):`anthropic` + `langgraph` + `redis` + `langgraph-checkpoint-redis`(见 `pyproject.toml`)。

> M0 评审已修复:健康检查 DB down 时返回 503 且不泄露 DSN;GetUser 非 UUID → 400;
> `isDuplicateKey` 改用 `pgconn.PgError`(23505);handler 层补无 DB 单测;Python 异常不再回显 `str(exc)`。

## 给 Claude Code 的工作流提示

M0–M7 ✅ 已完成(demo 标志闭环 **AI 提议 Pool → 群体下注 → 结算** 已由 `make smoke-m7` 真实跑通)。
当我说"做 M8/M9/…"时,**先读** `docs/ROADMAP.md` + 对应 ADR(若已有)看范围,先写 `docs/m<n>/PLAN.md` 再编码,
然后按"添加一个新服务"清单落地 + 写 `smoke_m<n>.sh` + 更新 README/CLAUDE.md/ROADMAP.md。

**跨服务编排的既定模式(M7 立的规矩,后续沿用)**:
- 编排放在**拥有相关上下文的服务**(M7 放 room-svc:它有聊天/NATS/成员校验);不反向让被编排方回调编排方。
- 跨服务调用**转发调用者的 Bearer token**(不引入服务身份体系,留 M10);下游据此做成员/权限校验,语义天然正确。
- 外部依赖客户端**可注入**(单测用 httptest 假服务 / 结构体 fake),base URL 空 = 禁用该依赖(返回 503,不拖垮本服务其余功能)。
- **compose 依赖不能成环**:若 A `depends_on` B,则 B 只能靠运行时弹性调用 A(不能反向 `depends_on`)。
- 钱相关的跨服务调用一律**幂等键**贯穿(bet↔wallet),崩溃后同 key 重放不重复扣/付。

**不要**在某个 M 顺便重构其它 M 的代码,除非破坏性变更明确(也只有那时)。
**范围纪律复盘**:M1 没顺手做 NATS/二进制Avro/outbox;M2 没顺手做角色/邮箱/OAuth/即时吊销;
M3 没顺手接真钱(M9)/跨账户转账(M6);M4 没顺手做 AI进房(M7)/WebSocket网关(M10)/共享 auth lib;
M5 没顺手接真数据源(M9)/真 RG(M12)/多轮记忆/完整 KOL(M11);M6 没顺手做固定赔率/真赛果 oracle(M9)/部分平仓;
M7 没顺手做比赛事件自动触发(M13)/AI 主动巡房/WebSocket 推送(M10)—— 各留对应里程碑。

## 大致工作量参考(M 后续阶段)

| 阶段 | 高级工程师工日 |
|---|---|
| M0(已完成) | 5-7 |
| M1 Event Mesh | 8-10 |
| M2 identity 完整化 | 7-9 |
| M3 wallet-svc OSS | 12-15 |
| M4 room-svc + chat | 10-12 |
| M5 ai-agent-svc LangGraph 真实化 | 15-20 |
| M6 bet-svc + Pool 数学 | 12-15 |
| M7 AI in Room 联调 | 8-10 |
| M8 Identity Graph | 10-12 |
| M9 Pragmatic Adapter + mock | 12-15 |
| M10 Edge Gateway | 8-10 |
| M11 KOL Persona MVP | 15-18 |
| M12 合规层(OPA + RG)| 10-12 |
| M13 端到端联调 + 性能 | 10-15 |
| **总计** | **142-180 工日 ≈ 6.5-9 月** |

详细拆解见 `docs/WORK_ESTIMATE.md`。

---

**最后**:每个 M 完成后,把这份 CLAUDE.md 的"当前阶段"、"当前已知限制"、"工作流提示"三段更新。这样下次对话 Claude 一开始就知道发生过什么。
