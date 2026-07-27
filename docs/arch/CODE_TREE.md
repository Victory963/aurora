# Aurora 代码树层级图

```
aurora/                                       MONOREPO 根
│
├── README.md                                 ◄── 入口文档
├── CLAUDE.md                                 ◄── Claude Code 协作上下文
├── Makefile                                  ◄── 开发者命令入口(make up/smoke/test)
├── docker-compose.yml                        ◄── M0 一键启动栈
│
├── services/                                 ┌──────────── 域服务层 ────────────┐
│   │                                        │  M0:2 个; M13:12 个              │
│   ├── identity-svc/        [Go]            │  每个服务自包含、自带 README/Dockerfile│
│   │   ├── cmd/main.go                      │  跨服务通信走 HTTP/RPC, 不直连 DB     │
│   │   ├── internal/                        └──────────────────────────────────┘
│   │   │   ├── config/config.go             ◄── 环境变量
│   │   │   ├── store/store.go               ◄── Postgres 数据访问
│   │   │   └── server/                      ◄── HTTP handlers
│   │   │       ├── server.go
│   │   │       └── server_test.go
│   │   ├── gen/                             ◄── (M1+ buf 生成代码放这)
│   │   ├── go.mod
│   │   ├── Dockerfile
│   │   └── README.md
│   │
│   ├── ai-agent-svc/        [Python 3.11]
│   │   ├── aurora_ai/
│   │   │   ├── __init__.py
│   │   │   ├── server.py                    ◄── FastAPI app
│   │   │   ├── models.py                    ◄── Pydantic 模型(M1+ 由 proto 生成)
│   │   │   └── agent.py                     ◄── M5 替换为 LangGraph
│   │   ├── tests/test_server.py
│   │   ├── pyproject.toml
│   │   ├── Dockerfile
│   │   └── README.md
│   │
│   └── [M3+]                                ◄── 后续阶段新增
│       ├── wallet-svc/                      [M3]
│       ├── room-svc/                        [M4]
│       ├── bet-svc/                         [M6]
│       ├── content-svc/                     [M11]
│       ├── compliance-svc/                  [M12]
│       └── ...                              共 12 个服务
│
├── libs/                                    ┌─── 共享代码 ───┐
│   ├── proto/aurora/                        │ 跨服务接口契约 │
│   │   ├── identity/v1/identity.proto       │ 不要把业务逻辑 │
│   │   └── ai/v1/ai.proto                   │ 放进 libs/    │
│   └── events/                              └──────────────┘
│       └── avro/                            ◄── (M1+ 第一批 schema 在此)
│
├── infra/                                   ┌─── 基础设施 ───┐
│   ├── docker/                              │ docker 构建参数 │
│   ├── sql/                                 │ K8s manifest    │
│   │   └── init/                            │ Terraform (M10+)│
│   │       └── 01-create-databases.sql     └────────────────┘
│   └── [M10+]
│       ├── k8s/
│       └── terraform/
│
├── scripts/                                 ┌── 自动化脚本 ───┐
│   ├── smoke_m0.sh                          │ 每个 M 一个 smoke│
│   ├── wait_healthy.sh                      │ 验证脚本是 M 的  │
│   └── [M1+]                                │ 真正"完成"判据  │
│       ├── smoke_m1.sh                      └────────────────┘
│       ├── kafka_consume.sh
│       └── register_schemas.sh
│
└── docs/                                    ┌── 工程文档 ───┐
    ├── ROADMAP.md                           │ ADR 必须不可修改│
    ├── WORK_ESTIMATE.md                     │ 只能被新 ADR    │
    ├── M0_NEXT_STEPS.md                     │ 取代            │
    ├── adr/                                 └────────────────┘
    │   ├── 0001-ai-agent-langgraph.md       (Proposed, M5 实施)
    │   └── [M1+]
    │       ├── 0002-event-mesh.md
    │       ├── 0003-wallet-event-sourcing.md
    │       └── ...
    ├── arch/                                ◄── 架构图(本文件等)
    │   ├── CODE_TREE.md                     (此文件)
    │   ├── M0_DATAFLOW.md
    │   └── GANTT_M0_M13.md
    └── deploy/
        ├── LOCAL_PC_SETUP.md                ◄── 给初学者
        └── CLOUD.md                         ◄── 云端选型


┌─────────────────────────────────────────────────────────────────────────┐
│                     依赖与边界规则                                       │
└─────────────────────────────────────────────────────────────────────────┘

✓ services/X 可以引用 libs/proto/aurora/X/...
✓ services/X 可以通过 HTTP 调用 services/Y(via env 配置的 URL)
✗ services/X 不可直接 import services/Y 的代码
✗ services/X 不可直连 services/Y 的数据库
✗ libs/ 不可依赖 services/
✗ infra/ 不可依赖 services/(只配置,不嵌业务)
✗ docs/ 不影响构建


┌─────────────────────────────────────────────────────────────────────────┐
│                     每个 M 增加的代码量(参考)                          │
└─────────────────────────────────────────────────────────────────────────┘

M0 (已完成):  ~1,500 lines code + ~250 tests + ~1,400 docs
M1:           ~+800 lines (Kafka producer + 5 schemas + consumer)
M2:           ~+1,400 lines (devices, sessions, JWT)
M3:           ~+3,500 lines (wallet-svc 是 M0-M5 中最大模块)
M4:           ~+2,500 lines (room + chat)
M5:           ~+3,000 lines (LangGraph state machine + tools)
M6:           ~+2,800 lines (Pool math + settlement)
M7:           ~+1,000 lines (集成,主要是配置 + 测试)
M8:           ~+2,000 lines (Cypher + projector)
M9:           ~+2,500 lines (Mock Pragmatic + Adapter)
M10:          ~+1,500 lines (Worker TS)
M11:          ~+3,000 lines (Persona pipeline)
M12:          ~+2,000 lines (OPA policies + integration)
M13:          ~+1,500 lines (OTel + chaos tests)
──────────────────────────
M0-M13 累计:  ~30,000 lines code + ~6,000 tests + ~5,000 docs

(注:与最初白皮书 3 想象的 50K-200K 行差距大,因为我们做的是 MVP 而非 production-ready
 的完整实现。真正 production 化每个模块还要 +50-100% 代码量。)
```
