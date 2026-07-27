# M7 实施计划 — AI in Room 联调

> 配套 [ADR-0007](../adr/0007-ai-in-room.md)。验收:`make up && make smoke-m7`。

## 验收口径(smoke_m7.sh)

1. A 建房、B 加入(M4);两人钱包充值(M3)。
2. A 调 `room.ProposeAiPool(room_id, match_id)` → 200:含 recommendation(带工具审计
   data_sources)、pool(bet-svc 真实创建)、AI 聊天消息。**无 ANTHROPIC_API_KEY 时走 M5
   降级模板,流程照样通**。
3. `ListMessages` 里能看到 `aurora-ai` 的提议消息;NATS in_msgs 增长(ai_proposal 信号)。
4. B 对该 Pool 押注(wallet 扣款)→ A 结算 → 余额变化正确 —— **demo 全闭环落地**:
   AI 提议 → 群体下注 → 结算。
5. 非成员触发 ProposeAiPool → 403。

## 交付物

room-svc:`ProposeAiPool` handler + ai/bet HTTP 客户端(injectable,单测用 httptest 假服务)
+ config(`AURORA_ROOM_AI_URL`/`AURORA_ROOM_BET_URL`)+ proto 扩展 + NATS `ai_proposal` 事件;
compose env;smoke_m7.sh;Makefile target。

## 不在 M7 范围

比赛事件自动触发(消费 Kafka match 事件;事件源 M9 才真实,自动化留 M13)、AI 主动巡房、
WebSocket 推送(M10)。
