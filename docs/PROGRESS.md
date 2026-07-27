# Aurora 详细进度报告

> 快照日期:2026-07-09。完成定义(仓库红线):`make up && make smoke-m<n>` 一次真实跑通 +
> 服务 README + ADR + CLAUDE.md 更新,四者齐才算 DONE。

## 总进度:M0–M8 ✅(9/14 里程碑,约 60% 工日)

**本机实测状态**:15 个容器全部 healthy;`smoke_m0..m8` 九个验收脚本全部真实 PASS;
六个服务的单元测试在容器内真实执行通过;demo 控制台 http://localhost:8090 可视化跑全闭环。

| M | 名称 | 状态 | 真实验证证据 |
|---|---|---|---|
| M0 | 工程基线 | ✅ | `smoke_m0`:两服务 happy path + 健康检查 |
| M1 | Event Mesh | ✅ | `smoke_m1`:Kafka 事件出现、Schema Registry 注册、NATS 就绪 |
| M2 | identity 完整化 | ✅ | `smoke_m2`:登录/刷新轮换防重放/吊销/自删 + deleted 事件 |
| M3 | wallet-svc | ✅ | `smoke_m3`:幂等重放不双扣、双记对称、outbox 事件到 Kafka、事件自动开钱包 |
| M4 | room-svc + chat | ✅ | `smoke_m4`:Scylla 聊天持久化、NATS in_msgs 增长、非成员 403 |
| M5 | ai-agent 真实化 | ✅ | `smoke_m5`:工具审计链完整、数字与工具一致、rgblock→0 推荐、无 key 走模板仍 200 |
| M6 | bet-svc 彩池 | ✅ | `smoke_m6`:A 1000 YES/B 600 NO→结算 A=5570/B=4400 分毫不差;重放幂等;Kafka 两类事件 |
| M7 | AI in Room | ✅ | `smoke_m7`:ProposeAiPool 全闭环(AI 推荐→建 Pool→AI 消息→NATS→群体下注→结算) |
| M8 | Identity Graph | ✅ | `smoke_m8`:投影收敛、共现、Sybil 同向命中/反向排除、KOL 跟注、PII 无泄漏、GDPR 删号消失、401 |
| — | Demo 控制台 | ✅(非里程碑) | 浏览器 :8090 全流程可点;同源代理消 CORS |
| M9 | Pragmatic 真钱 | ⏳ 下一个 | Facade + mock Pragmatic server(12-15 工日) |
| M10 | Edge Gateway | ⏳ | Cloudflare Workers + 共享鉴权(8-10) |
| M11 | KOL Persona MVP | ⏳ | Few-shot 人格 + 订阅(15-18) |
| M12 | 合规层 | ⏳ | OPA + 真 RG + 图谱风控授权(10-12) |
| M13 | 端到端联调 | ⏳ | 真用户完整闭环 + 性能基线 + 比赛事件自动触发 AI(10-15) |

## 质量记录(对抗性评审)

每个大交付后跑多 agent 对抗评审(发现者 → 独立复核者逐条证伪 → 修复 → 回归测试):

- **M5/M6/M7 轮**(14 agents):确认 8 缺陷全修 —— 含 **rake int64 溢出铸币**(19 笔顶格注触发,
  改全程 big.Int)、**孤儿扣款丢钱路径**(改恢复重放)、钱包幂等重放参数校验加固、UTF-8 截断、
  AI 降级梯完备性。
- **M8/demo 轮**(17 agents):确认 12 缺陷全修 —— 含 **GDPR 复活漏洞**(硬删→墓碑+写守卫)、
  **投影丢事件**(跳过→原地重试同 offset)、**图谱越权查询**(绑定 token sub 仅查自己)、
  Influence 跨积膨胀、Sybil 伪造零时差、demo XSS/轮询竞态/中断结算不可恢复、
  demo-ui 健康检查 IPv6 解析永败。
- 全部修复配了**回归测试**并重跑单测 + smoke 验证。

## 单元测试清单(全部容器内真实执行)

| 服务 | 覆盖重点 |
|---|---|
| identity-svc | auth(JWT 签验/过期/iss)、events 编码、server handlers |
| wallet-svc | 幂等重放参数校验、双记、outbox(DB 集成测试 `AURORA_TEST_DB=1` 门控) |
| room-svc | 房间/成员/聊天 handlers、M7 ProposeAiPool 编排(httptest 假 ai/bet)8 用例 |
| bet-svc | poolmath 守恒/尘埃/溢出回归、全流程结算、孤儿扣款恢复、退款可重试、键冲突 |
| graph-svc | 投影(4 事件类型/毒消息/PII 丢弃/GDPR)、查询(鉴权/仅查自己/404/降级) |
| ai-agent-svc | 19 用例:降级模板、工具接地、确定性、rgblock、质量门丢幻觉、顺序 runner 等价 |

## 已知限制(详见 CLAUDE.md「当前已知限制」+ 各 ADR)

钱是 OSS 虚拟币(真钱 M9);AI 工具确定性 mock(真数据源 M9);创建者充当结算 oracle(M9/M13);
JWT 共享密钥 4 份拷贝(M10);identity/bet 事件直发无 outbox;dev 单节点存储;
Sybil/KOL 启发式非风控判定(M12);前端轮询无 WebSocket(M10)。

## 新 PC 迁移(接手指南)

1. 装 Docker Desktop(启用 WSL integration)+ `jq`;克隆本仓库。
2. `make up` → `make wait-healthy` → `make register-schemas` → `make smoke-m8`(会级联验证大部分依赖)。
3. 打开 http://localhost:8090 跑可视化闭环;Neo4j 浏览器 :7474(neo4j/aurora_dev_password)。
4. 要看真 LLM 推理:`ANTHROPIC_API_KEY=sk-ant-… make up`(无 key 走确定性模板,一切照常)。
5. 给 Claude Code 的完整上下文在 `CLAUDE.md`(约定/命令/坑);继续开发说"做 M9"即可。
