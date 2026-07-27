# ADR-0007:AI in Room —— M4+M5+M6 联调(M7)

- **状态**:Accepted(M7 实施)
- **日期**:2026-07-05
- **前置**:M4(room)、M5(真 AI)、M6(bet pools)

## 上下文

demo 的标志性时刻:比赛关键时刻,AI 在房间里发消息并提议一个 Group Pool。M7 把三个已建成
的服务接成这个真实闭环。

## 决策

**编排放在 room-svc**(它拥有聊天写入、NATS 信号与成员校验):新 RPC `ProposeAiPool`
(Bearer,房间成员触发):

```
member → room.ProposeAiPool(room_id, match_id)
  1. ai-agent.Recommend(mode=ROOM, user_id=caller, match_id, room_id)   [HTTP, 25s 超时]
  2. 取 top 推荐 → bet.CreatePool(room_id, question, YES/NO 选项)       [转发调用者 Bearer, 5s]
  3. Scylla 写入一条 AI 聊天消息(user_id="aurora-ai":平台角色,服务端写入,
     不走成员校验 —— AI 不是房间成员,是产品能力)
  4. NATS `aurora.room.<id>.ai_proposal` 信号(pool_id + question)
  5. 返回 {recommendation, pool, message}
```

要点:
- **Pool 创建者 = 触发的成员**(转发其 token → bet 侧成员校验/结算权限天然成立,
  与 demo"太郎发起 Pool"一致);AI 只"提议"。
- AI 不可用/无推荐 → 502(`ai_unavailable`/`ai_no_recommendation`),**不建 Pool 不发消息**
  (半完成状态最糟);bet 不可用 → 502 `bet_unavailable`。
- NO 选项指示赔率 = 公平互补 `B = A/(A-1)`(仅显示用,结算是 parimutuel,见 ADR-0006)。
- 触发是**手动 RPC**(成员点按钮)。比赛事件自动触发(消费 match.event.goal.v1)留 M13 联调阶段
  —— 事件源本身要到 M9 才真实。

## 后果 / 债务

- room-svc 新增对 ai/bet 的运行时依赖(带超时;未配置 URL → 503 `not_configured`,
  room 其余功能不受影响)。
- "aurora-ai" 是保留 user_id 约定(不在 identity 注册;客户端按此渲染 AI 徽标)。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| ai-agent-svc 编排 | 它无聊天/NATS/成员上下文,还得反向调 room;server.py 要动(违 ADR-0001)|
| bet-svc 编排 | 同上,聊天写入不属于 bet 域 |
| 服务间专用 service token | 转发用户 token 更简单且权限语义正确;服务身份体系留 M10 |
