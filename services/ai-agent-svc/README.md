# ai-agent-svc

Aurora AI 推荐服务。**M5 已真实化:LangGraph 状态机 + 真 Claude API**(不再是硬编码)。

## M5 范围(真 AI)

- **LangGraph 状态机**(`aurora_ai/pipeline.py`):`ingest → rg_check →(blocked?)→ retrieve → mode_context →
  llm_reason → rank_and_quality → audit_log`。LangGraph 不可用时**同语义顺序 runner** 兜底。
- **真 Claude API**(`aurora_ai/llm.py`):默认 `claude-opus-4-8`,结构化输出走 `output_config.format=json_schema`;
  **不发** temperature/top_p/thinking(Opus 4.7+ 会 400)。
- **工具接地**(`aurora_ai/tools.py`):数字**只来自工具**(player stats / odds / alt-odds / RG / room sentiment),
  LLM 只选市场 + 写推理。`rank_and_quality` **重新挂上工具赔率并重算 EV** —— LLM 无法注入假数字;
  引用不存在的市场会被质量门丢弃。
- **Redis 检查点**(compose `redis`);连不上降级 `MemorySaver`。
- **降级阶梯**(每级仍返回 200):完整 LLM → 顺序 runner → **模板推荐**(无 key / LLM 超时 / 质量门全丢)→ RG BLOCKED 短路(空推荐)。
- **审计**:每条推荐的 `data_sources` 带工具标签(`tool:...` / `room:...`)+ 引擎标记(`llm:<model>` 或 `fallback:template`)。

**接口稳定约定(ADR-0001)**:`agent.py::generate_recommendations(user_id, mode, match_id, room_id) -> list[dict]`
签名**不变**,`server.py` 零改动。M7 room-svc 以 `mode=ROOM` 调用它做房内提议。

见 [ADR-0001](../../docs/adr/0001-ai-agent-langgraph.md)、[PLAN](../../docs/m5/PLAN.md)。

## 端点

| 方法 | 路径 |
|---|---|
| POST | `/aurora.ai.v1.AIAgentService/Recommend` |
| GET/POST | `/aurora.ai.v1.AIAgentService/HealthCheck` |
| GET | `/healthz` |

### Recommend 例子

```bash
curl -X POST http://localhost:8082/aurora.ai.v1.AIAgentService/Recommend \
  -H "Content-Type: application/json" \
  -d '{"user_id":"<existing-user-uuid>","mode":1,"match_id":"j1.urawa-vs-fctokyo","room_id":""}'
```

`mode` 取值:`1=SOLO`, `2=ROOM`, `3=KOL_PERSONA`

## 本地开发

```bash
cd services/ai-agent-svc
pip install -e ".[dev]"
uvicorn aurora_ai.server:app --reload --port 8082
```

## 测试

```bash
pytest tests/ -v
```

## 配置

| Env | 默认 | 说明 |
|---|---|---|
| `AURORA_AI_IDENTITY_URL` | `http://localhost:8081` | identity-svc 的 base URL |
| `ANTHROPIC_API_KEY` / `AURORA_AI_ANTHROPIC_API_KEY` | `` (空) | Claude API key;**空=走降级模板**(smoke 仍 200)|
| `AURORA_AI_MODEL` | `claude-opus-4-8` | Claude 模型 ID |
| `AURORA_AI_LLM_TIMEOUT_S` | `20` | 单次 LLM 调用超时(秒)|
| `AURORA_AI_LLM_EFFORT` | `low` | `output_config.effort`(low..max)|
| `AURORA_AI_REDIS_URL` | `` (空) | LangGraph 检查点;空=内存 `MemorySaver` |

在 docker-compose 中通过 service name 连通(identity=`http://identity-svc:8081`,redis=`redis://redis:6379`)。
**无 `ANTHROPIC_API_KEY` 时服务照常工作**(确定性模板推荐,基于同一批工具数据),`make smoke-m5` 仍通过。
