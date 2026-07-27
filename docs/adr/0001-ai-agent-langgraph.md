# ADR-0001:ai-agent-svc 的 LangGraph 状态机设计

- **状态**:Accepted —— M5 已实施(2026-07-05),带偏差:工具为确定性 mock(真数据源 M9)、
  retrieve 顺序执行、审计进响应而非 Kafka(M12)、默认模型 `claude-opus-4-8`。
  偏差清单见 `docs/m5/PLAN.md`。
- **日期**:2026-05-26(设计)/ 2026-07-05(落地)
- **覆盖 milestone**:M5

## 上下文

ai-agent-svc 需要同时服务三种模式(Solo、Room、KOL Persona)且共享底层引擎。
M0 用硬编码占位符,M5 将替换为 LangGraph 状态机。

## 决策

采用 **有向无环图(DAG)+ Redis 检查点持久化** 的 LangGraph 状态机。

### 状态机节点(13 个)

```
[entry]
  ├─► ingest_context              # 收集触发上下文
  ├─► rg_check                    # 责任博彩硬关卡
  │     └─[BLOCKED]─► audit_log → END
  │     └─[PASSED/WARNING]─► continue
  ├─► retrieve_user_pattern       # 用户历史模式
  │
  ├─► retrieve_player_stats       ┐
  ├─► retrieve_current_odds       ├─ 并行(LangGraph Send API)
  ├─► retrieve_alt_odds           ┘
  │
  ├─► [conditional split by mode]
  │     ├─[solo]──────► llm_reason
  │     ├─[room]──────► retrieve_room_sentiment ─► llm_reason
  │     └─[persona]───► load_persona_card ─► llm_reason
  │
  ├─► llm_reason                  # 主 LLM 推理(流式)
  ├─► rank_and_dedupe             # 推荐排序 + 去重
  ├─► quality_gate                # 输出质量检查
  │     └─[FAIL]──► llm_reason(retry, max 1)
  │     └─[PASS]──► audit_log
  ├─► audit_log                   # 写入审计 + Kafka
  └─► END
```

### 接口稳定性约定

M5 替换 `agent.py::generate_recommendations()` 时,函数签名**不变**:

```python
def generate_recommendations(
    user_id: str,
    mode: Mode,
    match_id: str,
    room_id: str,
) -> list[dict[str, Any]]:
```

调用方(`server.py`)**不需要修改**。这是 M0 设计的核心理由。

### 性能目标

| 节点 | P95 目标 | 硬超时 |
|---|---|---|
| ingest_context | 50ms | 200ms |
| rg_check | 80ms | 300ms |
| retrieve_*(并行) | 250ms | 800ms |
| llm_reason | 800ms | 2000ms |
| rank_and_dedupe | 80ms | 300ms |
| quality_gate | 50ms | 200ms |
| audit_log | 30ms | 100ms |
| **端到端 P95** | **1340ms** | **3900ms** |

### 工具(6 个)

LLM 必须通过工具获取数据,不允许凭空生成数字。

| 工具 | 数据源 | 缓存策略 |
|---|---|---|
| `GetPlayerStatsTool` | Sportradar / Genius | Redis 30s TTL |
| `GetCurrentOddsTool` | Pragmatic / Kambi | Redis 5s TTL |
| `GetUserHistoryTool` | identity-svc + bet-svc | Redis 60s TTL |
| `CheckResponsibleGamblingTool` | compliance-svc | 无缓存 |
| `ComputeKellyStakeTool` | 内部纯函数 | N/A |
| `QueryIdentityGraphTool` | identity-svc Neo4j | Redis 5min TTL |

### LLM 选型

- 主:Claude Sonnet 4.6(quality + 日语强)
- 备:Llama 3.3 70B 自托管(成本敏感场景)
- 路由策略 ADR:M5 时另写

### 错误处理与降级

4 级降级:

| 级别 | 触发 | 行为 |
|---|---|---|
| NONE | 全部正常 | 完整推理 |
| FALLBACK | LLM 超时 / 工具失败 ≤2 | cached 模板推荐 |
| MINIMAL | 工具失败 ≥3 | 只展示赔率,不推荐 |
| NONE_OUT | RG_BLOCKED 或致命错误 | 不输出 |

## 后果

**正面**:
- 状态机可视化清晰
- Redis checkpoint 让"AI 失忆"不发生
- 流式输出降低用户感知延迟 50%
- 全部工具调用可审计

**负面**:
- LangGraph 学习曲线陡峭,团队需培训
- Redis Checkpointer 增加存储成本
- 流式 + 并行调试复杂,需 LangSmith

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| LangChain AgentExecutor | 不支持复杂状态机 |
| 自研状态机 | 重复造轮子 |
| Temporal Workflow | 太重,延迟 200ms+ 启动 |
| AWS Step Functions | 锁定 AWS + 不适合流式 |
| OpenAI Assistants API | 锁定 OpenAI,不支持 Claude/Llama 切换 |
