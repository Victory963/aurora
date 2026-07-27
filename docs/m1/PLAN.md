# M1 实施计划 — Aurora Event Mesh

> 路线图约定:每个 M 编码前先写 PLAN。本文件是 M1 的落地清单,配套 ADR-0002。

## 目标(验收口径)

`make up && make smoke-m1` 一键跑通,在 M0 6 步之上**额外**证明:

1. Schema Registry 已注册 Avro 契约(subject `aurora.identity.events.v1.UserLifecycleCreated`,RecordNameStrategy)。
2. CreateUser 后,Kafka topic `aurora.identity.user-lifecycle.v1` 出现一条事件。
3. 该事件 JSON **符合 Avro 字段契约**,且 `event_type=user.lifecycle.created.v1`、`user_id`/`email` 与创建的用户一致。

## 交付物

| # | 文件 | 说明 |
|---|---|---|
| 1 | `docs/adr/0002-event-mesh.md` | 设计决策(✅) |
| 2 | `libs/events/avro/*.avsc` | 第一批 5 个 schema(1 个现产,4 个前向契约) |
| 3 | `libs/events/README.md` | schema 治理 / 命名 / 演进规则 |
| 4 | `services/identity-svc/internal/events/events.go` | `Publisher` 接口 + 事件类型 + `Nop`(无外部依赖) |
| 5 | `services/identity-svc/internal/events/kafka/publisher.go` | `KafkaPublisher`(segmentio/kafka-go) |
| 6 | `services/identity-svc/internal/events/events_test.go` | Nop + envelope 单测(无 DB / 无 Kafka) |
| 7 | identity-svc `config.go` / `main.go` / `server.go` | 接线:配置 broker、CreateUser 发事件 |
| 8 | `docker-compose.yml` | +zookeeper +kafka +schema-registry +nats |
| 9 | `scripts/register_schemas.sh` | 把 avsc 注册到 SR(含 wait-for-ready + BACKWARD) |
| 10 | `scripts/smoke_m1.sh` | M1 验收脚本 |
| 11 | `Makefile` | `smoke-m1`、`register-schemas` target |
| 12 | README / CLAUDE / ROADMAP | 文档同步 |

## 关键约定(全仓一致,改动须同步全部)

- Topic:`aurora.identity.user-lifecycle.v1`
- Subject(RecordNameStrategy):`aurora.identity.events.v1.UserLifecycleCreated`
- 事件类型:`user.lifecycle.created.v1`
- Avro:namespace `aurora.identity.events.v1`,record `UserLifecycleCreated`
- 环境变量:`AURORA_IDENTITY_KAFKA_BROKERS`(csv,空=禁用)、`AURORA_IDENTITY_KAFKA_TOPIC`
- compose 端口:kafka host `29092`、schema-registry host `8085`、nats `4222`/`8222`

## 不在 M1 范围(留给后续)

- ❌ NATS producer(M4 room-svc)
- ❌ transactional outbox / 严格投递(M3)
- ❌ 二进制 Avro 线格式 + SR 运行时编解码(候选 M6)
- ❌ 多副本 / 生产级 Kafka 拓扑(部署阶段)
- ❌ ai-agent-svc / 其它服务接 LLM 或订阅事件(各自里程碑)

## 本机限制提示

开发机若无 `go` / `docker`,**无法在此本地验证**;代码经静态审查,
真验收须在装有 Docker 的机器上 `make up && make wait-healthy && make smoke-m1`。
首次构建前请 `make tidy` 生成并提交 `go.sum`(新增 segmentio/kafka-go 依赖)。
