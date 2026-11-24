# 开发技能目录

本目录回答“在哪个阶段用什么技能、用到什么程度”。技能是开发协助，不是 Go/Electron 的运行依赖。以下按 2026-11-23 的实装状态与公开目录核对；项目级已安装技能由根目录 [skills-lock.json](../skills-lock.json) 锁定上游提交与哈希，安装目录为 `.claude/skills/`。其他工作站或新会话应先核对自己实际暴露的名称。阶段编号对应[执行方案](EXECUTION.md)。

## 1. 会话内置技能（无需安装）

| 技能的准确名称 | 适用任务 | 期待的结果 / 不适用范围 |
|---|---|---|
| `ui-ux-pro-max` | P2–P4：主机表单、长任务列表、危险确认与键盘交互 | 给出可操作的焦点恢复、可见焦点、长列表窗口化验收；不要把 Web 页面示例照搬成运维桌面布局 |
| `anthropic-skills:frontend-design` | P2–P4：设计应用卡片、参数表单、任务详情的界面层次 | 输出组件和交互设计；不负责系统凭据、IPC 或安装规则。应同时满足离线字体/资源和高密度信息展示 |
| `run` | P1 起，实际开发/安装入口出现后启动桌面应用做冒烟 | 观察窗口、Go 就绪和报错；Vite 页面或开发窗口跑起来不是安装版通过 |
| `code-review`、`security-review` | P2–P6，已有源码或差异后审查协议、权限、迁移、打包及代码改动 | 给可复核的文件/行为和修复建议；不能用技能评语替代目标机试装、系统密钥库或签名验证 |
| `find-skills` | 现有能力不覆盖某项具体工作时，查找外部候选 | 从目录到发布方仓库核实，再检查安装脚本、权限、适用版本与 license；查到不等于启用 |

## 2. 项目已安装技能（由 `skills-lock.json` 锁定）

以下 9 项技能已按项目级（`--copy -a claude-code`）安装至本仓库 `.claude/skills/`，可在新工作站按需恢复或复核。

| 技能名称 | 上游来源与锁定提交 | 适用范围与使用边界 |
|---|---|---|
| `golang-concurrency` | [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) (`d68d80a`) | 审查 `backend/` 任务队列、`context` 取消、goroutine 生命周期与并发检测/卸载限流；**禁止**将通用工作池套用到必须全局串行的批量安装 |
| `golang-security` | [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) (`d68d80a`) | 审查 SSH/SFTP 路径穿越、命令拼接注入、凭据脱敏与主机密钥校验 |
| `golang-testing` | [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) (`d68d80a`) | 规范 Go 表格驱动测试、子测试、竞态检测与回环夹具测试 |
| `golang-database` | [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) (`d68d80a`) | 优化 `modernc.org/sqlite` 事务边界、旧库迁移一致性与分页查询 |
| `vercel-react-best-practices` | [vercel-labs/agent-skills](https://github.com/vercel-labs/agent-skills) (`5a37a63`) | 优化 `apps/desktop/src/renderer/` React 19 渲染性能、去重事件监听、长日志流渲染；**跳过** Next.js / RSC / SSR 专属规则，不改变 IPC 信任边界 |
| `vitest` | [antfu/skills](https://github.com/antfu/skills) (`4a6bb39`) | 扩展 `tests/workbench.test.tsx` 等前端组件与交互单测 |
| `playwright-best-practices` | [currents-dev/playwright-best-practices-skill](https://github.com/currents-dev/playwright-best-practices-skill) (`0b75b58`) | 规范定位器、断言等待与防抖动模式，提升 `tests/desktop-smoke.mjs` 等 Electron 冒烟测试稳定性 |
| `bash-defensive-patterns` | [wshobson/agents](https://github.com/wshobson/agents) (`34e50fe`) | 防御性 Bash 编写、严格错误处理、引号与安全清理，适用于 `resources/scripts/` 下远端 Linux `.sh` 脚本审查与扩展 |
| `native-feel-cross-platform-desktop` | [yetone/native-feel-skill](https://github.com/yetone/native-feel-skill) (`9bd88c6`) | 改善 Windows / macOS / Linux 三端桌面菜单、快捷键、窗口行为与原生质感 |

**恢复或更新方式（新环境按需执行，不作为项目构建依赖）：**

```bash
npx skills experimental_install -a claude-code
```

## 3. 未安装的可选候选

| 技能 / 来源 | 适用边界 | 核验备注 |
|---|---|---|
| `webapp-testing` · [目录](https://skills.sh/anthropics/skills/webapp-testing) · [Anthropic 仓库](https://github.com/anthropics/skills) | 可选，用 Python Playwright 驱动本地 Vite 页面和模拟 preload，验证界面交互；当前已有 Vitest + JS Playwright 覆盖，暂不安装 | **不能**证明 Electron IPC、真实 Go、操作系统凭据库或安装包可用 |

> 说明：此前在索引中出现的 `partme-ai/full-stack-skills@electron` 经实测上游仓库已不包含该技能条目，已移除。Electron 主进程安全、预加载白名单与打包配置继续以第 4 节官方文档为准。

## 4. Go、Electron 和迁移专项：以一手资料为准

对掌握 SSH 凭据、签名身份和真实数据库的核心链路，优先以官方文档、受限本地工具和脱敏夹具为准。以下为**权威文档，不是可安装技能**。

| 领域 | 官方/上游入口 | 用它核对什么 |
|---|---|---|
| Go 与 SSH | [Go 数据访问](https://go.dev/doc/database/) · [Go testing](https://pkg.go.dev/testing) · [x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) | 事务、子测试、连接及主机密钥校验；通过授权测试机验证，不把真实主机交给第三方技能 |
| Electron 与分发 | [Electron 安全](https://www.electronjs.org/docs/latest/tutorial/security) · [electron-builder 格式](https://www.electron.build/v26/docs/targets) · [代码签名](https://www.electronjs.org/docs/latest/tutorial/code-signing) | preload 白名单、隔离、辅助程序/安装包签名、各平台构建约束 |
| 真桌面回归与数据导入 | [Playwright Electron API](https://playwright.dev/docs/api/class-electron) · [SQLite Online Backup](https://www.sqlite.org/backup.html) | 指向已打包可执行文件的真实桌面冒烟（Electron API 仍为实验性），以及旧库一致快照的正确做法 |

**选择原则：** 新版没有 WPF，排除 WPF 美化技能；没有远程 Web 服务，不引入面向公网部署、SSR 或 Kubernetes 的技能。任何技能都不应默认得到终端输出、生产 IP、带密数据库、签名凭据或自动发布权限。
