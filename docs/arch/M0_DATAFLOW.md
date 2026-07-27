# M0 数据流图

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         M0 端到端数据流                                 │
└─────────────────────────────────────────────────────────────────────────┘

   ┌──────────┐
   │  curl    │  ◄── developer / smoke_m0.sh
   │ (client) │
   └────┬─────┘
        │
        │ POST /aurora.identity.v1.IdentityService/CreateUser
        │ {email, display_name, kyc_country}
        │
        ▼
   ┌─────────────────────────────────┐
   │      identity-svc (:8081)       │
   │  ┌──────────────────────────┐   │
   │  │  internal/server/        │   │
   │  │   handleCreateUser       │   │
   │  └─────────────┬────────────┘   │
   │                │                 │
   │  ┌─────────────▼────────────┐   │
   │  │  internal/store/         │   │
   │  │   CreateUser (pgx)       │   │
   │  └─────────────┬────────────┘   │
   └────────────────┼─────────────────┘
                    │ INSERT INTO users
                    │ (id, email, ...)
                    ▼
              ┌──────────┐
              │ Postgres │
              │  :5432   │
              │          │
              │  users   │
              │ ┌──────┐ │
              │ │ id   │ │
              │ │email │ │
              │ │ ...  │ │
              │ └──────┘ │
              └──────────┘

  ─────────  返回 user_id  ─────────►

   ┌──────────┐
   │  curl    │
   │ (client) │
   └────┬─────┘
        │
        │ POST /aurora.ai.v1.AIAgentService/Recommend
        │ {user_id, mode, match_id, room_id}
        │
        ▼
   ┌─────────────────────────────────┐         ┌──────────────────┐
   │      ai-agent-svc (:8082)       │         │   identity-svc   │
   │  ┌──────────────────────────┐   │ 验证    │      (:8081)     │
   │  │   server.py              │───┼────────►│  GetUser         │
   │  │    /Recommend            │   │  user   │                  │
   │  └─────────────┬────────────┘   │ ◄───────│                  │
   │                │ user exists    │ 200 OK  └──────────────────┘
   │                │                 │
   │  ┌─────────────▼────────────┐   │
   │  │   agent.py               │   │
   │  │    generate_recommend()  │   │   ← M0: hardcoded
   │  │                          │   │   ← M5: LangGraph
   │  └─────────────┬────────────┘   │
   │                │                 │
   │  ┌─────────────▼────────────┐   │
   │  │  return RecommendResponse │   │
   │  │    recommendations[]      │   │
   │  │    correlation_id         │   │
   │  │    latency_ms             │   │
   │  └──────────────────────────┘   │
   └─────────────────────────────────┘
                    │
                    │ 200 OK + JSON
                    ▼
              ┌──────────┐
              │  client  │
              │  prints  │
              │  result  │
              └──────────┘


┌─────────────────────────────────────────────────────────────────────────┐
│                    M0 关键设计点                                        │
└─────────────────────────────────────────────────────────────────────────┘

1. ai-agent-svc 真的调用 identity-svc 验证用户存在
   → 证明"跨服务通讯"plumbing 已通

2. URL 路径使用 Connect-RPC 约定:
     POST /<proto-package>.<Service>/<Method>
   M1+ 可以无缝切到 Connect-RPC 二进制协议,客户端 URL 不变

3. identity-svc 和 ai-agent-svc 用 docker 网络通信:
   service name `identity-svc` 在 ai-agent-svc 容器内可解析

4. Postgres 数据持久化在命名 volume `aurora_postgres_m0`
   `make down` 不会删数据,`make clean` 才删

5. 健康检查链:
   docker healthcheck → service /healthz → service 检查 DB
   make wait-healthy 等到全绿


┌─────────────────────────────────────────────────────────────────────────┐
│                    M0 不包含(后续阶段补)                                │
└─────────────────────────────────────────────────────────────────────────┘

✗ 事件发布 (M1: Kafka)
✗ 鉴权 (M2: JWT)
✗ 钱包 (M3: wallet-svc + OSS PAM)
✗ 房间 (M4: room-svc)
✗ 真实 LLM (M5: LangGraph + Claude API)
✗ 群体投注 Pool (M6)
✗ Identity Graph (M8: Neo4j)
✗ Pragmatic 集成 (M9)
✗ Edge Gateway (M10: Cloudflare Workers)
✗ KOL Persona (M11)
✗ OPA 合规 (M12)
✗ 性能压测 (M13)
```
