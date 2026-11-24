# 内置脚本与离线资源

本目录随桌面客户端分发，只包含应用目录和小型脚本。离线安装包不随客户端分发，不从相邻旧工作树自动搜索；资源管理只读用户在客户端目录选择器中明确选定的本地目录。

## 内置目录与脚本

`catalog.json` 依据旧版 `Scripts/app-configuration.json`（从相邻工作树的 `f9e83d6` 基点读取当时源码）构建：12 个 ID、10 个家族；JDK 8/11/17 共享 `JDK` 家族。`scriptDir` 明确指定 `scripts/` 下旧版大小写准确的目录。保留旧目录中的版本、参数与提示；旧 RabbitMQ 的密码默认值未迁入，MariaDB 和 RabbitMQ 的远程访问默认关闭；Nginx 工作进程接受 `auto` 或整数。

83 份脚本来自旧版 `RemoteInstaller/Scripts/<应用目录>/` 的同目录 `.sh`/`.ps1` 源文件，不来自 `bin`、`publish` 或二进制包。Bash 采用 UTF-8/LF，Redis 三份脚本移除 shebang 前的空行；PowerShell 采用 UTF-8 BOM/LF，便于 Windows PowerShell 5.1 读取中文；将有问题的顶层 `param` 移到首个可执行语句之前，并修正 Nginx 两处进度行的变量边界。Linux 保留同目录 `install_common.sh` 等依赖；未发现这些入口引用需单独随客户端分发的模板。资源中的运行时模板、依赖包或二进制应与对应资源包一并提供。

脚本选择使用**远端**操作系统：先找精确版本目录及实际历史 `v` 变体，再找应用通用目录。Ubuntu/CentOS 优先对应发行版入口，再找 `_linux.sh` 或同版本的通用 Bash 入口；Windows 只找 `_windows.ps1`。例如 MySQL/Redis 的内置 Windows 脚本只有检测，不能以 Linux 安装脚本替代。目录和脚本文件名按真实大小写核对。

这是**源码与入口清单**，不是已验证的安装矩阵。`supportsOnline` 全部关闭；未审查的旧版安装/卸载脚本不能据此启用。安装闭环和卸载保留范围需另按实际远端组合验收；没有对真机执行这些脚本。

## 导入用户审核资源

1. 选定一个应用、实际包版本和远端系统版本/CPU。比如 Ubuntu `24.04` 与 `22.04` 不通用，Windows 使用客户端探测返回的系统版本字符串。不要把旧版目录 `24` 当成目标版本号，也不要把旧市场可选版本当成包实物版本。旧 Redis Ubuntu 24 ARM64 的索引显示 `7.0.15`，而旧市场只有 `7.2.3`、`6.2.13`，这组旧包不得改标签导入。
2. 在**独立新目录**放入明确属于该组合的包、依赖、索引和必要配置。名字是目录内相对路径，使用 `/`，不能有链接、设备名、大小写冲突、隐藏的额外文件或空目录。不要纳入账户数据、私钥、令牌、日志或其他应用的包；不要修改旧仓库。
3. 在新目录根放置 UTF-8 `manifest.json`。可手写，或在升级版仓库根执行下面的生成工具（Git Bash，路径换成实际目录）。工具只读指定目录，散列逐文件流式计算，不执行脚本、不联网、不解压、不猜测版本或架构。先把输出重定向到**目录外**，检查后再放进去，以免输出文件被当成包内容。`--entrypoint` 仅用于单个包的相对路径，不是脚本；目录型软件包一般省略它。已知的文件依赖可以重复传 `--requires '源文件=依赖文件'`。

   ```bash
   node scripts/import-legacy-resources.mjs \
     --dir 'C:/offline/jdk17-ubuntu2404-arm64' --app jdk17 --version 17.0.15_6 \
     --os ubuntu --os-version 24.04 --arch arm64 --source '经审核的 Temurin 发布包' \
     --entrypoint 'OpenJDK17U-jdk_aarch64_linux_hotspot_17.0.15_6.tar.gz' \
     > 'C:/offline/jdk17-manifest.json'
   ```

4. 在资源管理中选择准备好的目录，检查来源、目标、大小和清单摘要，再确认导入。预览不是签名验证，也不等于已确认。确认必须对应同一目录和刚检查的摘要；预览过期、清单或文件变化时重新检查。资源整包复制校验通过后才原子进入新版用户数据目录的 `offline-resources/`；不会在原目录写文件。应用缓存中不应手工改动包；每次任务解析包会再次验文件。多个完全匹配的资源需要指定其中一个，不会自动联网或跨架构、版本选包。

### 清单格式 v1

下例的两个文件内容分别为 `abc` 和 `xy`（均不带换行），仅用于微型校验，**不是可安装包**。字段 `source` 是用于人工审阅的来源说明（不用作下载地址），不要填访问令牌、带凭据的地址或本机隐私路径。

```json
{
  "schema": 1,
  "appId": "jdk17",
  "version": "17.0.15_6",
  "os": "ubuntu",
  "osVersion": "24.04",
  "arch": "arm64",
  "source": "合成资源，仅用于校验",
  "entrypoint": "package.tar.gz",
  "files": [
    {
      "path": "package.tar.gz",
      "size": 3,
      "sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
      "dependencies": ["deps/config.txt"]
    },
    {
      "path": "deps/config.txt",
      "size": 2,
      "sha256": "769a4e6d0003189c7e96c5d9b7e810a0d11c3a12832527ec94b0f86d277f51ca",
      "dependencies": []
    }
  ]
}
```

- `arch` 为 `amd64`/`arm64`，`os` 为目录中存在的 `ubuntu`/`centos`/`windows`。`appId` 和 `version` 必须是目录中的精确版本。`osVersion` 必须等于远端探测值，不使用通配符。
- `files` 必须声明除 `manifest.json` 以外的每个文件。`size` 是字节数，`sha256` 是 64 位小写十六进制；`dependencies` 必须为数组，所有引用（包括传递依赖）须在同一清单中出现并通过大小与散列校验。没有依赖就使用 `[]`。清单 SHA-256 按原始字节计算，亦为资源 ID。
- Ubuntu ARM64 的 MySQL、MariaDB、Redis、Nginx、RabbitMQ、Mosquitto 必须有非空 `Packages` 索引，覆盖全部 DEB 并包含所选版本的主包；验证每个 `Filename`、`Size`、`SHA256` 和 `Architecture`。CentOS 7 ARM64 对这六类系统包保持拒绝。归档等无原生索引时，文件散列不证明其实际应用版本、CPU 或是否安全；须人工核实真实来源和平台。清单依赖校验不替代远端包管理器的依赖、目标环境及包内容检查。
- 保存上限：清单 2 MiB、单个 `Packages` 索引 16 MiB、单文件 2 GiB、单包合计 8 GiB、每包 4,096 个文件、缓存 512 个资源；另按设置的缓存上限核对总包体字节数。调低缓存上限只限制新导入，不自动删除资源。路径最长 240 个字符、最深 16 层；仅允许 ASCII 文件名字符 `A–Z a–z 0–9 . _ + @ % ~ -`，目录分隔符用 `/`。
- 当前无受信发布公钥和签名验证流程，资源只能标为 `reviewed`（用户确认），不能自行声明 `signed`。散列只是核对清单与文件，不证明发布者身份。

本地验证：从 `backend/` 运行 `go test ./internal/assets`。用例仅使用打包脚本文本与微型合成包，不执行远端脚本或读取用户凭据。
