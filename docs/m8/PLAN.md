# M8 实施计划 — Identity Graph(Neo4j + 异步投影)

> 配套 [ADR-0008](../adr/0008-identity-graph.md)。验收:`make up && make smoke-m8`。

## 验收口径(smoke_m8.sh)

1. graph-svc `/healthz` OK(Neo4j + Kafka 详情)。
2. 三个用户 A/B/C:A 建房,B/C 加入;两个 Pool 里 A 先押、B 每次**跟着 A 同向**押,C 反向押。
3. 轮询(投影异步,≤60s):
   - `CoBettors(A)` 同时包含 B 和 C(共现);B 的 shared_pools = 2。
   - `SybilCandidates(A, min_shared=2)` 命中 B(2 个 Pool 同向)且**不含** C(反向)。
   - `Influence(A)` followers ≥ 1、follow_events ≥ 2(B 跟注两次)。
4. **PII 分离**:`GetUserGraph(A)` 返回里没有 email / display_name 字段。
5. **GDPR**:identity `DeleteUser(B)` → 轮询直到 `CoBettors(A)` 不再含 B(节点已 DETACH DELETE)。
6. 未带 Bearer 调查询 → 401。

## 交付物

- `services/graph-svc/`(Go 1.25,端口 8087):config / auth(第 4 份 JWT 拷贝)/
  graph(Neo4j 驱动封装 + Cypher)/ projector(2 topic 消费者)/ server(4 个查询 RPC + Health)。
- `libs/proto/aurora/graph/v1/graph.proto`。
- compose:`neo4j:5-community`(堆 256m,7474/7687)+ graph-svc。
- `scripts/smoke_m8.sh`;Makefile `smoke-m8` / `test-graph` / `tidy-graph`;wait_healthy 加 8087。
- 文档:ADR-0008、本 PLAN、服务 README、CLAUDE.md / ROADMAP 更新。

## 不在 M8 范围(留给对应里程碑)

- 真风控模型 / 决策引擎(M12 合规层;这里只是启发式 demo 查询)。
- 设备指纹 / IP 图信号(事件里还没有;M9+ 数据源真实化后加)。
- Neo4j Causal Cluster、投影拆分独立部署、DLQ / lag 指标(生产化阶段)。
- 共享 auth lib(M10)。

## 与估算对照(WORK_ESTIMATE M8 = 10-12 工日)

| 子任务 | 状态 |
|---|---|
| ADR(Neo4j vs Dgraph + PII 分离)| ADR-0008 |
| compose 加 Neo4j | ✅ 计划内 |
| Cypher schema(约束/索引)| graph.go `EnsureSchema` |
| 投影服务(Kafka → Cypher)| projector 包 |
| 3 个 demo 查询端点 | CoBettors / SybilCandidates / Influence(+GetUserGraph)|
| GDPR 删除流程 | deleted 事件 → DETACH DELETE + smoke 验证 |
| 集成测试 | 单测(fake Graph)+ smoke_m8 全链路 |
