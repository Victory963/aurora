# Aurora 工程工作量诚实拆解

> 这份文档假设:**1 名高级全栈工程师全职 8-10 小时/天**。
> 数字是"产出+测试+文档+code review 修改+卡壳排查"的总和,不是纯写代码时间。

---

## 总览

| 输入 | 数值 |
|---|---|
| 总工日 | **142-180 工日** |
| 折算月数(20 工日/月) | **7-9 个月** |
| 实际日历时间(含周末 + 假期 + 卡壳) | **8-11 个月** |
| 与其团队协同(若有)折扣 | 减 15-30% |

---

## M0 (已完成,做参考校准)

实际产出(本对话):
- ~1,500 行生产代码(Go + Python)
- ~250 行测试
- 2 个 README,1 个 CLAUDE.md,3 张架构图
- 1 个 smoke 脚本
- docker-compose + Makefile

折算工日:**5-7 天**
- 设计 + ADR 起草:0.5 天
- identity-svc 代码:1 天
- ai-agent-svc 代码:0.5 天
- docker-compose + Makefile + 脚本:0.5 天
- 文档(README、CLAUDE、ROADMAP):1 天
- 调试 + 跑通 smoke:1-2 天
- 架构图:0.5 天
- 余地(代码 review 改 + 边角情况):1 天

校准点:**M0 的真实代码 1,500 行 / 6 天 ≈ 250 行/天**。这个生产率适用于"较熟悉的栈 + 已知设计"。

后续 M 中,**新领域 + 新工具 + 新设计**的生产率会降到 **100-150 行/天**。

---

## M1: Aurora Event Mesh

**目标**:Kafka + NATS + Schema Registry,identity-svc CreateUser 时发 `user.lifecycle.created.v1`,可被外部消费验证。

| 子任务 | 工日 |
|---|---|
| ADR 撰写(双总线、Avro 选型) | 0.5 |
| docker-compose 加 Kafka + ZK + SR + NATS,健康等待 | 1 |
| Avro schema(首批 ~5 个 event types)+ 注册脚本 | 1 |
| identity-svc 加 Kafka producer + 双写模式 | 2 |
| 一个消费者验证脚本(Go 或 Python)| 1 |
| smoke_m1.sh:CreateUser → 验证事件被消费 | 1 |
| 文档更新 | 0.5 |
| 调试 + 缓冲 | 1.5-2.5 |
| **小计** | **8-10** |

---

## M2: identity 完整化

**目标**:设备、会话、JWT 签发、登录、token 刷新。

| 子任务 | 工日 |
|---|---|
| ADR(JWT vs 不透明 token,key rotation 策略)| 0.5 |
| devices 表 + sessions 表 + migration | 1 |
| Login/Logout/RefreshToken 端点(handlers + tests) | 2 |
| JWT 签发(JWKS endpoint)| 1 |
| device fingerprint 抽取 + 关联 | 1 |
| 集成测试(整套 flow:Create → Login → Refresh) | 1 |
| smoke_m2.sh | 0.5 |
| 调试 + 缓冲 | 1-1.5 |
| **小计** | **7-9** |

---

## M3: wallet-svc(OSS)

**目标**:虚拟币 Debit/Credit + 幂等 + 事件溯源。基于 formeo/igaming-platform 模式启动,自己重写。

| 子任务 | 工日 |
|---|---|
| ADR(Event Sourcing 模式 + 幂等键策略)| 1 |
| 数据库 schema(wallet_events, wallet_balances)+ migration | 1 |
| Debit/Credit/GetBalance/GetTransactions 端点 | 3 |
| 幂等性 + 乐观锁实现 + 全面测试 | 2 |
| Event Mesh 发布 wallet 事件 | 1 |
| Wallet interface(Facade Pattern starter) | 1 |
| 集成测试 + 并发场景 | 2 |
| smoke_m3.sh | 0.5 |
| 调试 + 缓冲(并发问题最容易卡)| 1.5-3.5 |
| **小计** | **12-15** |

---

## M4: room-svc

**目标**:创建房间 / 加成员 / 聊天 / ScyllaDB(或 Postgres for M4,M11 后迁移)

| 子任务 | 工日 |
|---|---|
| ADR(房间数据模型 + 实时通信选型) | 0.5 |
| Room/Member/Message schema | 1 |
| 房间 CRUD 端点 | 2 |
| 聊天:WebSocket(Centrifugo OR 简易内置) | 2 |
| 房主迁移逻辑 | 1 |
| 集成测试 | 1.5 |
| smoke_m4.sh:创建房间 + 加成员 + 发消息 + 读 | 1 |
| 调试 + 缓冲 | 1-3 |
| **小计** | **10-12** |

---

## M5: ai-agent-svc 真实化

**目标**:LangGraph 状态机 + 真 LLM(Claude API)+ 6 个工具。

| 子任务 | 工日 |
|---|---|
| ADR-001 细化(已有起草)+ 节点详细设计 | 1 |
| LangGraph 状态机骨架(13 节点)| 2 |
| 6 个 tool 实现(stats、odds、user_history、rg_check、kelly、graph) | 3 |
| Claude API 集成 + 流式 | 2 |
| Redis Checkpointer 自定义 | 2 |
| JSON schema 强约束 + 重试 | 2 |
| 评估框架(LangSmith 或自建) | 1 |
| 集成测试 + 多模式覆盖 | 2 |
| smoke_m5.sh | 0.5 |
| LLM 行为调优 + 缓冲(LLM 总有惊喜) | 0.5-4.5 |
| **小计** | **15-20** |

---

## M6: bet-svc + Closed Pool

**目标**:Closed Pool 数学引擎、结算、与 wallet-svc 集成。

| 子任务 | 工日 |
|---|---|
| ADR(Pool 数学 + 结算原子性 + 反勾结)| 1 |
| Pool / Bet 数据模型 | 1 |
| CreatePool / JoinPool / LockPool / SettlePool 端点 | 3 |
| 与 wallet-svc 双向交互(Debit on join, Credit on settle)| 2 |
| Saga 模式(失败回滚)| 2 |
| 反勾结启发式(5 层中的 3 层)| 1.5 |
| 集成测试(含失败场景)| 2 |
| smoke_m6.sh | 0.5 |
| 调试 + 缓冲 | 1-2 |
| **小计** | **12-15** |

---

## M7: AI in Room

**目标**:把 M4(room)+ M5(AI)+ M6(bet)接起来。AI 在房间发起 Pool 提议。

| 子任务 | 工日 |
|---|---|
| ADR(房间多人协议 + AI 提议 lifecycle)| 0.5 |
| Aurora AI 作为 room 成员(member type 扩展) | 1 |
| AI 触发逻辑(订阅 match.event.* + 节流)| 1.5 |
| Proposal 数据模型 + 房间投票机制 | 1.5 |
| 通过投票 → 调 bet-svc 创建 Pool | 1 |
| 集成测试(从 match event 到 Pool 创建)| 1.5 |
| smoke_m7.sh | 0.5 |
| 调试 + 缓冲 | 0.5-1.5 |
| **小计** | **8-10** |

---

## M8: Identity Graph

**目标**:Neo4j Causal Cluster + 异步投影服务。

| 子任务 | 工日 |
|---|---|
| ADR(Neo4j vs Dgraph + PII 分离策略)| 0.5 |
| docker-compose 加 Neo4j | 0.5 |
| Cypher schema(节点 / 关系 / 索引)| 1 |
| 投影服务(Kafka consumer → Cypher write)| 3 |
| 3 个 demo 查询端点(Sybil、KOL 风险、共现)| 2 |
| GDPR 删除流程 | 1 |
| 集成测试 | 1.5 |
| smoke_m8.sh | 0.5 |
| 调试 + 缓冲 | 0-2 |
| **小计** | **10-12** |

---

## M9: Pragmatic Adapter

**目标**:Facade Pattern 实现 + Mock Pragmatic server(他们的 sandbox 申请通常 6-12 周)。

| 子任务 | 工日 |
|---|---|
| ADR(Facade 接口 + Mock 范围)| 1 |
| Mock Pragmatic server(独立服务,模拟 SOAP/REST)| 3 |
| Pragmatic Adapter 实现 | 2 |
| Wallet Router(OSS vs Pragmatic routing)| 1.5 |
| 跨账户 Saga(Temporal 或简化版)| 2 |
| 集成测试(mock + real switch)| 2 |
| smoke_m9.sh | 0.5 |
| 调试 + 缓冲 | 0-3 |
| **小计** | **12-15** |

---

## M10: Edge Gateway

**目标**:Cloudflare Workers + Durable Objects + 鉴权 + 路由。

| 子任务 | 工日 |
|---|---|
| ADR(Workers vs ALB,Durable Objects 用法)| 0.5 |
| Worker 代码骨架(TypeScript)| 2 |
| JWT 校验 + 速率限制 | 1.5 |
| Connect-RPC URL 路由到内部服务 | 1 |
| Wrangler dev 流程文档 | 0.5 |
| 集成测试(Worker → identity → ai-agent)| 1.5 |
| smoke_m10.sh | 0.5 |
| 调试 + 缓冲 | 0.5-2.5 |
| **小计** | **8-10** |

---

## M11: KOL Persona MVP

**目标**:1 个真实 KOL 同意作为 alpha,Few-shot prompt 调出可用 Persona,粉丝订阅 + 推荐流。

| 子任务 | 工日 |
|---|---|
| ADR(Persona 训练流程 + 法律检查表)| 1 |
| KOL 数据采集脚本(从 Twitter / blog 抓投注历史)| 2 |
| Persona Card 生成器(GPT 提取风格)| 2 |
| Few-shot prompt template + 调优 | 2 |
| Subscription 数据模型 + payments adapter | 2 |
| Persona 推荐端点(集成 M5 ai-agent-svc)| 2 |
| 法律 review(请律师走一遍)| 0.5 |
| 内部 KOL 自审流程 + UI 占位 | 1 |
| 集成测试 + smoke_m11.sh | 1 |
| 调试 + 缓冲(LLM 风格调优容易卡 1-2 周)| 1.5-4.5 |
| **小计** | **15-18** |

---

## M12: 合规层

**目标**:OPA(Open Policy Agent)+ 跨服务统一 RG check + 多区合规策略。

| 子任务 | 工日 |
|---|---|
| ADR(OPA 部署模式 + 策略组织)| 0.5 |
| OPA sidecar 部署模板 | 1 |
| 第一批策略(RG threshold、KYC required、限额)| 2 |
| 各服务接 OPA(identity / wallet / bet)| 3 |
| RG Score 跨场景计算 | 1.5 |
| 合规告警 + 通知流程 | 1 |
| 集成测试 + smoke_m12.sh | 1 |
| 调试 + 缓冲 | 0-2 |
| **小计** | **10-12** |

---

## M13: 端到端联调

**目标**:一个真用户从注册到提现的完整路径跑通,P95 延迟达标。

| 子任务 | 工日 |
|---|---|
| 全链路 trace 检测(OpenTelemetry)| 2 |
| 性能压测(k6 / Locust)| 2 |
| 跨服务延迟优化 | 2 |
| Chaos 测试(任一服务挂掉的降级)| 2 |
| smoke_m13.sh(完整闭环)| 1 |
| 报告 + Dashboards | 1 |
| 缓冲(总会有惊喜)| 0-5 |
| **小计** | **10-15** |

---

## 总和

| | 工日 |
|---|---|
| M0 (已完成) | 5-7 |
| M1-M13 | 137-173 |
| **合计** | **142-180** |
| **月数(20 工日/月)** | **7-9 个月** |
| **日历时间(80% 利用率,含周末/假期/会议)** | **9-11 个月** |

---

## 我**不**算的成本(但你应该考虑)

| 成本类 | 估算 |
|---|---|
| LLM API 费用(M5+ 开发) | $500-2,000/月 |
| 云资源(开发 + staging) | $200-500/月 |
| Sportradar / Genius Sports 数据(测试)| $0-3,000/月(取决于谈判) |
| Pragmatic Solutions sandbox 申请 | 6-12 周等待,可能 $0-10K 入场 |
| 法律审查(KOL 合约、博彩合规)| 一次性 $10K-30K |
| 第三方 pen test(M13 准备 production)| $15K-40K |

这些不影响"工程实现"工日数,但决定项目能否真上线。

---

## 这个估算的不确定性

| 因素 | 影响 |
|---|---|
| 与 Pragmatic 的真实集成时间 | 可能多 30-90 工日 |
| LangGraph 行为调优 | 可能多 10-30 工日 |
| Multi-region 部署复杂度(K8s) | 可能多 15-40 工日 |
| 合规审查反复 | 可能多 10-20 工日 |

**带不确定性的最终估算**:**6.5 月最乐观,12-15 月最悲观,9 月最现实**。

---

## 我**不会**说的话

- ❌ "39 模块工业级实现 6 个月做完" — 物理不可能,需 8-10 工程师并行
- ❌ "AI 帮你写 95% 代码" — AI 可以加速 30-50%,不是 95%
- ❌ "M0 之后剩下全自动" — 每个 M 都需要主动设计 + 决策,AI 是助手不是工程师

诚实定义结束。
