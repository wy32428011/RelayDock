# CLAUDE.md

This file provides Claude Code-specific guidance when working in this repository.

@AGENTS.md

上述导入为本项目所有工具共享的架构、命令与工程边界。本文件仅规定 Claude Code 的上下文加载、技能选用、验证汇报与文档维护方式，不重复维护技术栈或规则清单。

## 会话启动与上下文加载

- **先核对工作目录：** 若会话同时涉及相邻旧版目录（`远程安装应用`）与升级版，写文件、跑命令、生成产物前必须确认目标指向**升级版**。绝不把旧版的 WPF / DI / XAML 约定带入新版。
- **按需读取，避免全量塞入上下文：**
  - 查架构边界、IPC 约定或数据归属 → 按章节读 [技术设计](docs/DESIGN.md) 与 [协议定义](packages/protocol/index.ts)。
  - 跑构建、测试或打包门禁 → 查阅 [执行方案](docs/EXECUTION.md) 与 [验收记录](docs/VERIFICATION.md)。
  - 核对旧版逻辑 → 先从 [迁移方案](docs/MIGRATION.md) 定位映射，只读目标旧代码片段。旧仓包含多 GiB 二进制资源及未提交改动，**禁止递归扫描离线包体**，也不从 `bin/`、`obj/`、`publish/` 目录推断权威实现。

## 技能使用与操作边界

- **按任务匹配技能：** 参考 [技能目录](docs/SKILLS.md) 选择当前会话已暴露的技能：
  - 界面设计/布局/交互审查：`ui-ux-pro-max`、`anthropic-skills:frontend-design`（仅用于渲染层，不能替代 Go 或 Electron 安全审查；WPF 技能不适用于本仓）。
  - 启动与冒烟桌面应用：`run`（注意 `npm run preview:ui` 仅验证前端布局，不能代替 Electron IPC 与 Go 联调或打包试装）。
  - 变更与安全审查：`code-review`、`security-review`（不能替代目标机试装、系统密钥库或代码签名验证）。
  - 检索外部扩展能力：`find-skills`。
- **区分本地开发与高权限/外部操作：** 阅读技能或文档说明不等于获得授权去安装外部技能/插件、执行第三方远程脚本、向外部服务发送项目内容、使用发布签名凭据或连接真实服务器。此类操作必须与本地开发/模拟验收分开，仅在用户明确批准后执行。

## 验证与结论汇报

- 汇报测试或验收结果时，必须写明**执行目录、平台/架构、使用的测试命令与夹具/安装包类型**（单测、回环模拟、开发版还是打包产物）。
- 未在目标平台（如 macOS、Linux、Windows ARM64 真机）实际运行的项目明确标注为**待验证**，不得用流程描述或交叉编译成功冒充真机运行记录。
- 避免将开发机绝对路径写入共享文档或代码注释中。

## 维护分工

- 跨工具共享规则、目录结构与常用命令 → 更新 [AGENTS.md](AGENTS.md)
- 构建/测试入口或阶段门禁变化 → 更新 [执行方案](docs/EXECUTION.md)
- 已核实的旧版兼容例外或迁移行为 → 更新 [迁移方案](docs/MIGRATION.md)
- 实测环境、构建产物与平台验收状态 → 更新 [验收记录](docs/VERIFICATION.md)
- 可用与候选技能清单 → 更新 [技能目录](docs/SKILLS.md)
- 仅限 Claude Code 的工作流约束 → 更新本文件
