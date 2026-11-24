# RelayDock 离线资源包清单（C:\projects\远程安装应用升级版\offline-resources）

- **总计**：`80` 个离线资源包目录，总计约 `8.58 GiB`
- **校验结果**：全部 80 个目录均已通过 `scripts/import-legacy-resources.mjs` 生成 `manifest.json`（含全文件 SHA-256），并通过 Go 核心 `backend/internal/assets` 的 `resource.inspect` 真实校验（**80/80 通过，0 失败**）。
- **仓库状态**：所有离线资源存放在项目根目录 `offline-resources/`，通过 Git LFS 纳入版本控制（二进制安装包与依赖包由 LFS 追踪，清单与索引保持原字节）；不随 `scripts/package.mjs` 打入基础桌面安装包，也未修改相邻旧版目录。

## 按目录明细（共 80 个）

| 目录名 | 应用 | 版本 | 目标系统 | 架构 | 文件数 | 大小 (MiB) | Entrypoint | 来源 |
|---|---|---|---|---|---:|---:|---|---|
| `consul-1.22.6-centos7-amd64` | consul | 1.22.6 | centos 7 | amd64 | 1 | 63.5 | `consul_1.22.6_linux_amd64.zip` | hashicorp-official |
| `consul-1.22.6-centos7-arm64` | consul | 1.22.6 | centos 7 | arm64 | 1 | 58.7 | `consul_1.22.6_linux_arm64.zip` | hashicorp-official |
| `consul-1.22.6-ubuntu22.04-amd64` | consul | 1.22.6 | ubuntu 22.04 | amd64 | 1 | 63.5 | `consul_1.22.6_linux_amd64.zip` | hashicorp-official |
| `consul-1.22.6-ubuntu22.04-arm64` | consul | 1.22.6 | ubuntu 22.04 | arm64 | 3 | 58.9 | `consul_1.22.6_linux_arm64.zip` | hashicorp-official |
| `consul-1.22.6-ubuntu24.04-amd64` | consul | 1.22.6 | ubuntu 24.04 | amd64 | 1 | 63.5 | `consul_1.22.6_linux_amd64.zip` | hashicorp-official |
| `consul-1.22.6-ubuntu24.04-arm64` | consul | 1.22.6 | ubuntu 24.04 | arm64 | 3 | 58.9 | `consul_1.22.6_linux_arm64.zip` | hashicorp-official |
| `elasticsearch-8.5.3-centos7-amd64` | elasticsearch | 8.5.3 | centos 7 | amd64 | 1 | 553.8 | `elasticsearch-8.5.3-linux-x86_64.tar.gz` | elastic-official |
| `elasticsearch-8.5.3-centos7-arm64` | elasticsearch | 8.5.3 | centos 7 | arm64 | 1 | 364.4 | `elasticsearch-8.5.3-linux-aarch64.tar.gz` | elastic-official |
| `elasticsearch-8.5.3-ubuntu22.04-amd64` | elasticsearch | 8.5.3 | ubuntu 22.04 | amd64 | 1 | 553.8 | `elasticsearch-8.5.3-linux-x86_64.tar.gz` | elastic-official |
| `elasticsearch-8.5.3-ubuntu22.04-arm64` | elasticsearch | 8.5.3 | ubuntu 22.04 | arm64 | 6 | 365.1 | `elasticsearch-8.5.3-linux-aarch64.tar.gz` | elastic-official |
| `elasticsearch-8.5.3-ubuntu24.04-amd64` | elasticsearch | 8.5.3 | ubuntu 24.04 | amd64 | 1 | 553.8 | `elasticsearch-8.5.3-linux-x86_64.tar.gz` | elastic-official |
| `elasticsearch-8.5.3-ubuntu24.04-arm64` | elasticsearch | 8.5.3 | ubuntu 24.04 | arm64 | 6 | 365.0 | `elasticsearch-8.5.3-linux-aarch64.tar.gz` | elastic-official |
| `elasticsearch-8.5.3-windows10-amd64` | elasticsearch | 8.5.3 | windows 10 | amd64 | 1 | 362.8 | `elasticsearch-8.5.3-windows-x86_64.zip` | elastic-official |
| `jdk11-11.0.27_6-centos7-amd64` | jdk11 | 11.0.27_6 | centos 7 | amd64 | 1 | 186.3 | `OpenJDK11U-jdk_x64_linux_hotspot_11.0.27_6.tar.gz` | adoptium-temurin-official |
| `jdk11-11.0.27_6-centos7-arm64` | jdk11 | 11.0.27_6 | centos 7 | arm64 | 1 | 183.2 | `OpenJDK11U-jdk_aarch64_linux_hotspot_11.0.27_6.tar.gz` | adoptium-temurin-official |
| `jdk11-11.0.27_6-ubuntu22.04-amd64` | jdk11 | 11.0.27_6 | ubuntu 22.04 | amd64 | 1 | 186.3 | `OpenJDK11U-jdk_x64_linux_hotspot_11.0.27_6.tar.gz` | adoptium-temurin-official |
| `jdk11-11.0.27_6-ubuntu22.04-arm64` | jdk11 | 11.0.27_6 | ubuntu 22.04 | arm64 | 8 | 184.1 | `OpenJDK11U-jdk_aarch64_linux_hotspot_11.0.27_6.tar.gz` | adoptium-temurin-official |
| `jdk11-11.0.27_6-ubuntu24.04-amd64` | jdk11 | 11.0.27_6 | ubuntu 24.04 | amd64 | 1 | 186.3 | `OpenJDK11U-jdk_x64_linux_hotspot_11.0.27_6.tar.gz` | adoptium-temurin-official |
| `jdk11-11.0.27_6-ubuntu24.04-arm64` | jdk11 | 11.0.27_6 | ubuntu 24.04 | arm64 | 8 | 184.1 | `OpenJDK11U-jdk_aarch64_linux_hotspot_11.0.27_6.tar.gz` | adoptium-temurin-official |
| `jdk11-11.0.27_6-windows10-amd64` | jdk11 | 11.0.27_6 | windows 10 | amd64 | 1 | 190.0 | `OpenJDK11U-jdk_x64_windows_hotspot_11.0.27_6.zip` | adoptium-temurin-official |
| `jdk17-17.0.15_6-centos7-amd64` | jdk17 | 17.0.15_6 | centos 7 | amd64 | 1 | 183.1 | `OpenJDK17U-jdk_x64_linux_hotspot_17.0.15_6.tar.gz` | adoptium-temurin-official |
| `jdk17-17.0.15_6-centos7-arm64` | jdk17 | 17.0.15_6 | centos 7 | arm64 | 1 | 181.9 | `OpenJDK17U-jdk_aarch64_linux_hotspot_17.0.15_6.tar.gz` | adoptium-temurin-official |
| `jdk17-17.0.15_6-ubuntu22.04-amd64` | jdk17 | 17.0.15_6 | ubuntu 22.04 | amd64 | 1 | 183.1 | `OpenJDK17U-jdk_x64_linux_hotspot_17.0.15_6.tar.gz` | adoptium-temurin-official |
| `jdk17-17.0.15_6-ubuntu22.04-arm64` | jdk17 | 17.0.15_6 | ubuntu 22.04 | arm64 | 8 | 182.7 | `OpenJDK17U-jdk_aarch64_linux_hotspot_17.0.15_6.tar.gz` | adoptium-temurin-official |
| `jdk17-17.0.15_6-ubuntu24.04-amd64` | jdk17 | 17.0.15_6 | ubuntu 24.04 | amd64 | 1 | 183.1 | `OpenJDK17U-jdk_x64_linux_hotspot_17.0.15_6.tar.gz` | adoptium-temurin-official |
| `jdk17-17.0.15_6-ubuntu24.04-arm64` | jdk17 | 17.0.15_6 | ubuntu 24.04 | arm64 | 8 | 182.7 | `OpenJDK17U-jdk_aarch64_linux_hotspot_17.0.15_6.tar.gz` | adoptium-temurin-official |
| `jdk17-17.0.15_6-windows10-amd64` | jdk17 | 17.0.15_6 | windows 10 | amd64 | 1 | 181.7 | `OpenJDK17U-jdk_x64_windows_hotspot_17.0.15_6.zip` | adoptium-temurin-official |
| `jdk8-8u452b09-centos7-amd64` | jdk8 | 8u452b09 | centos 7 | amd64 | 1 | 98.3 | `OpenJDK8U-jdk_x64_linux_hotspot_8u452b09.tar.gz` | adoptium-temurin-official |
| `jdk8-8u452b09-centos7-arm64` | jdk8 | 8u452b09 | centos 7 | arm64 | 1 | 97.5 | `OpenJDK8U-jdk_aarch64_linux_hotspot_8u452b09.tar.gz` | adoptium-temurin-official |
| `jdk8-8u452b09-ubuntu22.04-amd64` | jdk8 | 8u452b09 | ubuntu 22.04 | amd64 | 1 | 98.3 | `OpenJDK8U-jdk_x64_linux_hotspot_8u452b09.tar.gz` | adoptium-temurin-official |
| `jdk8-8u452b09-ubuntu22.04-arm64` | jdk8 | 8u452b09 | ubuntu 22.04 | arm64 | 8 | 98.3 | `OpenJDK8U-jdk_aarch64_linux_hotspot_8u452b09.tar.gz` | adoptium-temurin-official |
| `jdk8-8u452b09-ubuntu24.04-amd64` | jdk8 | 8u452b09 | ubuntu 24.04 | amd64 | 1 | 98.3 | `OpenJDK8U-jdk_x64_linux_hotspot_8u452b09.tar.gz` | adoptium-temurin-official |
| `jdk8-8u452b09-ubuntu24.04-arm64` | jdk8 | 8u452b09 | ubuntu 24.04 | arm64 | 8 | 98.3 | `OpenJDK8U-jdk_aarch64_linux_hotspot_8u452b09.tar.gz` | adoptium-temurin-official |
| `jdk8-8u452b09-windows10-amd64` | jdk8 | 8u452b09 | windows 10 | amd64 | 1 | 102.0 | `OpenJDK8U-jdk_x64_windows_hotspot_8u452b09.zip` | adoptium-temurin-official |
| `mariadb-11.4.7-centos7-amd64` | mariadb | 11.4.7 | centos 7 | amd64 | 128 | 197.3 | `-` | mariadb-official-and-distro-deps |
| `mariadb-11.4.7-ubuntu22.04-amd64` | mariadb | 11.4.7 | ubuntu 22.04 | amd64 | 37 | 141.8 | `-` | mariadb-official-and-distro-deps |
| `mariadb-11.4.7-ubuntu22.04-arm64` | mariadb | 11.4.7 | ubuntu 22.04 | arm64 | 86 | 45.4 | `-` | mariadb-official-and-distro-deps |
| `mariadb-11.4.7-ubuntu24.04-amd64` | mariadb | 11.4.7 | ubuntu 24.04 | amd64 | 43 | 142.4 | `-` | mariadb-official-and-distro-deps |
| `mariadb-11.4.7-ubuntu24.04-arm64` | mariadb | 11.4.7 | ubuntu 24.04 | arm64 | 85 | 45.9 | `-` | mariadb-official-and-distro-deps |
| `mosquitto-1.6.10-centos7-amd64` | mosquitto | 1.6.10 | centos 7 | amd64 | 3 | 0.5 | `-` | eclipse-mosquitto-official-and-distro-deps |
| `mosquitto-2.0.21-windows10-amd64` | mosquitto | 2.0.21 | windows 10 | amd64 | 1 | 3.0 | `mosquitto-2.0.21-windows-x64.zip` | eclipse-mosquitto-official-and-distro-deps |
| `mosquitto-2.0.22-ubuntu22.04-amd64` | mosquitto | 2.0.22 | ubuntu 22.04 | amd64 | 5 | 0.6 | `-` | eclipse-mosquitto-official-and-distro-deps |
| `mosquitto-2.0.22-ubuntu22.04-arm64` | mosquitto | 2.0.22 | ubuntu 22.04 | arm64 | 55 | 9.8 | `-` | eclipse-mosquitto-official-and-distro-deps |
| `mosquitto-2.1.2-ubuntu24.04-amd64` | mosquitto | 2.1.2 | ubuntu 24.04 | amd64 | 5 | 0.9 | `-` | eclipse-mosquitto-official-and-distro-deps |
| `mosquitto-2.1.2-ubuntu24.04-arm64` | mosquitto | 2.1.2 | ubuntu 24.04 | arm64 | 46 | 10.3 | `-` | eclipse-mosquitto-official-and-distro-deps |
| `mysql-5.7.44-centos7-amd64` | mysql | 5.7.44 | centos 7 | amd64 | 47 | 235.6 | `-` | mysql-official-and-distro-deps |
| `mysql-8.0.35-centos7-amd64` | mysql | 8.0.35 | centos 7 | amd64 | 49 | 104.7 | `-` | mysql-official-and-distro-deps |
| `mysql-8.0.35-ubuntu22.04-amd64` | mysql | 8.0.35 | ubuntu 22.04 | amd64 | 35 | 45.3 | `-` | mysql-official-and-distro-deps |
| `mysql-8.0.35-ubuntu24.04-amd64` | mysql | 8.0.35 | ubuntu 24.04 | amd64 | 37 | 45.9 | `-` | mysql-official-and-distro-deps |
| `nginx-1.24.0-centos7-amd64` | nginx | 1.24.0 | centos 7 | amd64 | 7 | 12.4 | `-` | nginx-official-and-distro-deps |
| `nginx-1.24.0-ubuntu22.04-amd64` | nginx | 1.24.0 | ubuntu 22.04 | amd64 | 8 | 4.0 | `-` | nginx-official-and-distro-deps |
| `nginx-1.24.0-ubuntu22.04-arm64` | nginx | 1.24.0 | ubuntu 22.04 | arm64 | 34 | 9.1 | `-` | nginx-official-and-distro-deps |
| `nginx-1.24.0-ubuntu24.04-amd64` | nginx | 1.24.0 | ubuntu 24.04 | amd64 | 8 | 6.9 | `-` | nginx-official-and-distro-deps |
| `nginx-1.24.0-ubuntu24.04-arm64` | nginx | 1.24.0 | ubuntu 24.04 | arm64 | 21 | 7.9 | `-` | nginx-official-and-distro-deps |
| `nginx-1.24.0-windows10-amd64` | nginx | 1.24.0 | windows 10 | amd64 | 1 | 1.7 | `nginx-1.24.0.zip` | nginx-official-and-distro-deps |
| `nginx-1.25.3-centos7-amd64` | nginx | 1.25.3 | centos 7 | amd64 | 7 | 12.4 | `-` | nginx-official-and-distro-deps |
| `nginx-1.25.3-ubuntu22.04-amd64` | nginx | 1.25.3 | ubuntu 22.04 | amd64 | 8 | 4.1 | `-` | nginx-official-and-distro-deps |
| `nginx-1.25.3-ubuntu22.04-arm64` | nginx | 1.25.3 | ubuntu 22.04 | arm64 | 34 | 9.3 | `-` | nginx-official-and-distro-deps |
| `nginx-1.25.3-windows10-amd64` | nginx | 1.25.3 | windows 10 | amd64 | 1 | 2.0 | `nginx-1.25.3.zip` | nginx-official-and-distro-deps |
| `rabbitmq-3.11.27-ubuntu22.04-amd64` | rabbitmq | 3.11.27 | ubuntu 22.04 | amd64 | 19 | 34.1 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.11.27-ubuntu22.04-arm64` | rabbitmq | 3.11.27 | ubuntu 22.04 | arm64 | 70 | 43.9 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.11.27-ubuntu24.04-amd64` | rabbitmq | 3.11.27 | ubuntu 24.04 | amd64 | 19 | 34.1 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.11.27-ubuntu24.04-arm64` | rabbitmq | 3.11.27 | ubuntu 24.04 | arm64 | 76 | 49.6 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.11.27-windows10-amd64` | rabbitmq | 3.11.27 | windows 10 | amd64 | 2 | 160.5 | `rabbitmq-server-3.11.27.exe` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.12.10-ubuntu22.04-amd64` | rabbitmq | 3.12.10 | ubuntu 22.04 | amd64 | 19 | 34.2 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.12.10-ubuntu22.04-arm64` | rabbitmq | 3.12.10 | ubuntu 22.04 | arm64 | 70 | 44.0 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.12.10-ubuntu24.04-amd64` | rabbitmq | 3.12.10 | ubuntu 24.04 | amd64 | 19 | 34.2 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.12.10-ubuntu24.04-arm64` | rabbitmq | 3.12.10 | ubuntu 24.04 | arm64 | 76 | 49.7 | `-` | rabbitmq-official-and-erlang-deps |
| `rabbitmq-3.12.10-windows10-amd64` | rabbitmq | 3.12.10 | windows 10 | amd64 | 2 | 160.5 | `rabbitmq-server-3.12.10.exe` | rabbitmq-official-and-erlang-deps |
| `redis-6.2.13-centos7-amd64` | redis | 6.2.13 | centos 7 | amd64 | 1 | 1.2 | `-` | redis-official-and-distro-deps |
| `redis-6.2.13-ubuntu22.04-amd64` | redis | 6.2.13 | ubuntu 22.04 | amd64 | 5 | 1.6 | `-` | redis-official-and-distro-deps |
| `redis-6.2.13-ubuntu22.04-arm64` | redis | 6.2.13 | ubuntu 22.04 | arm64 | 43 | 8.3 | `-` | redis-official-and-distro-deps |
| `redis-7.2.3-ubuntu22.04-amd64` | redis | 7.2.3 | ubuntu 22.04 | amd64 | 5 | 1.9 | `-` | redis-official-and-distro-deps |
| `redis-7.2.3-ubuntu22.04-arm64` | redis | 7.2.3 | ubuntu 22.04 | arm64 | 43 | 8.6 | `-` | redis-official-and-distro-deps |
| `traefik-3.6.13-centos7-amd64` | traefik | 3.6.13 | centos 7 | amd64 | 1 | 45.9 | `traefik_v3.6.13_linux_amd64.tar.gz` | traefik-official |
| `traefik-3.6.13-centos7-arm64` | traefik | 3.6.13 | centos 7 | arm64 | 1 | 41.1 | `traefik_v3.6.13_linux_arm64.tar.gz` | traefik-official |
| `traefik-3.6.13-ubuntu22.04-amd64` | traefik | 3.6.13 | ubuntu 22.04 | amd64 | 1 | 45.9 | `traefik_v3.6.13_linux_amd64.tar.gz` | traefik-official |
| `traefik-3.6.13-ubuntu22.04-arm64` | traefik | 3.6.13 | ubuntu 22.04 | arm64 | 4 | 41.4 | `traefik_v3.6.13_linux_arm64.tar.gz` | traefik-official |
| `traefik-3.6.13-ubuntu24.04-amd64` | traefik | 3.6.13 | ubuntu 24.04 | amd64 | 1 | 45.9 | `traefik_v3.6.13_linux_amd64.tar.gz` | traefik-official |
| `traefik-3.6.13-ubuntu24.04-arm64` | traefik | 3.6.13 | ubuntu 24.04 | arm64 | 1 | 41.1 | `traefik_v3.6.13_linux_arm64.tar.gz` | traefik-official |

## 版本差异、依赖说明与未覆盖项（实事求是记录）

1. **Windows 10/11 覆盖**：已覆盖 `catalog.json` 全部支持 Windows 的应用与版本（JDK 8/11/17、Elasticsearch 8.5.3、Nginx 1.24.0/1.25.3、RabbitMQ 3.11.27/3.12.10 含 Erlang OTP 26.2.5.13、Mosquitto 2.0.21），共 10 个 `windows10-amd64` 包。
2. **Ubuntu ARM64 离线索引**：所有 Ubuntu 22.04/24.04 ARM64 系统包（Nginx、RabbitMQ、Redis、MariaDB、Mosquitto）均包含完整 `.deb` 依赖闭包与同步更新后的 `Packages` 索引文件，已通过 `assets/apt.go` 校验。
3. **上游已下架/归档版本说明（非精确主包版本的目录）**：
   - `mysql-8.0.35-ubuntu22.04-amd64`、`mysql-8.0.35-ubuntu24.04-amd64`：MySQL 官方 APT 池已移除 8.0.35 `.deb`，当前目录内封装的是相邻旧版目录中的 `mysql-server 8.0.45` 及完整 `deps/` 依赖闭包。
   - `redis-6.2.13-centos7-amd64`：官方/EPEL CentOS 7 源已归档，当前目录内封装的是相邻旧版目录中的 `redis-6.2.14` RPM。
   - `nginx-1.25.3-ubuntu24.04-*`：Nginx 官方 mainline 源无 1.25.3 的 Noble (24.04) 构建，因此 Ubuntu 24.04 仅提供 `1.24.0`。
   - `redis-7.2.3-centos7-amd64`、`redis-*-ubuntu24.04-*`：无现成 CentOS 7 7.2.3 RPM 与 Ubuntu 24.04 官方 Redis 6.2/7.2 离线依赖闭包（需在目标发行版容器/主机中构建）。
   - `mysql-*-ubuntu*-arm64`、`mysql-5.7.44-ubuntu*`：MySQL 官方未提供 Ubuntu ARM64 8.0.35 / Ubuntu 22.04+ 5.7.44 离线包。
   - `rabbitmq-*-centos7-amd64`：CentOS 7 系统包源已 EOL，缺少 Erlang 25/26 + RabbitMQ 3.11/3.12 的 CentOS 7 离线 RPM 依赖闭包。
   - **CentOS 7 ARM64 系统包**：`backend/internal/assets/manifest.go` 与 `scripts/import-legacy-resources.mjs` 已按规则明确禁止 MySQL/MariaDB/Redis/Nginx/RabbitMQ/Mosquitto 的 CentOS 7 ARM64 系统包；归档型应用（JDK 8/11/17、Consul、Traefik、Elasticsearch）均已提供 CentOS 7 ARM64 包。
4. **如何使用**：在 RelayDock 桌面端的资源管理中，选择 `C:\projects\远程安装应用升级版\offline-resources\<目标包目录>` 进行导入（主进程会调用 `resource.inspect` / `resource.import` 校验并写入本地缓存），随后即可在无外网环境下执行远端安装。
