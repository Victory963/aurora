# Aurora

**DAZN 下一代社交 × AI 体育博彩平台 — Monorepo(白皮书 3 落地实现)**

> 当前状态:**M0–M8 全部完成并真实验证**(9 个 smoke 一键复现),含浏览器可视化 demo 控制台。
> 标志性闭环已落地:**AI 在房间提议 Pool → 群体下注 → parimutuel 结算 → 行为进图谱**。

## 30 秒看懂

| | |
|---|---|
| 📊 详细进度 | [docs/PROGRESS.md](docs/PROGRESS.md)(M0-M8 ✅,每个 M 的验证证据) |
| 🏗️ 架构分析 | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)(拓扑图/事件流/钱安全不变量/技术债) |
| 🗺️ 路线图 | [docs/ROADMAP.md](docs/ROADMAP.md)(13 阶段,142-180 工日) |
| 📐 关键决策 | [docs/adr/](docs/adr/)(8 篇 ADR) |
| 🤖 AI 开发上下文 | [CLAUDE.md](CLAUDE.md)(给 Claude Code 的仓库约定,继续开发从这里开始) |

## 快速开始(新机器)

前置:Docker Desktop(WSL 用户开启 WSL integration)、`jq`、GNU make。

```bash
git clone https://github.com/Victory963/aurora.git && cd aurora
make up                # 构建并启动全栈(15 容器:6 服务 + Kafka/NATS/SR/PG/Scylla/Neo4j/Redis/demo)
make wait-healthy      # 等全部服务 healthy(首次含镜像构建,数分钟)
make register-schemas  # 注册 Avro schema
make smoke-m8          # 最大覆盖的端到端验收(或逐个跑 smoke-m0..m8)
```

然后打开 **http://localhost:8090** —— 演示控制台,每个按钮都调用真实后端:
一键注册两名用户(Kafka 事件自动开钱包)→ 建房聊天(ScyllaDB + NATS)→
🤖 AI 提议 Pool(LangGraph 工具审计链)→ 双人下注(幂等扣款)→ 结算(彩池分钱)→ 图谱查询(Neo4j 共现/KOL)。

> 无 `ANTHROPIC_API_KEY` 时 AI 走确定性降级模板(同一批工具数据),全流程照常;
> 配了 key 则是真 Claude 推理:`ANTHROPIC_API_KEY=sk-ant-… make up`。

## 服务一览

| 服务 | 技术 | 端口 | 一句话 |
|---|---|---|---|
| identity-svc | Go + Postgres | 8081 | 注册/登录/JWT + 轮换 refresh/设备/自删(发生命周期事件) |
| ai-agent-svc | Python + LangGraph + Claude | 8082 | 数字只来自工具,LLM 只选市场写推理;质量门重算 EV;无 key 降级 |
| wallet-svc | Go + Postgres | 8083 | 虚拟币 AUC 双记账本;幂等 Credit/Debit;transactional outbox |
| room-svc | Go + PG + ScyllaDB + NATS | 8084 | 房间/聊天/实时信号;M7 编排 `ProposeAiPool` |
| bet-svc | Go + Postgres | 8086 | 房间内 Closed Pool,parimutuel 结算(big.Int,崩溃可重放) |
| graph-svc | Go + Neo4j | 8087 | 事件投影图谱:共现/Sybil/KOL;PII 边界 + GDPR 墓碑 |
| demo-ui | nginx | 8090 | dev 演示控制台(同源反代,零 CORS) |

基础设施:Kafka(29092)/ Schema Registry(8085)/ NATS(4222,监控 8222)/ Postgres(5432)/
ScyllaDB(9042)/ Neo4j(7474/7687)/ Redis(6379)。

## 常用命令

```bash
make help          # 全部命令
make test          # 六个服务的单元测试(不依赖外部服务)
make smoke-m<n>    # 第 n 个里程碑的端到端验收(n=0..8)
make logs          # 跟日志
make down          # 停止;make clean 连数据卷一起删
```

## 仓库结构

```
services/<name>-svc/     # 一个服务一个目录,自包含(cmd/ internal/ Dockerfile README.md go.mod)
libs/proto/aurora/       # 跨服务 proto 契约(aurora.<domain>.v1)
libs/events/avro/        # 事件 schema(Avro,线上 JSON 兼容其字段契约)
demo/                    # 演示控制台(index.html + nginx.conf)
infra/sql/init/          # Postgres 多库初始化
scripts/                 # smoke_m*.sh 验收 + 运维脚本
docs/                    # ROADMAP / ARCHITECTURE / PROGRESS / adr/ / m<n>/PLAN.md
CLAUDE.md                # AI 结对开发的仓库约定(命令/红线/已知限制/工作流)
```

## 工程红线(节选,全文见 CLAUDE.md)

- 任何"完成"必须 `make smoke-m<n>` 一键真实复现 —— **禁止自报通过**。
- 跨服务只走 HTTP/RPC,**禁止读对方数据库**;钱相关调用**幂等键贯穿**。
- 密钥只走环境变量(dev 默认值仅本地);go.sum 提交保证可复现构建。
- 每个里程碑严格控范围,未来功能显式登记到对应 M(见各 ADR「后果/债务」)。
