# libs/events — Aurora Event Mesh 契约

跨服务**事件**的单一事实源。RPC 契约在 `libs/proto/`;**事件**契约在这里。

配套决策见 [ADR-0002](../../docs/adr/0002-event-mesh.md)。

## 目录

```
avro/
  user.lifecycle.created.v1.avsc   ← M1 现产(identity-svc)
  user.lifecycle.deleted.v1.avsc   ← 前向契约,M2 产出
  match.event.goal.v1.avsc         ← 前向契约,M5 消费
  bet.placed.v1.avsc               ← 前向契约,M6 产 / M3 消费
  audit.action.v1.avsc             ← 前向契约,M12 统一审计
```

> "前向契约" = schema 已定稿并可注册,但**还没有服务真正发布**。这样下游可以提前按契约开发。

## 命名约定

| 概念 | 规则 | 例 |
|---|---|---|
| Kafka topic | `aurora.<domain>.<entity>.v<bus-n>` | `aurora.identity.user-lifecycle.v1` |
| Schema subject | **RecordNameStrategy**:`<namespace>.<RecordName>` | `aurora.identity.events.v1.UserLifecycleCreated` |
| 事件类型 `event_type` | `<entity>.<verb>.v<schema-n>` | `user.lifecycle.created.v1` |
| Avro namespace | `aurora.<domain>.events.v<schema-n>` | `aurora.identity.events.v1` |

一个 topic 承载同实体的多种生命周期事件,用 envelope 的 `event_type` 区分;
每种 record 用 RecordNameStrategy 拿到独立 subject 与独立兼容性演进(见 ADR-0002 §3)。

## 事件信封(envelope)

所有事件是**扁平 record**(方便 `jq` 校验),前 3 个字段是信封,其余是 payload:

```
event_id            string   事件自身 UUID,幂等去重用
event_type          string   见上
occurred_at_unix_ms long     事件产生时间
<payload ...>
```

## M1 线格式

**JSON**,字段与对应 `.avsc` 一一对应。Schema Registry 用于**治理 + 兼容性门禁**,
不做运行时编解码。二进制 Avro 线格式延后(见 ADR-0002 §2)。

## 注册到 Schema Registry

```bash
make register-schemas        # 或 bash scripts/register_schemas.sh
```

脚本会:等待 SR 就绪 → 对每个 subject 设 `BACKWARD` 兼容 → 注册 `.avsc`。

## 演进规则(BACKWARD 兼容)

- ✅ 加**带 `default` 的可选字段**
- ❌ 删字段 / 改类型 / 改字段名
- 破坏性变更 → 新 `event_type`(`...created.v2`)+ 新 `.avsc`,迁移期内并存
