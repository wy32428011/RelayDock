# 开发技能目录

本目录回答“在哪个阶段用什么技能、用到什么程度”。技能是开发协助，不是 Go/Electron 的运行依赖。以下按本次会话的可用清单与 2026-11-18 的公开目录核对；**未安装或更新任何技能**。其他工作站或新会话应先核对自己实际暴露的名称。阶段编号对应[执行方案](EXECUTION.md)。

## 1. 本次环境已提供

| 技能的准确名称 | 适用任务 | 期待的结果 / 不适用范围 |
|---|---|---|
| `ui-ux-pro-max` | P2–P4：主机表单、长任务列表、危险确认与键盘交互 | 给出可操作的焦点恢复、可见焦点、长列表窗口化验收；本次已检索确认框焦点、React 长列表。不要把 Web 页面示例照搬成运维桌面布局 |
| `anthropic-skills:frontend-design` | P2–P4：设计应用卡片、参数表单、任务详情的界面层次 | 输出组件和交互设计；不负责系统凭据、IPC 或安装规则。应同时满足离线字体/资源和高密度信息展示 |
| `run` | P1 起，实际开发/安装入口出现后启动桌面应用做冒烟 | 观察窗口、Go 就绪和报错；Vite 页面或开发窗口跑起来不是安装版通过 |
| `code-review`、`security-review` | P2–P6，已有源码或差异后审查协议、权限、迁移、打包及代码改动 | 给可复核的文件/行为和修复建议；不能用技能评语替代目标机试装、系统密钥库或签名验证 |
| `find-skills` | 现有能力不覆盖某项具体工作时，查找外部候选 | 从目录到发布方仓库核实，再检查安装脚本、权限、适用版本与 license；查到不等于启用 |

## 2. 已核实、尚未安装的外部候选

先查 [skills.sh 排行目录](https://skills.sh/)，再核对条目、上游 `SKILL.md` 和仓库。页面的 `Installs` 未注明统计周期，星数是**整个仓库**的数量；热度只用于初筛，不是安全或兼容性证明。

| 技能 / 来源 | 适用边界 | 此次核验 |
|---|---|---|
| `vercel-react-best-practices` · [目录](https://skills.sh/vercel-labs/agent-skills/vercel-react-best-practices) · [上游规则](https://raw.githubusercontent.com/vercel-labs/agent-skills/main/skills/react-best-practices/SKILL.md) · [Vercel 仓库](https://github.com/vercel-labs/agent-skills) | 推荐在 P4 有性能数据后针对 React/Vite：去重监听、减少不必要渲染、按需加载日志详情；跳过 Next.js、RSC、SSR 专属规则 | `Installs` 910.5K；仓库约 3.4 万星。适合 renderer，不改变 IPC 信任边界 |
| `webapp-testing` · [目录](https://skills.sh/anthropics/skills/webapp-testing) · [上游规则](https://raw.githubusercontent.com/anthropics/skills/main/skills/webapp-testing/SKILL.md) · [Anthropic 仓库](https://github.com/anthropics/skills) | 可选，用 Python Playwright 驱动本地 Vite 页面和模拟 preload，验证任务错误/取消/重试等界面行为；已有 Vitest/Playwright 覆盖足够时可以不装 | `Installs` 208.3K；仓库约 19.7 万星。**不能**证明 Electron IPC、真实 Go、操作系统凭据库或安装包可用 |

**Go 专项候选：** [`go-concurrency-patterns`](https://skills.sh/wshobson/agents/go-concurrency-patterns)（[上游 SKILL.md](https://raw.githubusercontent.com/wshobson/agents/main/plugins/systems-programming/skills/go-concurrency-patterns/SKILL.md)，[社区仓库](https://github.com/wshobson/agents)，不是 Go 官方技能）。页面 `Installs` 11.8K、仓库约 4.3 万星。P3/P4 可用来评审 `context` 取消、goroutine 退出、并发检测/卸载的限流与优雅关闭。**不能**将其中的通用工作池套到必须全局串行的批量安装；用可控时钟和模拟执行器验证实际并发数、取消后无泄漏、没有重复写操作。

**可选安装方式，不是项目初始化步骤，本次未执行。** 若日后决定试用，在独立分支或隔离目录核对发布者、上游版本/提交、许可证、会写入的位置和附带脚本；确认后才执行，为项目记录所选版本。不要默认全局安装或跳过审核。三个候选独立选择：

```bash
npx skills add https://github.com/vercel-labs/agent-skills --skill vercel-react-best-practices
```

```bash
npx skills add https://github.com/anthropics/skills --skill webapp-testing
```

```bash
npx skills add https://github.com/wshobson/agents --skill go-concurrency-patterns
```

## 3. Go、Electron 和迁移专项：先用一手资料

本次未选入额外的 Go 远程执行、Electron 打包或生产迁移类第三方技能；不意味着不存在。对掌握 SSH 凭据、签名身份和真实数据库的项目，优先用受限的本地工具、带审查的小型夹具和以下规范。它们是**文档，不是可安装技能**。

| 领域 | 官方/上游入口 | 用它核对什么 |
|---|---|---|
| Go 与 SSH | [Go 数据访问](https://go.dev/doc/database/) · [Go testing](https://pkg.go.dev/testing) · [x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) | 事务、子测试、连接及主机密钥校验；通过授权测试机验证，不把真实主机交给第三方技能 |
| Electron 与分发 | [Electron 安全](https://www.electronjs.org/docs/latest/tutorial/security) · [electron-builder 格式](https://www.electron.build/v26/docs/targets) · [代码签名](https://www.electronjs.org/docs/latest/tutorial/code-signing) | preload 白名单、隔离、辅助程序/安装包签名、各平台构建约束 |
| 真桌面回归与数据导入 | [Playwright Electron API](https://playwright.dev/docs/api/class-electron) · [SQLite Online Backup](https://www.sqlite.org/backup.html) | 指向已打包可执行文件的真实桌面冒烟（Electron API 仍为实验性），以及旧库一致快照的正确做法 |

**选择原则：** 新版没有 WPF，排除 WPF 美化技能；没有远程 Web 服务，不引入面向公网部署、SSR 或 Kubernetes 的技能。任何技能都不应默认得到终端输出、生产 IP、带密数据库、签名凭据或自动发布权限。
