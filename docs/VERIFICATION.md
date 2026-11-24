# RelayDock 0.1.0 验收记录

本轮在 Windows 10 Pro for Workstations 22H2（10.0.19045）x64 上执行，使用 Node.js 22.22.0、Go 1.26.11、Electron 45.3.4、electron-builder 26.18.0 和 IPC v1。测试数据使用临时目录、合成旧库和回环 SSH/SFTP 夹具。相邻旧版工作树只用于读取迁移依据与离线资源。

## 客户端安装包

| 工作站 | 产物与实测状态 | 大小 |
|---|---|---:|
| Windows x64 | 已构建 NSIS，已从实际安装目录启动、验证和卸载。[安装包](../artifacts/win32-x64/RelayDock-0.1.0-win-x64.exe) · [产物清单](../artifacts/win32-x64/BUILD.json) | 125,703,100 字节（119.88 MiB） |
| Windows ARM64 | 已构建 NSIS，检查了包的摘要，以及 Electron 与 Go 两个可执行文件的 ARM64 PE 架构。原生试装待验证。[安装包](../artifacts/win32-arm64/RelayDock-0.1.0-win-arm64.exe) · [产物清单](../artifacts/win32-arm64/BUILD.json) | 119,339,295 字节（113.81 MiB） |
| macOS Intel / Apple Silicon | 两个目标的 Go 交叉编译通过。DMG 构建入口和原生运行器配置已就绪，完整客户端包和试装待在 macOS 上验证。 | — |
| Linux x64 / ARM64 | 两个目标的 Go 交叉编译通过。DEB、AppImage 构建入口和原生运行器配置已就绪，完整客户端包和试装待在 Linux 上验证。 | — |

两份 Windows 安装包均为本地未签名试装产物。源码中提供 [六目标构建配置](../.github/workflows/build.yml)，此次交付使用本机产物，跨平台 CI 尚未运行。正式分发所需的发布者签名、macOS 公证以及各系统最低版本仍待验证。

### Windows x64 试装证据

- NSIS 按当前用户安装到包含中文与空格的临时目录，并从该目录启动 `RelayDock.exe`。应用进程继承仅有系统路径的 `PATH`，Go 核心来自安装包。验证了同架构 IPC 握手、`file:` 页面、隔离的 preload、12 个应用 ID，以及添加主机后取消不留下记录。
- 从打包的工作台预览并导入合成离线资源和合成旧库。预览时新版库无对应记录，确认后可读取资源和主机记录，已导入的旧主机待补凭据和指纹。源快照的文件字节保持一致。亮暗主题可切换，测试期间窗口未发出 HTTP 或 HTTPS 请求。
- 退出后验证 Go 子进程已结束。NSIS 卸载清除了试装程序、相应注册表项和快捷方式。本次试装使用临时应用数据，尚未演练日常用户库保留、跨版本升级或干净系统上的断网安装。
- 安装包 SHA-256 已从文件重新计算并与 `BUILD.json` 对照。包内归档包含桌面界面和主进程，外部资源目录包含同架构 Go、应用目录和 83 份 `.sh` / `.ps1`。工作台截图来自实际试装，图中的主机为合成数据。[截图](../artifacts/verification/workbench.png)

## 功能与验证范围

| 范围 | 已验证 | 待完成或未验证 |
|---|---|---|
| 主机、终端与远端文件 | 主机 CRUD 和平台字段、首次指纹核对、指纹变化拦截、复用已批准目标、SSH 认证与 PTY、SFTP 传输和覆盖保护有回环或合成测试。 | Windows Server、Ubuntu、CentOS 的真机兼容性，以及各客户端原生凭据库实写。 |
| 目录、安装和任务 | 12 个应用 ID、10 个家族。现有检测脚本进入只读管线。Go 测试覆盖结果判定、全局安装 FIFO、按主机限并发及复用连接、队列恢复、任务取消、终态持久化与脱敏。Mosquitto 安装验证覆盖回环 SSH/SFTP 的包传输与复查。 | 0.1.0 的内置写入仅支持 Ubuntu 24.04 amd64/arm64 的 Mosquitto 2.1.2 首次离线安装及本版登记实例的保留数据卸载。实际服务器闭环、其他组件安装和卸载、在线安装和旧版 JDK 文件夹安装入口待完成。 |
| 脚本、配置与自定义部署 | 远端文件、脚本和配置文本编辑、写入前差异与冲突校验。Traefik 的主配置和动态配置、Elasticsearch 的配置和 JVM 参数可分别选择。自定义定义保存和操作确认有测试。 | 旧版结构化配置编辑器尚未移植，真实自定义部署的端到端运行未验证。 |
| 资源 | 精确匹配远端组合，逐文件校验大小、SHA-256 与依赖。Ubuntu ARM64 系统包核对 `Packages`。两份本地 Mosquitto 资源均用试装包内的 Go 核心完成预览和导入。 | 现有资源标记为人工审核 `reviewed`。正式发布签名验证和真机依赖满足情况待验证。 |
| 数据迁移与设置 | 六表元数据和关联使用合成库测试。覆盖旧 WAL、超过 100 条任务、50,000 条日志分页、重复导入、冲突拒绝及失败回滚。桌面试装从「设置 → 旧版数据」通过了主机预览和导入。 | 真实库迁移演练待验证。历史正文、凭据与旧自定义命令的处理见 [迁移方案](MIGRATION.md#5-sqlite-数据迁移和凭据补录)。 |

本地最终复测通过 `npm run typecheck`、`npm run lint`、`npm run test:contract`（2 项）、`npm run test:ui`（17 项）、`npm run test:core:stdio`、`npm run test:e2e:local` 和 `npm run test:e2e:packaged`。`go -C backend vet -mod=readonly ./...` 与 `go -C backend test -mod=readonly -count=1 -timeout=2m ./...` 通过。构建与复测入口见 [执行方案](EXECUTION.md)。

## 本地试用 Mosquitto 离线资源

下列资源从相邻旧版的 Ubuntu 24.04 目录筛选，主包和依赖与 Go 代码固定的包摘要一致。ARM64 目录带有对应 DEB 的 `Packages` 索引。两份目录独立于基础安装包，供本机验收使用。[包摘要来源](../backend/internal/operations/mosquitto_packages.go) · [资源格式](../resources/README.md)

| 远端 CPU | 资源目录 | 校验结果 |
|---|---|---|
| amd64 | `artifacts/offline-resources/mosquitto-2.1.2-ubuntu-24.04-amd64` | 4 个 DEB，清单和文件通过预检与导入。[清单](../artifacts/offline-resources/mosquitto-2.1.2-ubuntu-24.04-amd64/manifest.json) |
| arm64 | `artifacts/offline-resources/mosquitto-2.1.2-ubuntu-24.04-arm64` | 8 个 DEB 和 1 份 `Packages`，索引、清单和文件通过预检与导入。[清单](../artifacts/offline-resources/mosquitto-2.1.2-ubuntu-24.04-arm64/manifest.json) |

1. 按**工作站**的 CPU 安装 Windows 客户端。打开后确认底栏显示「本地核心就绪」与 `IPC v1`。
2. 选择「离线资源」，在目录选择器选中其中一个完整资源目录。根据**远端**架构核对 `amd64` 或 `arm64`，确认导入。
3. 在可恢复的 Ubuntu 24.04 测试机上核对指纹并保存主机。先检测 Mosquitto。选择离线安装版本 **2.1.2**，核对目标、资源和端口，再提交。安装要求远端有管理员权限、systemd 可用、原有包状态正常且没有现存 Mosquitto 或残留。APT 用目标机现有软件包和审核目录中的本地包解析依赖。
4. 核对安装状态，验证服务与所需监听，再为测试数据制作可复查记录。仅对本版登记的实例执行保留数据卸载，检查保留的配置、数据及非目标服务。若远端状态未知，查看任务日志并先做只读复查。

要演练旧数据，请从旧版退出后的副本或 SQLite 备份开始。「设置 → 旧版数据」支持只迁主机，也可导入全部可迁记录。例如从 Windows x64 工作站管理 Ubuntu ARM64，使用 x64 客户端和 ARM64 中间件资源各自对应上述表格。
