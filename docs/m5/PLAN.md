# M5 实施计划 — ai-agent-svc 真实化(LangGraph + Claude API)

> 配套 [ADR-0001](../adr/0001-ai-agent-langgraph.md)(Proposed → 本次落地,含偏差说明)。
> 验收:`make up && make smoke-m5` —— 无 API key 走降级仍 200;有 key 时出现真实 LLM 推理痕迹。

## 目标(验收口径)

1. `agent.py::generate_recommendations` **签名不变**(server.py 零改动,ADR-0001 约定)。
2. 内部换成 **LangGraph 状态机**:ingest → rg_check → retrieve(stats/odds/alt_odds)→
   模式分支(room_sentiment / persona_card)→ llm_reason → rank → quality_gate → audit。
3. **LLM 只用工具数据**:所有数字(赔率/统计)来自工具输出;quality_gate 校验 LLM 给出的
   market/odds 必须与工具数据一致,置信度 ∈ [0,1],否则丢弃该条。
4. 每条推荐的 `data_sources` = **工具调用审计**(`tool:*`)+ 引擎标记(`llm:<model>` 或 `fallback:template`)。
5. **降级阶梯**:无 key / anthropic 或 langgraph 不可用 / LLM 超时或输出不合格 →
   用**同一批工具数据**生成模板推荐(deterministic),服务永不 500、不依赖外网。
6. RG 硬关卡:`rg_check` BLOCKED → 返回空推荐(200)。deterministic 测试钩子:user_id 含 `rgblock`。
7. Redis 检查点:compose 加 `redis`;`AURORA_AI_REDIS_URL` 存在时用 RedisSaver,失败回退 MemorySaver。

## 与 ADR-0001 的偏差(如实记录)

| ADR-0001 | 本次实现 | 原因 |
|---|---|---|
| retrieve_* 并行(Send API) | 顺序执行 | 工具是本地 mock,毫秒级;并行留真实外部源(M9)|
| 工具接 Sportradar/Pragmatic/compliance | **确定性 mock**(match_id/user_id 哈希种子) | 真数据源是 M9/M12 范围 |
| audit 写 Kafka | 审计进响应 `data_sources` + 日志 | Kafka 审计统一留 M12 合规层 |
| P95 1340ms | 真 LLM 不承诺;fallback 路径毫秒级 | 诚实记录 |
| LLM 主 Sonnet | 默认 **claude-opus-4-8**(`AURORA_AI_MODEL` 可换) | 平台默认最新最强;Opus 4.8 上不发 temperature(API 会 400) |

## 模块

```
aurora_ai/
  config.py     # AURORA_AI_MODEL / LLM_TIMEOUT_S / LLM_EFFORT / REDIS_URL / key 解析
  tools.py      # mock 工具:player_stats / current_odds / alt_odds / rg_check(+审计标签)
  personas.py   # KOL persona 卡(M11 MVP 子集:few-shot 风格提示)
  llm.py        # Anthropic 封装:structured output(output_config.format json_schema)+ typed 异常
  pipeline.py   # LangGraph StateGraph + 顺序 fallback runner + 检查点选择
  agent.py      # generate_recommendations(签名不变):跑管线 + 降级阶梯
  server.py     # 不动
```

## 不在 M5 范围

- ❌ 真实数据源(Sportradar/Pragmatic)→ M9;❌ Kafka 审计 → M12;❌ KOL 完整订阅/LoRA → M11;
- ❌ 流式输出 / server.py 改造(阻塞调用在单 worker dev 可接受,已记录限制)→ M10/M13。

## 已知限制(登记 CLAUDE.md)

- server.py 的同步 `generate_recommendations` 在 async handler 里**阻塞事件循环**至多 LLM 超时时长(dev 单 worker 可接受;M10 网关/M13 性能阶段处理)。
- 工具数据是 mock;LLM 推理真实但底层数字非真实行情。
