# Aurora Live Demo 上线 + 二维码使用指南

**目标**:让 LINE / 微信 / 任何手机扫码即可在线体验 Aurora demo
**预计耗时**:5 分钟(已有账号)→ 10 分钟(全新流程)
**总成本**:¥0(全程免费托管 + 免费二维码生成)

---

## 0. 前置说明

二维码 **不能直接装下 HTML 文件**(QR 码理论上限 ~3KB,Aurora demo 是 57KB),所以必须:
1. 先把 HTML 文件部署到一个公开 URL
2. 再用该 URL 生成二维码

下面提供 **4 种零成本部署方案**,任选一种 5 分钟即可拿到公开 URL。

---

## 方案 A:Netlify Drop(推荐 — 最简单,0 命令)

**最适合**:没有 GitHub / 不想装命令行 / 现场快速演示

**步骤**:
1. 浏览器打开 https://app.netlify.com/drop
2. 注册 / 登录(用 Google 或 GitHub 账号 30 秒搞定)
3. 把 `aurora_live_demo.html` **拖进网页中央的方框**
4. 等待 ~10 秒上传
5. 拿到形如 `https://xxx-xxx-xxx.netlify.app` 的公开 URL

**完整 URL 范例**:`https://wonderful-tartufo-abc123.netlify.app/aurora_live_demo.html`

**优点**:
- 完全无需技术背景
- HTTPS 自动配置(LINE/微信扫码需要 HTTPS)
- 全球 CDN,日本访问极快
- 永久免费,无访问量限制

**可选定制域名**:Netlify 网页里 Site Settings → 改 site name(如改为 `aurora-demo`),URL 就变成 `https://aurora-demo.netlify.app`

---

## 方案 B:Cloudflare Pages(也很简单)

**最适合**:想要更专业的域名 / 已有 Cloudflare 账号

**步骤**:
1. 注册 Cloudflare 账号(免费)
2. 进入 Pages → Upload Assets
3. 拖入 `aurora_live_demo.html`
4. 选择项目名,如 `aurora-demo`
5. 等待 ~30 秒部署完成
6. URL:`https://aurora-demo.pages.dev/aurora_live_demo.html`

**优点**:
- 速度比 Netlify 更快(尤其在亚太)
- 无任何流量上限

---

## 方案 C:GitHub Pages(适合开发者)

**最适合**:已有 GitHub 账号,想要永久托管 / 版本管理

**步骤**:
1. 在 GitHub 创建新 public repo,例如 `aurora-demo`
2. 上传 `aurora_live_demo.html`(也可以改名为 `index.html` 让 URL 更短)
3. 进入 Settings → Pages → Source 选 `main` 分支 `/(root)`,Save
4. 等 1-2 分钟,URL:`https://你的用户名.github.io/aurora-demo/aurora_live_demo.html`

**优点**:
- 永久免费,与 GitHub 账号绑定
- 可以版本控制 demo 的迭代

**注意**:GitHub Pages 部署有 5-10 分钟的 CDN 缓存,刚上传访问可能 404,稍等再试。

---

## 方案 D:Vercel(适合 React/Next.js 团队)

**步骤**:
1. https://vercel.com → Login with GitHub
2. New Project → Import → 选含 `aurora_live_demo.html` 的 repo
3. 直接 Deploy,无需任何配置
4. URL:`https://你的项目名.vercel.app/aurora_live_demo.html`

---

## 三种方案对比

| 维度 | Netlify Drop | Cloudflare Pages | GitHub Pages | Vercel |
|---|---|---|---|---|
| 难度 | ★(最简单)| ★ | ★★ | ★★ |
| 上线时间 | 10 秒 | 30 秒 | 1-2 分钟 | 1 分钟 |
| HTTPS | ✓ 自动 | ✓ 自动 | ✓ 自动 | ✓ 自动 |
| 亚太速度 | 中 | **快** | 中 | 快 |
| 自定义域名 | ✓ 免费 | ✓ 免费 | ✓ 免费(需 DNS) | ✓ 免费 |
| 访问统计 | ✓ 内置 | ✓ 内置 | ✗ 需自己加 | ✓ 内置 |
| 适合 | 一次性 demo | 长期托管 | 团队协作 | 全栈项目 |

**推荐顺序**:Netlify Drop(临时演示)> Cloudflare Pages(长期可靠)> GitHub Pages > Vercel

---

## 1. 生成二维码(拿到 URL 之后)

部署完成拿到 URL,跑 Python 脚本生成二维码:

### 方式 1:本机 Python(需要 Python 3.8+)

```bash
pip install qrcode pillow
python3 generate_qr.py "https://你的实际URL.netlify.app/aurora_live_demo.html"
```

输出:`aurora_demo_qr.png`(300 DPI 高清,可印刷)

### 方式 2:不想写代码 → 在线生成器

如果不方便跑 Python,推荐这些**免费在线二维码工具**:

| 工具 | URL | 优势 |
|---|---|---|
| QR Code Monkey | https://www.qrcode-monkey.com/ | 支持 logo + 自定义颜色 + 高清下载 |
| QR.io | https://qr.io/ | 简洁,支持追踪扫描次数 |
| Adobe Express | https://www.adobe.com/express/feature/qr-code-generator | 设计感强,印刷质量 |

操作:粘贴 URL → 下载 PNG

### 方式 3:命令行 + Python 脚本(完整定制)

我们提供的 `generate_qr.py` 脚本特性:
- ✅ Aurora 品牌色(深蓝灰 + 紫青渐变 logo)
- ✅ 30% 容错级别(logo 占中心 1/5 仍可扫描)
- ✅ 300 DPI 高清,可直接印名片 / 海报
- ✅ 圆角模块,视觉精致
- ✅ 自带标题 + 副标题 + URL 显示

修改脚本里这个变量定制风格:

```python
DEMO_URL = "https://你的URL"  # 改这里
```

---

## 2. 扫描验证

生成 PNG 后,**必须先自测**再发给别人:

### 验证清单

- [ ] 用 iPhone 自带相机扫码 → 自动弹出 URL → 点击 → demo 打开
- [ ] 用 Android 自带相机扫码 → 同上
- [ ] **LINE 扫码**:打开 LINE App → 右上角 ➕ → "QR コードリーダー" → 扫码 → 自动打开浏览器
- [ ] **微信扫码**:打开微信 → 右上角 ➕ → "扫一扫" → 扫码 → 微信会强制提示"在外部浏览器打开"(微信 inner browser 对动态 JS 兼容性差,务必让用户点"用浏览器打开")
- [ ] 横屏 / 竖屏均能扫
- [ ] 距离 30cm-1m 都能扫
- [ ] 印刷品大小(3cm × 3cm)能扫

### 常见问题

**Q: 微信扫了打不开 demo?**
A: 微信内置浏览器对 Canvas 和复杂 JS 支持差。提示用户点右上角"..." → "在浏览器中打开"。这是 demo 性质决定的,无法绕开。

**Q: LINE 打开后样式错乱?**
A: 同上,LINE 内置浏览器在 iOS 上用的是 WKWebView,基本正常。Android 用 ChromeCustomTabs 也正常。如果碰到,引导用户长按链接 → "在 Safari/Chrome 打开"。

**Q: 二维码扫不出来?**
A: 检查 3 点:
1. PNG 是否被压缩到很小尺寸(<500px 不行)
2. 印刷时四周是否留 "quiet zone"(白边)
3. 中间 logo 是否过大(<25% 的 QR 面积才安全)

**Q: 想要追踪扫描次数?**
A: 用短链工具中转,例如 Bitly / TinyURL:
- 原 URL:`https://aurora-demo.netlify.app/aurora_live_demo.html`
- 短链 URL:`https://bit.ly/aurora-live`(带统计)
- 用短链 URL 生成二维码

---

## 3. 印刷 / 投屏使用建议

### 用于投委会演示(大屏投屏)
- 二维码尺寸:**15cm × 15cm 以上**
- 放在 slide 一角,搭配 "扫码现场体验" 文字
- 现场需可靠 Wi-Fi(demo 已是完全 client-side,但首次加载需要 ~60KB 流量)

### 印刷在名片 / 宣传单
- 二维码最小尺寸:**2.5cm × 2.5cm**(再小 LINE/微信扫不到)
- 印刷分辨率 ≥ 300 DPI
- 周围留 5mm "quiet zone" 不要放任何文字

### 嵌入邮件 / Slack / Notion
- 直接发 URL 链接,二维码反而多此一举
- 二维码只在**纸质场景**和**会场大屏**有意义

---

## 4. 高级:让 demo URL 更专业

### 4.1 自定义子域名(免费)

Netlify 支持改 site name:
- `https://abc-xyz-123.netlify.app` → `https://aurora-demo.netlify.app`

操作:Netlify Dashboard → Site → Site Settings → Change site name

### 4.2 自定义域名(需有域名)

如果 DAZN 有 `dazn.com` 域名,可以做:
- `https://aurora-demo.dazn.com`(指向 Netlify / Cloudflare Pages)

DNS 配置:加一条 CNAME 记录,5 分钟生效。

### 4.3 加密码保护(避免外泄)

如果 demo 不想给所有人看,Netlify Pro 计划($19/月)支持密码保护。或者用 Cloudflare Access(免费)做 OAuth 登录限制。

---

## 5. 部署后维护

### 更新 demo 内容
- Netlify Drop:重新拖文件覆盖
- GitHub Pages / Vercel / Cloudflare Pages:git push,自动重新部署

### 监控访问量
- Netlify / Vercel / Cloudflare 都有内置 analytics
- 推荐另外加 Plausible.io($9/月)或 Cloudflare Web Analytics(免费)

### 关停 demo
- 直接在托管面板里删项目,1 分钟生效
- 已分发出去的二维码扫码后会显示 "Site Not Found"

---

## 附录:本目录文件清单

```
demo/
├── aurora_live_demo.html           # 主 demo 文件(57KB,直接部署)
├── generate_qr.py                  # Python 二维码生成器
├── aurora_qr_netlify_template.png  # 示例:Netlify 模板二维码
├── aurora_qr_github_template.png   # 示例:GitHub Pages 模板二维码
├── aurora_qr_cloudflare_template.png # 示例:Cloudflare Pages 模板二维码
└── README.md                        # 本文档
```

---

## 一句话总结

**最快路径**:
1. 浏览器开 https://app.netlify.com/drop
2. 拖 `aurora_live_demo.html` 上去
3. 把拿到的 URL 粘进 `generate_qr.py`,跑一下
4. 得到 `aurora_demo_qr.png`,可印刷可投屏

**全程 5 分钟,¥0 成本**。
