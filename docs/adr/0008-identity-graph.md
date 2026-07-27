# ADR-0008:Identity Graph —— Neo4j + 异步投影(M8)

- **状态**:Accepted(M8 实施)
- **日期**:2026-07-09
- **前置**:M1(事件网格)、M2(identity 事件)、M6(bet 事件)

## 上下文

社交博彩平台需要"关系视角":谁和谁共同下注(共现)、谁在带节奏(KOL 影响力/风险)、
哪些账号疑似同一人操纵(Sybil)。这些是图查询,在关系型库里是多层自 JOIN,表达笨重且慢。
M1 起所有关键事实都已经以事件形式流经 Kafka —— 图可以作为**纯投影**(read model)异步构建,
不侵入任何既有服务。

## 决策

### 1. Neo4j(单节点 dev)+ 独立 graph-svc(Go,端口 8087)

- **Neo4j vs Dgraph vs Postgres 递归 CTE**:Neo4j 生态最成熟(Cypher 表达力、驱动质量、运维资料);
  Dgraph 团队/维护状态波动大;PG 递归 CTE 在 3 跳共现查询上既难写又难优化。dev 用
  `neo4j:5-community` 单节点(堆 256m/页缓存 128m,适配本机内存);生产 Causal Cluster 留部署阶段。
- graph-svc 同时承担**投影**(Kafka consumer → Cypher MERGE)与**查询**(HTTP RPC),
  两者共享一个 Neo4j 驱动;拆分留到量级需要时。

### 2. 纯事件投影,at-least-once + MERGE 幂等

- 消费 `aurora.identity.user-lifecycle.v1`(User 节点的建/删)与 `aurora.bet.lifecycle.v1`
  (BET 边 + Pool 节点;settled 事件更新 Pool 状态)。
- 沿用 wallet-svc 的消费模式:`FetchMessage → 处理成功 → CommitMessages`(at-least-once);
  投影全部用 `MERGE` 按业务键(user.id / pool.id / bet_id)写入,**重放天然幂等**。
- 毒消息(解析失败)记日志后跳过提交,不阻塞消费组;Graph 写失败不提交、重试。
- 首次启动 `StartOffset=FirstOffset`,重放全部历史 → 图自动收敛;**图可随时删库重建**。

### 3. PII 分离(硬边界)

- 图中 User 节点**只存**:`id`(UUID)、`kyc_country`、`created_at_ms`。
  **email / display_name 等 PII 一律不入图**(事件里有,投影时丢弃)。
- 查询结果只返回 user_id;调用方需要展示名时自己拿着 id 问 identity-svc(权限在那边)。

### 4. GDPR 删除 = **墓碑(tombstone)**,不是硬删

- 消费 `user.lifecycle.deleted.v1` → 删掉全部边与属性,但**保留** `(u:User {id, deleted:true})`
  墓碑节点(id 是不透明 UUID,零个人数据);Pool 聚合(总注额等)在 bet-svc/wallet,不受影响。
- **为什么不能 DETACH DELETE**(评审确认的真实漏洞):identity 与 bet 是**两个 topic,无跨序**。
  硬删后,一条迟到/重投递的 `bet.placed.v1` 会 `MERGE (u:User)` 把已删除用户连同下注史
  **复活**,且再无删除事件到来 —— GDPR 静默失效。墓碑 + `UpsertBet/UpsertUser` 带
  `WHERE coalesce(u.deleted,false)=false` 守卫,使删除**真正终态**:任何顺序重放收敛到墓碑。
- 读查询把墓碑视为不存在(无边可达;`GetUserGraph` 显式过滤)。

### 5. 三个 demo 查询(WORK_ESTIMATE 钦定)

| RPC | 图语义 | 用途 |
|---|---|---|
| `CoBettors` | 与我共同下注过的用户,按共享 Pool 数排序 | 共现/社交推荐 |
| `SybilCandidates` | 共享 Pool ≥ N 且**同向下注**、注册时间接近的账号对 | 多账号操纵初筛 |
| `Influence` | 在我之后、同 Pool 同选项跟注的人数/次数/平均延迟 | KOL 影响力→风险分 |

- 全部 Bearer JWT 鉴权(行为数据敏感);HS256 校验是**第 4 份拷贝**(债务同 ADR-0005/0006,共享 lib 留 M10)。

## 后果 / 债务(登记 CLAUDE.md)

- 投影有**秒级延迟**(消费+写图);smoke 用轮询等待收敛。
- 无 DLQ / 无消费监控指标;bet 事件本身直发可丢(ADR-0006)→ 图是**尽力而为**的视图,
  不是审计源(钱以 wallet 为准)。
- 单节点 Neo4j 无副本,dev only;JWT 第 4 份拷贝。
- Sybil/KOL 是**启发式 demo 查询**,不是风控模型(真风控留 M12 合规层)。

## 替代方案

| 方案 | 拒绝理由 |
|---|---|
| Dgraph | 维护状态不稳,生态小,团队几次重组 |
| PG 递归 CTE 在 identity 库内 | 跨服务共库违反仓库红线;多跳查询表达/性能都差 |
| 投影进 identity-svc | identity 是权威源,不该背 read-model;图可删库重建,边界干净 |
| 同步双写(服务直接写图)| 耦合 + 一致性地狱;事件已是事实来源,投影免费获得重放/重建 |
