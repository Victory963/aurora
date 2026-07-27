# 云端部署方案对比

## TL;DR

| 阶段 | 建议 |
|---|---|
| M0-M2 开发 | 本地 PC,不需要云 |
| M3-M5 | DigitalOcean Droplet 或 AWS EC2 单机($20-50/月) |
| M6-M10 | AWS EKS 或 GKE 多服务($300-600/月) |
| M11+ 上 production | AWS 多 Region 全栈($3K-8K/月) |

不要一开始就上 EKS。每个阶段选最简单的方案,**功能驱动 infra**,不是反过来。

---

## 阶段 1:单机部署(M0-M5)

### 选项 A:DigitalOcean Droplet(推荐给初学者)

- 4GB RAM、2 vCPU、80GB SSD:**$24/月**
- 8GB RAM、4 vCPU、160GB SSD:**$48/月**(M5 推荐)
- 选 region:`Singapore` 或 `Tokyo`(亚太开发者)

**步骤**:

```bash
# 1. 在 DO 网页创建 Ubuntu 22.04 Droplet,记 IP

# 2. SSH 上去
ssh root@<droplet-ip>

# 3. 装 Docker
curl -fsSL https://get.docker.com | sh

# 4. 装基础工具
apt install -y git make jq

# 5. clone + run
git clone <repo> /opt/aurora
cd /opt/aurora
make up

# 6. 设防火墙(只开必要端口)
ufw allow 22         # SSH
ufw allow 80         # 反代后用
ufw allow 443
ufw enable
# 不要暴露 8081/8082/5432 到公网!通过 nginx + Let's Encrypt 反代
```

**反代配置(让用户访问 https://aurora.yourdomain.com)**:

```bash
apt install -y nginx certbot python3-certbot-nginx
# 配置 nginx 反代 -> localhost:8081 等
# 申请 SSL: certbot --nginx -d aurora.yourdomain.com
```

### 选项 B:AWS EC2 单机

- `t3.medium`(4GB):**~$30/月**
- `t3.large`(8GB):**~$60/月**

成本比 DO 略高,但 EBS 卷快照、IAM、CloudWatch 都比 DO 完善。

### 选项 C:Hetzner / OVH

- 性价比之王:8GB RAM、4 vCPU 的 VPS 只要 **€5-8/月**
- 缺点:面板英文不友好,Region 主要在欧洲

---

## 阶段 2:容器编排(M6-M10)

到 M6 服务数 ~6,M10 已有 12 个服务 + 数据库。手动 docker-compose 不够了。

### 选项 A:AWS EKS(全托管 K8s)

- 控制面 **$73/月**(固定)
- 节点 EC2:3 个 `t3.large` ≈ **$180/月**
- ELB、NAT Gateway 等:**~$50/月**
- **总:$300-400/月**

**优势**:DAZN 本身就用 AWS(白皮书 1 的 ViewLift 收购也在 AWS),技术栈一致。

### 选项 B:GKE Autopilot(Google)

- 控制面免费
- 按 vCPU/RAM 计费,~$0.045/vCPU/h
- 同等规模:**~$250-350/月**

**优势**:Autopilot 模式管理负担小,适合小团队。

### 选项 C:DigitalOcean Kubernetes

- 控制面免费
- 3 节点 4GB:**~$60/月**
- 缺点:生态不如 AWS/GCP

### 选择建议

如果你**已经熟悉 AWS** → EKS。
如果你**完全没有云经验** → DigitalOcean Kubernetes(便宜 + 文档友好)。
**不要**为了 EKS 而学 EKS。M6-M10 还是开发阶段,基础设施越简单越好。

---

## 阶段 3:Production(M13+)

### 多 Region 架构

WP3 §7 设计是:Tokyo + Frankfurt + Virginia。
真实启动可以单 Region(欧洲先,因为 DAZN BET 牌照在欧洲)。

### 必须有的额外组件

- **CDN**:Cloudflare(配合 Edge Gateway M10)
- **数据库主备**:RDS Multi-AZ(Postgres)+ Neo4j Aura Enterprise
- **Kafka 集群**:MSK 或 Confluent Cloud
- **观测**:Datadog 或 Grafana Cloud
- **密钥**:AWS Secrets Manager + Vault
- **备份**:RDS automated + S3 跨区复制

### Production 预估月度成本

| 资源 | 月度 |
|---|---|
| EKS 控制面 + 节点(3 × t3.xlarge) | $500-700 |
| RDS Multi-AZ Postgres(db.t3.large) | $400-500 |
| MSK Kafka(3 broker,kafka.t3.small) | $300-400 |
| Neo4j Aura Enterprise(B 级) | $1,200 |
| ElastiCache Redis(cache.t3.medium) | $100 |
| CloudWatch + Datadog | $200-500 |
| Cloudflare(Pro plan) | $20 |
| NAT Gateway + ELB + S3 | $200-400 |
| **总(单 Region)** | **$3,000-3,800** |
| **多 Region(乘 ~2.5)** | **$7,000-10,000** |

加上 Sportradar / Genius 数据 / Pragmatic license 等,**production 真实月度成本 $15K-30K**。

---

## 我建议的路径(给初学者)

```
M0-M2: 本地 PC                    $0
M3-M5: DigitalOcean Droplet 8GB   $48/月
M6-M9: DO Kubernetes 3 节点        $60/月  
M10+: AWS EKS(因为接 Cloudflare、Pragmatic)$300+/月
M13 production prep: 单 Region    $3K/月
GA: 多 Region                     $7-10K/月
```

总开发期(M0-M12)云成本累计 **$1.5K-3K**,可控。

---

## CI/CD 选型

| 选项 | 月度成本 | 适合 |
|---|---|---|
| GitHub Actions(免费层 2000 min)| $0-50 | 个人/小团队 |
| GitLab CI(免费层 400 min)| $0-30 | 偏向 EU |
| CircleCI | $30-150 | 中型团队 |
| Buildkite | $$$ | 已有 production |

M0-M5 用 GitHub Actions 免费层够用。

## 监控

| 选项 | 月度 | 适合 |
|---|---|---|
| Grafana Cloud(免费层 10K series)| $0-200 | 自管 |
| Datadog | $300+ | 不想自管 |
| New Relic | $250+ | 同上 |
| 自建 Prometheus + Grafana | $50(只算 server)| 团队有 SRE |

WP3 推荐自建 Grafana stack(OpenTelemetry + Tempo + Loki + Mimir)。
M0-M5 不需要,**别提早优化**。
