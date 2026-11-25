# 版本记录（Version History）

本文件记录 **RelayDock（远程安装应用升级版）** 的版本演进与重要变更。

---

## v0.1.5（2026-11-25）

### 变更内容：右侧远端文件列表右键菜单新增「终端切到当前目录」功能

1. **右键菜单快捷操作区新增「终端切到当前目录」**
   - 在右侧文件列表右键菜单顶部快捷操作栏首位新增 **终端切到当前目录** 快捷图标按钮（`terminal-files.tsx`），点击后将当前激活终端会话切换至文件面板所在目录，并保留原「新建文件夹」入口于工具栏与「更多」子菜单中。
2. **实现安全的终端目录切换与命令转义**
   - 在终端面板与会话视图中暴露输入发送句柄（`terminal.tsx`），对含空格及特殊字符的 POSIX 路径自动做单引号安全转义（`cd -- '...'`）并兼容 Windows 远端路径，同时拦截控制字符；终端未连接时给出提示而不盲目发包。
3. **补充配套单测、提交发布规范并升级版本号至 `0.1.5`**
   - 在 `widgets.test.tsx` 中补充未连接提示与含空格路径安全转义发送 `terminal.input` 的前端单元测试，并在 `AGENTS.md` / `CLAUDE.md` 中完善提交与发布规则。
   - 统一将版本号升级至 `0.1.5`（`package.json`、`apps/desktop/package.json`、`package-lock.json`、`backend/internal/state/store.go`）。

---

## v0.1.4（2026-11-24）

### 变更内容：右侧远端文件列表右键菜单新增修改权限功能

1. **右键菜单新增「修改权限」弹窗**
   - 在终端右侧文件列表右键菜单中新增 **修改权限** 入口（`terminal-files.tsx`、`widgets.css`），弹出权限配置弹窗并回显当前文件/目录的 POSIX 权限位。
   - 支持 **用户 / 组 / 任何人** 的 **读 / 写 / 执行** 复选框配置与实时八进制及符号文本预览（如 `权限:776，文本:drwxrwxrw-`），并提供 **sudo 提权**、**快速执行** 与 **递归修改子目录及文件**（目录可用）选项。
2. **新增 `files.chmod` 跨进程协议与 Go 核心权限修改实现**
   - 扩展 `RemoteEntry.mode` 与 `files.chmod` 协议契约及 Electron 主进程白名单校验（`packages/protocol/index.ts`、`core.ts`、`validation.ts`），拒绝根目录 `/` 修改且要求用户显式确认。
   - 在 Go 核心（`backend/internal/remote/chmod.go`、`files.go`、`service.go`）实现 `files.list` 权限位回传及 `files.chmod` 执行逻辑，支持 SFTP 单项/后序递归 `Chmod` 与 SSH `chmod` / `sudo -n chmod` 快速执行模式。
3. **补充配套测试并升级版本号至 `0.1.4`**
   - 补充 Go 核心权限解析、SFTP 递归、快速执行与 `sudo` 命令单测（`chmod_test.go`）及前端权限弹窗交互单测（`widgets.test.tsx`）。
   - 统一将版本号升级至 `0.1.4`（`package.json`、`apps/desktop/package.json`、`package-lock.json`、`backend/internal/state/store.go`）。

---

## v0.1.3（2026-11-24）

### 变更内容：终端三栏工作台重构、右侧文件列表右键菜单与跨服务器文件传输

1. **升级终端视图为三栏工作台并支持多开标签**
   - 终端工作区升级为“左侧服务器列表 + 中间可多开终端标签 + 右侧远端文件列表”的三栏联动布局（`terminal.tsx`、`hosts.tsx`、`App.tsx`），支持拖拽调整右侧栏宽度与快速折叠展开。
   - 切换或关闭终端标签时自动同步右侧当前主机的文件目录，并支持为同一主机或不同主机多开独立终端会话。
2. **右侧远端文件列表新增右键操作菜单与快捷编辑**
   - 新增 `terminal-files.tsx`，在文件条目或空白区域右击可弹出完整操作菜单，覆盖新建文件夹、复制/剪切/粘贴、打开、在线文本编辑（内置编辑弹窗）、刷新、收藏路径、下载/上传文件与目录、SCP 定向传输、压缩/解压、重命名及删除。
3. **支持文件与文件夹跨服务器传输及远端归档解压**
   - 扩展 `files.transfer`、`files.archive`、`files.extract` 跨进程协议与 Electron 主进程白名单校验（`packages/protocol/index.ts`、`core.ts`、`validation.ts`），高危写入与传输动作均需用户显式确认。
   - 在 Go 核心（`backend/internal/remote/transfer_remote.go`）实现同机与跨服务器递归流式文件/目录传输（含复制与剪切、权限保留与覆盖控制）及 `.tar.gz`/`.zip` 归档解压能力。
4. **补充配套测试并升级版本号至 `0.1.3`**
   - 补充 Go 核心跨机传输/归档单元测试（`transfer_remote_test.go`）及前端多开终端与右键菜单单测（`widgets.test.tsx`）。
   - 统一将版本号升级至 `0.1.3`（`package.json`、`apps/desktop/package.json`、`package-lock.json`、`backend/internal/state/store.go`）。

---

## v0.1.2（2026-11-24）

### 变更内容：GitHub Releases 自动更新、Windows 发布工作流与离线资源 Git LFS 托管

1. **接入 GitHub Releases 自动更新引擎**
   - 在 Electron 主进程新增 `AppUpdater`（`apps/desktop/src/main/updater.ts`、`desktop.ts`、`validation.ts`），支持按当前系统架构匹配发行版资产、流式下载安装包与 SHA-256 完整性校验，并在启动 Windows NSIS 安装程序前优雅关闭 Go 核心进程。
2. **完善更新协议、预加载白名单与桌面端界面交互**
   - 扩展 `packages/protocol/index.ts` 与 `apps/desktop/src/preload/channels.ts` 的 `update.*` 接口及严格 IPC 校验。
   - 在设置工作区（`settings-workspace.tsx`）新增版本检测、下载进度展示与一键安装控件，并在应用顶栏（`App.tsx`）增加新版本提示徽标。
3. **新增 Windows 发行版自动打包发布工作流与配套单测**
   - 新增 `.github/workflows/release.yml`，支持推送 `v*` 标签或手动触发构建 Windows `x64` / `arm64` 安装包、生成 `SHA256SUMS.txt` 并自动发布至 GitHub Releases。
   - 补充更新器、IPC 边界校验与设置页组件单元测试（`updater.test.ts`、`boundary.test.ts`、`widgets.test.tsx`）。
4. **离线资源包纳入 Git LFS 托管并升级版本号**
   - 通过 Git LFS 追踪 `offline-resources/` 下 80 组经校验的离线应用与依赖包（同步 `.gitattributes` 与 `offline-resources/INVENTORY.md`），保持 `Packages` 索引二进制完整性以确保 SHA-256 清单一致。
   - 统一将版本号升级至 `0.1.2`（`package.json`、`apps/desktop/package.json`、`package-lock.json`、`backend/internal/state/store.go`）。

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
