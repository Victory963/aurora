# graph-svc — Aurora Identity Graph(M8)

Kafka 事件 → Neo4j 的**纯投影**(read model)+ 图查询 RPC。见 [ADR-0008](../../docs/adr/0008-identity-graph.md)。

## 范围

- 消费 `aurora.identity.user-lifecycle.v1`(User 节点建/删)与 `aurora.bet.lifecycle.v1`
  (BET 边 + Pool 节点)。at-least-once + MERGE 幂等;首启从最早 offset 重放,图可删库重建。
- **PII 边界**:图里只有 `user.id` / `kyc_country` / `created_at_ms`;email、display_name
  在投影层丢弃,永不入图。查询只返回 user_id,且**只能查自己**(user_id 必须等于 token sub;
  分析师/平台侧访问是 M12 的授权课题)。
- **GDPR**:`user.lifecycle.deleted.v1` → 删边删属性,留 `{id, deleted:true}` **墓碑**;
  写入守卫保证迟到的 bet 事件不会复活用户(两 topic 无跨序,硬删会被 MERGE 复活 —— 评审确认)。
- 投影失败**原地重试**同一 offset(不跳过、不丢事件);毒消息(解析失败)跳过。
- `SybilCandidates` 的 `created_gap_ms = -1` 表示**未知**(某侧注册时间尚未投影),排序时排最后。

## 端点(端口 8087,全部 Bearer JWT)

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/aurora.graph.v1.GraphService/CoBettors` | 共同下注过的用户(共现),按共享 Pool 数排序 |
| POST | `/aurora.graph.v1.GraphService/SybilCandidates` | 同向共注 ≥ min_shared 的疑似关联账号 |
| POST | `/aurora.graph.v1.GraphService/Influence` | 跟注人数/次数/平均延迟(KOL 信号)|
| POST | `/aurora.graph.v1.GraphService/GetUserGraph` | 用户节点 + 全部 BET 边(无 PII)|
| GET/POST | `/healthz` · `.../HealthCheck` | Neo4j 连通 + projection 开关状态 |

请求都是 `{"user_id": "...", ...}`;错误统一 `{code, message}`。

## 图模型

```
(:User {id, kyc_country, created_at_ms})
  -[:BET {bet_id, selection, stake_minor, at_ms}]->
(:Pool {id, room_id, settled?, winning_option_id?, total_stake_minor?, rake_minor?})
```

唯一约束:`User.id`、`Pool.id`(`EnsureSchema` 启动时建)。

## 配置(env)

| 变量 | 默认 | 说明 |
|---|---|---|
| `AURORA_GRAPH_HTTP_ADDR` | `:8087` | |
| `AURORA_GRAPH_NEO4J_URI` | `bolt://localhost:7687` | |
| `AURORA_GRAPH_NEO4J_USER/PASS` | `neo4j` / dev 密码 | |
| `AURORA_GRAPH_KAFKA_BROKERS` | *(空 = 投影关闭,健康降级)* | |
| `AURORA_GRAPH_IDENTITY_TOPIC` | `aurora.identity.user-lifecycle.v1` | |
| `AURORA_GRAPH_BET_TOPIC` | `aurora.bet.lifecycle.v1` | |
| `AURORA_GRAPH_GROUP_ID` | `graph-svc` | |
| `AURORA_GRAPH_JWT_SECRET` | dev 共享密钥 | HS256(第 4 份拷贝,M10 收敛)|

## 测试

```bash
make test-graph     # 单测:projector(事件→投影,fake store)+ server(查询/鉴权/PII)
make smoke-m8       # 全链路:投影收敛 / 共现 / Sybil / 影响力 / GDPR 删除 / 401
```

## M8 不包含(留给后续)

真风控模型与决策(M12)、设备/IP 图信号(M9+)、Neo4j 集群与投影独立部署(生产化)、
共享 auth lib(M10)。Sybil/KOL 查询是**启发式 demo**,不是合规判定。
