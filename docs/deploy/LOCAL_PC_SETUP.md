# 本地 PC 环境搭建(给编程初学者)

> 假设:你从未跑过任何 Go / Python / Docker 项目。这份指南面向第一次。

---

## macOS

### 1. 安装 Homebrew(包管理器)

打开终端,粘贴运行:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

完成后跑 `brew --version` 看版本。

### 2. 装 Docker Desktop

- 下载:https://www.docker.com/products/docker-desktop/
- 选 Apple Silicon(M1/M2/M3)或 Intel 版
- 装好后启动 Docker Desktop,菜单栏看到 🐳 鲸鱼图标 = OK

### 3. 装 git、make、curl、jq

```bash
brew install git make curl jq
```

(macOS 自带 git/make/curl,brew 会装更新版本;jq 是 smoke 脚本需要的 JSON 工具)

### 4. (M1+ 才需要)装 Go 和 Python

M0 跑 docker 即可,**不需要本地装 Go / Python**。但本地开发会需要:

```bash
brew install go@1.22
brew install python@3.11
```

### 5. 装编辑器:VSCode

- 下载:https://code.visualstudio.com/
- 必装插件(在 VSCode 内左侧 Extensions 搜索):
  - `Go`(golang.go)
  - `Python`(ms-python.python)
  - `Pylance`(ms-python.vscode-pylance)
  - `Docker`(ms-azuretools.vscode-docker)
  - `YAML`(redhat.vscode-yaml)
  - `Even Better TOML`(tamasfe.even-better-toml)
  - `Buf`(bufbuild.vscode-buf)— M1+ 用 protobuf

### 6. 运行 Aurora

```bash
cd ~/Downloads  # 或任何你想放代码的目录
# (假设 Aurora 代码在 ~/Downloads/aurora)
cd aurora
make up
make wait-healthy
make smoke
```

如果看到 `M0 SMOKE TEST PASSED`,环境 OK。

---

## Windows 11

### 1. 装 WSL2(Linux 子系统)

打开 PowerShell **以管理员身份**,跑:

```powershell
wsl --install
```

重启电脑。重启后会自动装 Ubuntu。设置一个 Linux 用户名 + 密码。

### 2. 装 Docker Desktop for Windows

- 下载:https://www.docker.com/products/docker-desktop/
- 装好后 Settings → General → 勾选 "Use the WSL 2 based engine"
- Resources → WSL Integration → 启用对你的 Ubuntu 发行版的集成

### 3. 在 WSL2 Ubuntu 内装工具

打开 WSL2 终端(开始菜单搜 "Ubuntu"):

```bash
sudo apt update
sudo apt install -y git make curl jq
```

### 4. 装编辑器:VSCode + WSL 扩展

- 下载 VSCode:https://code.visualstudio.com/
- 装扩展 "WSL"(ms-vscode-remote.remote-wsl)
- 装上面 macOS 一节列出的其它插件
- 在 WSL2 终端跑 `code .` 会自动连上 VSCode

### 5. 运行 Aurora

```bash
# 在 WSL2 终端
cd ~
git clone <repo-url> aurora
cd aurora
make up
make wait-healthy
make smoke
```

### Windows 常见问题

| 问题 | 解决 |
|---|---|
| `docker: command not found` | Docker Desktop 没开 WSL2 集成 |
| 端口 8081 / 8082 被占用 | `netstat -ano | findstr 8081` 看进程,杀掉或改端口 |
| Docker 占内存 100% | Docker Desktop → Settings → Resources → Memory 改 4-6GB |

---

## Linux(Ubuntu 22.04+)

### 1. 装 Docker Engine

```bash
# Docker 官方一键脚本
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
newgrp docker  # 不用 sudo 跑 docker

# 装 docker compose v2
sudo apt install -y docker-compose-plugin
```

### 2. 装工具

```bash
sudo apt update
sudo apt install -y git make curl jq
```

### 3. (M1+)装 Go 和 Python

```bash
# Go
wget https://go.dev/dl/go1.22.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.6.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Python(系统自带 3.10+,M5+ 需要 3.11)
sudo apt install -y python3.11 python3.11-venv python3-pip
```

### 4. 运行

```bash
cd ~
git clone <repo-url> aurora
cd aurora
make up
make wait-healthy
make smoke
```

---

## 验证 M0 跑通

成功的 `make smoke` 应该输出:

```
━━━ 1/6 identity-svc health ━━━
✓ identity-svc reports status=ok

━━━ 2/6 ai-agent-svc health + identity dependency ━━━
✓ ai-agent-svc reports status=ok, identity-svc reachable

━━━ 3/6 Create a user via identity-svc ━━━
✓ Created user: f47ac10b-58cc-4372-a567-0e02b2c3d479

━━━ 4/6 Fetch user back by ID ━━━
✓ GetUser returned the right user

━━━ 5/6 Ask ai-agent-svc for recommendations (SOLO mode) ━━━
✓ Got 2 recommendation(s), correlation_id=<uuid>, latency=12ms

━━━ 6/6 Verify recommendation schema ━━━
✓ Recommendation schema valid: market=j1.urawa-vs-fctokyo.next-goal-15m confidence=0.74

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  M0 SMOKE TEST PASSED
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

看不到这个就有问题。复制错误信息到 Issue 里(或下次对话给我),我帮你 debug。

---

## 常用 debug 操作

| 操作 | 命令 |
|---|---|
| 看所有容器状态 | `make ps`(或 `docker compose ps`) |
| 看某个服务的日志 | `docker compose logs identity-svc` |
| 进 Postgres 看数据 | `make shell-pg`(然后 `\dt` 看表,`SELECT * FROM users;`) |
| 重建某个服务镜像 | `docker compose build --no-cache identity-svc` |
| 彻底重置 | `make clean && make up` |
| 看 Docker 占用资源 | `docker stats` |

---

## 资源占用预期

| 资源 | M0 | M3(M3 完成后)| M10(完整 stack)|
|---|---|---|---|
| 内存 | ~600MB | ~1.5GB | ~6-8GB |
| 磁盘 | ~500MB | ~2GB | ~10-15GB |
| CPU 空载 | 1-2% | 3-5% | 10-15% |

如果你的 PC 内存 ≤ 8GB,M5 之后建议租云开发机(见 `CLOUD.md`)。
