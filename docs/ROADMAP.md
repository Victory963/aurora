# Aurora 13 阶段路线图

每个 M(milestone)的核心约定:**完成 = 可被一个 `scripts/smoke_m<n>.sh` 真实跑通**,而不是"骨架就绪"。

| M | 名称 | 状态 | 核心交付 | 验收脚本 | 高工工日 |
|---|---|---|---|---|---|
| **M0** | 工程基线 | ✅ DONE | 2 服务 + 端到端 | `smoke_m0.sh` | 5-7 |
| **M1** | Aurora Event Mesh | ✅ DONE | Kafka + NATS + Schema Registry + 第一批 Avro;identity 发 `user.lifecycle.created.v1` | `smoke_m1.sh` | 8-10 |
| **M2** | identity 完整化 | ✅ DONE | 密码登录 + JWT access/rotating-refresh + 设备 + 会话 + 账户删除(发 deleted 事件) | `smoke_m2.sh` | 7-9 |
| **M3** | wallet-svc(OSS) | ✅ DONE | 双记账本 + 幂等 Debit/Credit + transactional outbox + 消费 user.created 开钱包 | `smoke_m3.sh` | 12-15 |
| **M4** | room-svc | ✅ DONE | 房间/成员(PG)+ 聊天(ScyllaDB)+ NATS 实时信号(首个 NATS producer)+ JWT 鉴权 | `smoke_m4.sh` | 10-12 |
| **M5** | ai-agent-svc 真实化 | ✅ DONE | LangGraph 状态机 + 真 LLM(Claude API,默认 opus)+ 工具接地 + Redis 检查点 + 降级模板 | `smoke_m5.sh` | 15-20 |
| **M6** | bet-svc + Pool | ✅ DONE | Closed Pool + parimutuel 结算 + wallet 幂等扣/结 + 直发 Kafka 生命周期事件 | `smoke_m6.sh` | 12-15 |
| **M7** | AI in Room | ✅ DONE | room-svc `ProposeAiPool` 编排 M4+M5+M6:AI 提议 → 群体下注 → 结算全闭环 | `smoke_m7.sh` | 8-10 |
| **M8** | Identity Graph | ✅ DONE | graph-svc + Neo4j:事件投影(幂等/可重建)+ 共现/Sybil/KOL 查询 + PII 边界 + GDPR 删除 | `smoke_m8.sh` | 10-12 |
| M9 | Pragmatic Adapter | ⏳ | Facade + mock Pragmatic server | `smoke_m9.sh` | 12-15 |
| M10 | Edge Gateway | ⏳ | Cloudflare Workers + 鉴权 | `smoke_m10.sh` | 8-10 |
| M11 | KOL Persona MVP | ⏳ | 单 KOL Few-shot + 订阅模式 | `smoke_m11.sh` | 15-18 |
| M12 | 合规层 | ⏳ | OPA + RG check 跨服务统一 | `smoke_m12.sh` | 10-12 |
| M13 | 端到端联调 | ⏳ | 一个真用户跑完完整闭环 + 性能基线 | `smoke_m13.sh` | 10-15 |
| | **总计** | | | | **142-180 工日** |

按"1 高级工程师全职"计:**6.5-9 个月**。详细工时拆解见 [WORK_ESTIMATE.md](WORK_ESTIMATE.md)。

---

## 阶段之间的依赖

```
M0 ──► M1 ──► M2 ──► M3 ──┐
              │           ├──► M6 ──► M7 ──► M13
              │           │           ▲
              └──► M4 ────┘           │
                                       │
M5 (并行 from M1) ─────────────────────┤
M8 (并行 from M3) ─────────────────────┤
M9 (并行 from M3) ─────────────────────┤
M10 (并行 from M2) ────────────────────┤
M11 (并行 from M5+M7) ─────────────────┤
M12 (并行 from M2+M3) ─────────────────┘
```

**当前进度**:M0 ✅、M1 ✅、M2 ✅、M3 ✅、M4 ✅、M5 ✅、M6 ✅、M7 ✅。
**关键路径 M0→M7 全部落地**:demo 的标志性闭环(AI 在房间提议 Pool → 群体下注 → 结算)现已可
`make smoke-m7` 真实跑通。下一步推荐 **M8**(Identity Graph)/ **M9**(Pragmatic 接真钱)/ **M12**(合规层),
或 **M13** 端到端联调 + 性能基线。

**关键路径**:M0 → M1 → M2 → M3 → M6 → M7 → M13(顺序),约 90 工日。
**可并行**:M8、M9、M10、M11、M12,把总日历时间从 8 月压到 6 月。

## 每个 M 的"完成"定义

不是"代码写完了",而是 4 个条件都满足:

1. ✅ `git pull` 后 `make up && make smoke-m<n>` 一次跑通
2. ✅ 每个新服务有 `README.md`,描述范围+边界+未来扩展
3. ✅ 新的关键决策有 ADR(`docs/adr/`)
4. ✅ `CLAUDE.md` 的"当前阶段"和"已知限制"两段更新

如果其中任一不满足,**这个 M 没结束**。

## 各 M 的"不范围"约定

每个 M 起 README 都明确列出"M<n>**不**包含什么 → 留给 M<m>"。这是为了:
- 防止范围蔓延
- 让后续 M 有明确目标
- Code review 时一眼看出是不是越界了

举例 M3 的"不范围":
- 真钱钱包(等 M9 Pragmatic Adapter)
- 跨账户转账(等 M6 拿到结算需求后再设计)
- 反欺诈 ML 模型(等 M12 合规层)

## 看下一阶段的细节

每个 M 进行前,先写一个 `docs/m<n>/PLAN.md`(类似 M0 的 PLAN),才开始编码。
