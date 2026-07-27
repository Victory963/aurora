# M0 跑通之后:怎么开始 M1

恭喜,M0 已完成。这份指南告诉你接下来怎么做。

## 1. 先确认 M0 真跑通了

```bash
make up
make wait-healthy
make smoke
```

应该看到 `M0 SMOKE TEST PASSED`。如果没看到,**不要往下读**,先去 `docs/deploy/LOCAL_PC_SETUP.md` 的 "常用 debug 操作" 一节排查。

## 2. M1 范围速览

**M1 = Aurora Event Mesh**(Kafka + NATS + Schema Registry)

**目标**:identity-svc 在 CreateUser 时**额外**发一个 Kafka 事件 `user.lifecycle.created.v1`。任何下游(M3 wallet-svc / M8 Identity Graph)都能订阅这个事件做投影。

**M1 完成的判据**:`scripts/smoke_m1.sh` 跑通,内容是:
1. 启动 stack(含 Kafka + Schema Registry)
2. CreateUser 一个用户
3. **从 Kafka 读出这个事件**
4. 验证事件 schema 符合 Avro 定义

## 3. M1 需要做的事(高工 8-10 天)

按推荐顺序:

### Day 1:设计 + ADR

写 `docs/adr/0002-event-mesh.md`:
- 为什么 Kafka + NATS 双总线(WP3 §6 已论证,这里压缩成 ADR)
- Schema 演进策略(向后兼容、版本号)
- Topic 命名约定

### Day 1-2:docker-compose 扩容

加 4 个 service:`zookeeper`、`kafka`、`schema-registry`、`nats`。
关键:`depends_on` 用 `service_healthy` 确保启动顺序。

### Day 2-3:Avro schema

在 `libs/events/avro/` 写第一批(5 个):
- `user.lifecycle.created.v1.avsc`
- `user.lifecycle.deleted.v1.avsc`
- `match.event.goal.v1.avsc`(给 M5 用)
- `bet.placed.v1.avsc`(给 M3 用)
- `audit.action.v1.avsc`(给 M12 用)

写一个 `scripts/register_schemas.sh` 把它们注册到 Schema Registry。

### Day 3-5:identity-svc 加 Kafka producer

```go
// services/identity-svc/internal/events/publisher.go (新文件)
package events

type Publisher interface {
    UserCreated(ctx context.Context, u User) error
    Close() error
}

type KafkaPublisher struct { /* ... */ }
```

CreateUser handler 改造:**双写模式**——先写 DB,再发事件。失败时**不**回滚 DB(M1 简化),M3 引入 outbox pattern。

### Day 5-6:消费者验证

写一个独立的小工具 `scripts/kafka_consume.sh`(用 `kafkacat`),订阅 topic 打印 JSON。

### Day 6-7:smoke_m1.sh

延伸 `smoke_m0.sh`:M0 6 步 + 新加 2 步:
- 7/8:消费者读到 user.created 事件
- 8/8:事件 schema 校验通过

### Day 7-8:文档 + 缓冲

更新:
- `README.md` 状态表
- `CLAUDE.md` 当前阶段 + 命令清单
- `ROADMAP.md` M1 标记 ✅
- `WORK_ESTIMATE.md` 用实际耗时校准 M2 估算

## 4. M1 之后的下一阶段选择

M1 之后,**M2/M3 都可以继续**:

- **走 M2**(identity 完整化:JWT、设备、会话)→ 适合"用户端"先准备好
- **走 M3**(wallet-svc OSS)→ 适合先验证最复杂的钱包逻辑

我推荐 **M2**(让"假用户"先能登录,M3 才有意义)。

## 5. 给 Claude Code 的提示

下次对话开始,粘贴这段给 Claude Code:

```
我在 Aurora 仓库的 M1 阶段。
请先读这些文件了解上下文:
- README.md
- CLAUDE.md
- docs/ROADMAP.md
- docs/M0_NEXT_STEPS.md
当前 M0 已完成,scripts/smoke_m0.sh 已通过。
我现在要开始 M1: Aurora Event Mesh。
请按 docs/M0_NEXT_STEPS.md 第 3 节的 Day 1-8 计划逐步实施。
每完成一个文件,我会跑测试并把结果发给你。
```

这样 Claude Code 不会上来就乱写,会按计划。

## 6. 常见陷阱(我替你预踩)

| 陷阱 | 怎么避 |
|---|---|
| Kafka container 启动慢,造成 identity-svc 启动失败 | docker-compose 用 `depends_on: condition: service_healthy` |
| Schema Registry 没启好就注册 schema | `scripts/register_schemas.sh` 内置 wait-for-ready |
| Avro schema 演进时破坏向后兼容 | 在 SR 用 `BACKWARD` 兼容模式 |
| Producer 阻塞影响 HTTP 响应延迟 | M1 用同步 producer + timeout 1s;M3 引入 outbox |
| 测试时事件还没到就检查 | smoke_m1.sh 加 `sleep 1` 或 poll |

## 7. 不要在 M1 顺便做的事

- ❌ 加 wallet-svc(留给 M3)
- ❌ 改 ai-agent-svc 接 LLM(留给 M5)
- ❌ 上 Cloudflare(留给 M10)
- ❌ 把 identity-svc 重构成 hexagonal architecture(M0 代码够用)
- ❌ 加 OpenTelemetry(留给 M13)

每个"顺便"都是 1-3 天工时,会让 M1 拖到 15+ 天。**严守 M1 范围**。
