# 技术设计：桌面远程安装器

本文记录新版的架构和放行条件。当前可用功能、已验证环境和安装包见 [验收记录](VERIFICATION.md)。旧行为与兼容差异见 [迁移方案](MIGRATION.md)，实施顺序见 [执行方案](EXECUTION.md)。

## 1. 范围与取舍

面向运维人员在工作站上管理获授权的 Windows Server、Ubuntu、CentOS 等远端。保留主机管理、应用市场、安装/检测/卸载、批量操作、任务历史、SSH 终端、SFTP、脚本管理、自定义部署、配置编辑、设置和更新检查。第一条链路只做**安装桌面客户端 → 打开本地窗口 → Go 就绪 → 检测一台测试主机上的应用 → 看到可解释的结果**。

| 决策 | 选用 | 支付的成本 / 不选的做法 |
|---|---|---|
| 界面与桌面壳 | React + TypeScript + Vite；Electron 主进程与 preload 用 TypeScript | 自带 Chromium/Node，重于系统 WebView；不承诺最小包或最低内存。生产 UI 随包离线加载，无 CDN、常驻 Web 服务或 WPF |
| 本地业务进程 | 单独编译的 Go 可执行文件，由 Electron 拉起；SSH 用 `golang.org/x/crypto/ssh`，SFTP 用 `github.com/pkg/sftp` | SSH/安装编排需重写；业务不拆成多份 Node 原生插件，也不要求用户安装 Go |
| 数据 | Go 的 `database/sql` + SQLite；优先验证纯 Go 驱动 `modernc.org/sqlite` | 减少打包时对 C 工具链的依赖，但须核对目标架构支持与二进制体积；凭据另存，不能放入普通设置表 |
| 终端与测试 | `@xterm/xterm`、`@xterm/addon-fit`；Go `testing`、Vitest、React Testing Library、Playwright Electron 冒烟 | xterm 是显示器，Go 才拥有远端 PTY。Playwright 的 Electron API 为实验性；自动化之外保留安装版人工试装 |
| 分发 | electron-builder + 分目标系统/架构的 Go 构建；独立离线资源包 | 不把全套中间件塞入基本安装包；也不因客户端跨平台而扩大某个中间件实际支持的远端范围 |

## 2. 进程与目录所有权

调用路径：**本地 React 页面 → preload 白名单 → Electron 主进程 → 标准输入/输出 → Go 服务 → SQLite / 系统凭据库 / SSH 或 SFTP**。远端脚本只能产生数据事件，不能反向要求主进程执行本地操作。

| 目录 | 所有权与禁止事项 |
|---|---|
| `apps/desktop/src/renderer/` | 页面、用户输入、任务与终端显示；不直接读盘、操作 SQLite、选包或拼接远程命令 |
| `apps/desktop/src/preload/`、`apps/desktop/src/main/` | 前者仅暴露有类型的方法；后者验证来源/参数、显示文件选择器、管理显式授权和 Go 生命周期。不提供通用 `exec`、任意文件读取或原始 IPC |
| `backend/cmd/installer-core/`、`backend/internal/` | 握手、连接、队列、协议解析、结果判定、资源、存储与凭据。用接口隔开实际 SSH/SFTP、文件系统、系统凭据库与时钟，使模拟测试不连服务器 |
| `packages/protocol/`、`testdata/` | 版本化 JSON Schema 与跨语言同一组正反例；测试夹具仅用合成数据、文本轨迹与微型假包 |
| `resources/`、`packaging/` | 前者为内置目录和小型脚本，后者保存打包图标。大包从用户选择的目录导入，缓存放在新版数据目录 `offline-resources/`，客户端安装包不携带缓存。正式资源的可信签名验证仍待接入 |

**生命周期：** 主进程创建单实例锁；按受控安装位置寻找本机 Go 文件，以 `spawn`、`shell:false` 启动。先握手版本/能力，通过后启用操作。Go 未启动、版本不匹配或异常退出时，保留窗口并显示错误，禁用有副作用的入口；重新启动不得重放旧写请求。退出前展示活跃任务、让用户决定是否继续退出；关闭会话、终止子进程、回收文件句柄。重开后从本机库找未终结任务，以“中断/待复查”显示，而非假设服务器已回滚。

## 3. 本地 IPC v1

主进程和 Go 用 UTF-8 NDJSON，经 `stdin/stdout` 双向交换；`stdout` **只能**写协议帧，诊断写脱敏后的 `stderr`。这与远端脚本输出协议是两层：脚本行先由 Go 解析，再生成面向 UI 的事件。React 与主进程是另一道权限边界，不桥接任意方法名。

```jsonl
{"v":1,"type":"request","id":"r1","method":"hello","params":{"supported":[1]}}
{"v":1,"type":"response","id":"r1","ok":true,"result":{"selected":1,"capabilities":["operation.detect"]}}
{"v":1,"type":"request","id":"r2","method":"operation.start","params":{"kind":"detect","hostId":"h-demo","appId":"redis","requestKey":"demo-1"}}
{"v":1,"type":"response","id":"r2","ok":true,"result":{"taskId":"t1"}}
{"v":1,"type":"event","topic":"task.progress","taskId":"t1","seq":1,"data":{"stage":"detecting","percent":40}}
{"v":1,"type":"response","id":"r3","ok":false,"error":{"code":"RESOURCE_MISMATCH","message":"缺少匹配目标架构的离线资源","retryable":false}}
```

最后一行是独立的失败响应示例，对应另一请求 `r3`。示例不含真实主机或凭据。接口演进有以下约束：

- `id` 只关联一次请求与一次响应；任务和终端分别用 `taskId` 和 `sessionId`。有副作用的开始请求带持久化 `requestKey`，服务返回已有任务而不是重复安装。拒绝无共同主版本、非法类型、超长帧、未知方法与不合当前会话的 ID。
- 主机方法负责新建/测试/存储；资源方法负责预检/导入；任务方法负责开始/取消/查询快照；终端方法负责打开/输入/缩放/关闭；SFTP 按动作开放。UI 只传不透明目标 ID、选项及短时文件授权，不传可执行文件路径、自由拼装的命令或无限制文件句柄。显式人工终端输入与用户自定义部署命令各走专用途径，不混作安装参数。
- 终端字节分块编码传输（v1 可用有上限的 Base64 块）；不把每个字符发成一个请求，不把整份终端记录保存在 React 状态。每个会话独立缓冲/流控；子进程断连时拒绝悬空请求。上传包和文件走 Go 文件描述符 + SFTP 流，不经 JSON 传大文件。
- 任务事件单调 `seq`；UI 发现缺口即取 `operation.get` 的快照与缺失日志。高频进度可以合并；终态/告警不能丢、不可被迟到进度覆盖。写库成功后才发送终态事件。错误用稳定代码区分 `HOST_KEY_CHANGED`、`AUTH_REQUIRED`、`RESOURCE_MISMATCH`、`CORE_UNAVAILABLE`、`CANCELLED` 等，日志详情不带秘密。

## 4. 操作管线与状态

0.1.0 的内置远端写入路径为 Ubuntu 24.04 amd64/arm64 上 Mosquitto 2.1.2 的首次离线安装，以及本版已登记实例的保留数据卸载。以下描述同时定义了其余组件接入时的规则。各类操作及实测范围见 [验收记录](VERIFICATION.md)。

1. **确认和预检：** 校验主机及指纹、目标能力、操作与应用参数、资源唯一性和权限。安装请求明确 `sourceMode=offline|online`：默认离线，离线缺件拒绝；显式在线仅对经验证的应用/远端组合开放，显示受信来源、需要远端联网及预期版本，由用户确认，绝不自动切换模式。UI 在上传/安装前展示“主机、远端 OS/CPU、应用版本、来源、操作影响”。预检失败不产生远程变更。
2. **执行：** Go 创建持久任务，按任务隔离临时目录，上传所需脚本与包。Linux 使用相应 Bash 入口，远端 Windows 用适用的 PowerShell 入口；本地操作系统不决定远端脚本。传递结构化参数，按目标平台处理路径、编码和换行。
3. **判定：** 并行收集 stdout、stderr、退出状态与脚本阶段；兼容 `PROGRESS:阶段:百分比`、状态/结束标记及 JSON 行，普通行作日志。安装/卸载都再检查目标状态，给证据单独标记“已存在、确认不存在、未知”；卸载复查失败且没有可靠状态时只能给“待复查”，不能采用默认 `false` 宣布卸载完成。若用户选择保留数据，预检必须能限定影响范围；复查同时验证保留项及非目标服务。具体决策表与逐组件门槛在[迁移方案](MIGRATION.md)。
4. **调度：** 批量安装全局串行；批量检测和卸载按主机限并发，同主机内按应用串行，检测复用连接。连接/只读检测允许有界重试；对已发出的写请求不盲重试。终端和安装任务使用不同逻辑会话，互不吞输出。
5. **中断：** 内部状态建议 `queued → preflight → running → verifying → succeeded / succeeded_with_warning / failed`；未开始的取消可直接 `cancelled`。正在执行时先 `cancelling`；连接中断、进程被杀或远端不确认停止则标 `interrupted`，要求只读复查。暂停显示与暂停远程操作是不同能力，未证实后者时不得宣称远端已暂停。

## 5. 数据、凭据与资源

| 数据 | 归属与写法 | 故障时的行为 |
|---|---|---|
| 主机/设置/自定义应用、任务/历史/日志索引 | Go 写独立 SQLite；主进程提供新版 `userData` 绝对路径。不写安装目录；用版本化事务迁移、稳定 ID 和分页；日志明细按需获取 | 数据库升级前创建可验证快照；写失败显示错误，不宣布成功。旧库只读导入，不共用 WAL |
| 主机密码、密钥口令、仓库令牌 | 凭据库存机密，SQLite 只存引用。Windows 使用凭据管理器或用户级 DPAPI 适配；macOS Keychain；Linux Secret Service；私钥路径在本机重新确认 | 只在输入瞬间穿过受限 IPC；不回显旧密码，不向工具日志输出。密钥库锁定/缺失时仅明确授权的本次会话可用，不降级到明文文件 |
| 远端脚本与配置 | 小型随包受控脚本以版本标识；Go 按清单选择。远端配置先取快照、显示差异并检查写前版本；失败保留原件及可执行的恢复说明 | 未知配置形态允许文本模式；不为支持结构化编辑而丢弃注释、未知字段或 YAML 列表 |
| 离线包、索引与临时传输 | 独立资源根目录；清单至少含 schema、来源、应用/版本、目标 OS/版本/CPU、相对路径、长度、SHA-256 与依赖。正式发布校验可信签名/固定信任来源，散列仅证明文件与清单相符 | 先流式验大小/哈希、校验索引和依赖闭包，再原子启用；拒绝越界路径、歧义、缺件和错误架构。失败只清理属于本任务的暂存文件，不替用户删除其他包 |

**选择示例：** Windows x64 运维电脑连接 Ubuntu 24.04 ARM64 服务器，启动的是 Windows `amd64` Go 程序，选择的是 Ubuntu 24.04 `arm64` 的中间件。资源不存在属于远端组合缺失，不是重装客户端能解决的问题。

## 6. 界面与用户可感知的行为

- **主工作区：** 左侧主机/分组与搜索，中间主机详情、应用市场、脚本、自定义部署、终端/文件，任务与日志可展开或收起。切换主机时取消或隔离上一主机的未返回查询，响应只能更新所属主机；后台任务不因切页丢失。
- **安装表单：** 公共项先显示，按应用定义展示版本/端口等参数。字段附近解释错误；缺资源、平台不支持和缺凭据要给出不同的修复动作。卸载/覆盖前显示目标与影响，默认焦点放安全动作；对话框内 Tab 不逃逸，关闭后焦点回原控件。
- **任务与历史：** 活动列表只显示摘要，打开详情才读日志；列表虚拟化、日志批量刷新，用户向上翻阅时暂停自动跟随。状态配文字而不只靠红绿；屏幕阅读器播报阶段与终态，不逐行播报终端输出。暗色/亮色模式都要有可见键盘焦点。
- **终端与文件：** Go 创建远端 PTY，xterm 转发字节，窗口大小更新远端列/行；覆盖中文、ANSI、`Ctrl+C`、复制/粘贴和重连。SFTP 不可用时提示具体限制，SSH 终端仍可用。文件覆盖、递归删除与脚本保存走独立确认。

## 7. 权限、故障与可观测性

生产窗口只加载打包的本地页面，`nodeIntegration:false`、`contextIsolation:true`、沙箱和限制性 CSP。拒绝未授权的导航、外部窗口、权限请求、子帧 IPC；主进程检验发送来源/窗口与业务授权，preload 不透传原始事件。Go 独立校验参数、资源目录和路径；不能把 JSON Schema 当完整的授权机制。远端 stdout、配置值和文件名在界面按文本呈现，不解释成 HTML。

首次连接显式核对并保存主机指纹，变化阻止连接，独立流程重新确认。用户输入的命令仅在已授权目标和会话执行；内置动作不自动提升权限。诊断使用请求/任务 ID 关联桌面与 Go，记录版本、阶段、耗时、资源 ID；凭据、令牌、带密码的参数和原始终端输入不进审计。存储/导出前都做脱敏；不默认上传遥测、终端内容或崩溃数据。

## 8. 构建与分发

- Electron `x64` 对应 Go `amd64`；`arm64` 对应 Go `arm64`。Electron `win32` 对应 Go `windows`；`darwin`、`linux` 名称相同。客户端无需额外安装 Node.js、Go、.NET。Linux 桌面系统库、最低系统版本及各架构驱动支持仍须以试装为准。
- Windows/Linux 通过 `extraResources` 将 Go 放入 `resources/bin/`，由 `process.resourcesPath` 解析；macOS 将已签名可执行程序置于应用包 `Contents/Helpers/`，再签主应用。不能从 `app.asar` 直接启动 Go，不能依赖开发机 `PATH` 或当前目录。Unix 产物校验可执行位。
- Windows 优先 NSIS `.exe`；macOS 优先 `.dmg`（Intel/Apple Silicon 分包）；Linux 优先 Ubuntu/Debian `.deb`，按支持发行版补 `.rpm`，需要便携可给 AppImage（不是传统安装器）。按平台原生构建机生成和试装；签名、公证、产物清单、校验和与发布审批见[执行方案](EXECUTION.md)。
- 基础包、可选离线资源包和用户数据三者各自有生命周期。更新检查与安装更新分开；首版不自动替换 Go、脚本或离线包。支持旧 CentOS 服务器的安装脚本，不代表支持在 CentOS 7 桌面上运行 Electron。

参考：[Electron 安全模型](https://www.electronjs.org/docs/latest/tutorial/security) · [Go 数据访问](https://go.dev/doc/database/) · [electron-builder 目标格式](https://www.electron.build/v26/docs/targets) · [Electron 分发签名](https://www.electronjs.org/docs/latest/tutorial/code-signing) · [Playwright Electron API](https://playwright.dev/docs/api/class-electron)。
