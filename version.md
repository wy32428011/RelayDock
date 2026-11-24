# 版本记录（Version History）

本文件记录 **RelayDock（远程安装应用升级版）** 的版本演进与重要变更。

---

## v0.1.1（2026-11-23）

### 变更内容：项目开发规范更新与项目级技能安装

- **重写跨工具共享开发规则（`AGENTS.md`）**
  - 补充代码结构速览、文档导航及常用构建/测试命令清单。
  - 细化三层职责隔离、协议先行、架构/离线资源边界、旧行为证据移植、副作用可见性及两代隔离等工程约束与变更验收标准。
- **更新 Claude Code 项目指引（`CLAUDE.md`）**
  - 明确会话启动与按需上下文加载规范、技能使用与高权限/外部操作边界、验证结论汇报口径及文档维护分工。
- **安装并锁定 9 项项目级开发技能（`.claude/skills/`、`skills-lock.json`）**
  - **Go 后端：** `golang-concurrency`、`golang-security`、`golang-testing`、`golang-database`
  - **前端与测试：** `vercel-react-best-practices`、`vitest`、`playwright-best-practices`
  - **脚本与桌面体验：** `bash-defensive-patterns`、`native-feel-cross-platform-desktop`
- **同步技能目录文档（`docs/SKILLS.md`）**
  - 梳理会话内置技能、已安装项目级技能（含上游来源、锁定提交与使用边界）、未安装可选候选及官方文档一手参考，并移除上游已失效的 `partme-ai/full-stack-skills@electron` 条目。

### 初始基线（`7daed80`）

- 完成 **RelayDock v0.1.1** 初始代码提交，包含 Go 核心进程（`backend/`）、Electron + React/TypeScript 桌面工作台（`apps/desktop/`）、跨进程协议契约（`packages/protocol/`）、远端应用目录与脚本（`resources/`）、打包脚本（`packaging/`、`scripts/`）及配套文档与测试套件（`docs/`、`tests/`、`testdata/`）。
