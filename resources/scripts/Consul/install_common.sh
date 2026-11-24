#!/bin/bash
set -e

LOG_FILE="consul_install.log"
exec > >(tee -a "$LOG_FILE") 2>&1

HTTP_PORT="${HTTP_PORT:-8500}"
DNS_PORT="${DNS_PORT:-8600}"
BIND_ADDR="${BIND_ADDR:-0.0.0.0}"
DATA_DIR="${DATA_DIR:-/var/lib/consul}"
NODE_NAME="${NODE_NAME:-consul-node}"
UI_ENABLED="${UI_ENABLED:-true}"
PACKAGE_PATH="${PACKAGE_PATH:-}"
INSTALL_DIR="/opt/consul"
CONFIG_DIR="/etc/consul.d"
SERVICE_NAME="consul"
PRIMARY_ADDR="$(hostname -I 2>/dev/null | awk '{print $1}')"
if [ -z "$PRIMARY_ADDR" ]; then
    PRIMARY_ADDR="127.0.0.1"
fi

AGENT_BIND_ADDR="$BIND_ADDR"
if [ "$AGENT_BIND_ADDR" = "0.0.0.0" ]; then
    AGENT_BIND_ADDR="$PRIMARY_ADDR"
fi

write_progress() {
    echo "PROGRESS:$1:$2"
}

is_port_listening() {
    local port=$1
    if command -v ss >/dev/null 2>&1; then
        ss -tuln 2>/dev/null | grep -Eq ":[[:space:]]*${port}\>|:${port}[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
        netstat -tuln 2>/dev/null | grep -Eq ":[[:space:]]*${port}\>|:${port}[[:space:]]"
    else
        return 1
    fi
}

normalize_arch() {
    local arch="${1:-}"
    arch="$(printf '%s' "$arch" | tr '[:upper:]' '[:lower:]')"

    case "$arch" in
        x86_64|x64|amd64)
            printf '%s\n' "amd64"
            ;;
        aarch64|arm64)
            printf '%s\n' "arm64"
            ;;
        *)
            printf '%s\n' ""
            ;;
    esac
}

# 根据远端主机架构选择同目录中匹配的 Consul 离线包，避免 ARM 主机误选 amd64 包。
detect_arch() {
    local detected
    detected="$(normalize_arch "$(uname -m 2>/dev/null || true)")"
    [ -n "$detected" ] || return 1
    printf '%s\n' "$detected"
}

consul_package_matches_arch() {
    local package_file="$1"
    local target_arch="$2"
    local file_name
    file_name="$(basename "$package_file" | tr '[:upper:]' '[:lower:]')"

    case "$target_arch" in
        amd64)
            [[ "$file_name" == *"linux_amd64"* || "$file_name" == *"linux_x86_64"* || "$file_name" == *"linux_x64"* ]]
            ;;
        arm64)
            [[ "$file_name" == *"linux_arm64"* || "$file_name" == *"linux_aarch64"* ]]
            ;;
        *)
            return 1
            ;;
    esac
}

find_consul_package() {
    local target_arch
    target_arch="$(detect_arch || true)"

    if [ -f "$PACKAGE_PATH" ]; then
        if [ -n "$target_arch" ] && ! consul_package_matches_arch "$PACKAGE_PATH" "$target_arch"; then
            return 1
        fi

        printf '%s' "$PACKAGE_PATH"
        return 0
    fi

    if [ -d "$PACKAGE_PATH" ]; then
        if [ -n "$target_arch" ]; then
            find "$PACKAGE_PATH" -maxdepth 1 -type f -name 'consul_*_linux_*.zip' | sort | while IFS= read -r package_file; do
                if consul_package_matches_arch "$package_file" "$target_arch"; then
                    printf '%s\n' "$package_file"
                fi
            done | tail -n 1
            return 0
        fi

        find "$PACKAGE_PATH" -maxdepth 1 -type f -name 'consul_*_linux_*.zip' | sort | tail -n 1
        return 0
    fi

    return 1
}

write_progress "Initializing" 5
echo "Consul 安装脚本开始..."
echo "PACKAGE_PATH=${PACKAGE_PATH:-<未指定>}"

if [ "$EUID" -ne 0 ]; then
    echo "错误：请使用 root 权限运行此脚本"
    exit 1
fi

PACKAGE_FILE="$(find_consul_package || true)"
if [ -z "$PACKAGE_FILE" ] || [ ! -f "$PACKAGE_FILE" ]; then
    echo "错误：未找到匹配当前 CPU 架构的 Consul 离线安装包，请确认 PACKAGE_PATH 中包含 consul_*_linux_amd64.zip 或 consul_*_linux_arm64.zip"
    exit 1
fi

echo "使用离线安装包：$PACKAGE_FILE"

write_progress "Preparing" 15
if [ -e "$PACKAGE_PATH" ]; then
    # 本地 ZIP 已就绪时只允许使用随包携带的 deb，禁止 apt/yum 联网补装 unzip。
    if ! command -v unzip >/dev/null 2>&1; then
        tool_dir="$PACKAGE_PATH"
        if [ -f "$PACKAGE_PATH" ]; then
            tool_dir="$(dirname "$PACKAGE_PATH")"
        fi
        if [ -f /etc/debian_version ] && compgen -G "$tool_dir/unzip_"*.deb >/dev/null; then
            echo "系统缺少 unzip，使用离线依赖包安装..."
            dpkg -i "$tool_dir"/unzip_*.deb "$tool_dir"/libbz2-*.deb || {
                echo "错误：离线安装 unzip 失败，请确认 $tool_dir 中的 deb 依赖完整。"
                exit 1
            }
        fi
    fi
    if ! command -v unzip >/dev/null 2>&1; then
        echo "错误：检测到本地 Consul 离线安装包，但系统未安装 unzip，且离线目录没有可用的 unzip deb。离线安装不会联网安装依赖。"
        exit 1
    fi
elif ! command -v unzip >/dev/null 2>&1; then
    echo "错误：系统未安装 unzip，且未提供本地 Consul 离线安装包。离线安装不会联网安装依赖。"
    exit 1
fi

mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR"
TMP_DIR="/tmp/consul_extract_$(date +%s)"
mkdir -p "$TMP_DIR"
unzip -oq "$PACKAGE_FILE" -d "$TMP_DIR"
install -m 0755 "$TMP_DIR/consul" /usr/local/bin/consul
rm -rf "$TMP_DIR"

if ! id consul >/dev/null 2>&1; then
    useradd --system --home "$DATA_DIR" --shell /sbin/nologin consul 2>/dev/null || useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin consul 2>/dev/null || true
fi

chown -R consul:consul "$DATA_DIR" "$CONFIG_DIR"

write_progress "Configuring" 45
cat > "$CONFIG_DIR/consul.hcl" <<EOF
server = true
datacenter = "dc1"
node_name = "$NODE_NAME"
data_dir = "$DATA_DIR"
bind_addr = "$AGENT_BIND_ADDR"
advertise_addr = "$AGENT_BIND_ADDR"
client_addr = "$BIND_ADDR"
ports {
  http = $HTTP_PORT
  dns = $DNS_PORT
}
ui_config {
  enabled = ${UI_ENABLED}
}
EOF

cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=Consul Agent
After=network-online.target
Wants=network-online.target

[Service]
User=consul
Group=consul
ExecStart=/usr/local/bin/consul agent -config-dir=$CONFIG_DIR
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

if command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port=${HTTP_PORT}/tcp 2>/dev/null || true
    firewall-cmd --permanent --add-port=${DNS_PORT}/tcp 2>/dev/null || true
    firewall-cmd --reload 2>/dev/null || true
fi

if command -v ufw >/dev/null 2>&1; then
    ufw allow ${HTTP_PORT}/tcp 2>/dev/null || true
    ufw allow ${DNS_PORT}/tcp 2>/dev/null || true
fi

if command -v iptables >/dev/null 2>&1; then
    iptables -C INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT 2>/dev/null || true
    iptables -C INPUT -p tcp --dport ${DNS_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${DNS_PORT} -j ACCEPT 2>/dev/null || true
    iptables -C INPUT -p udp --dport ${DNS_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p udp --dport ${DNS_PORT} -j ACCEPT 2>/dev/null || true
fi

write_progress "Starting" 75
systemctl daemon-reload
systemctl enable ${SERVICE_NAME}
systemctl restart ${SERVICE_NAME}

RUNNING=false
for i in $(seq 1 15); do
    sleep 2
    if systemctl is-active --quiet ${SERVICE_NAME} && is_port_listening "$HTTP_PORT"; then
        RUNNING=true
        break
    fi
    echo "等待 Consul 服务就绪 ($i/15)..."
done

VERSION="$(consul version 2>/dev/null | head -n 1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n 1 || echo '未知')"

write_progress "Complete" 100
echo ""
echo "--- MACHINE READABLE ---"
echo "INSTALLED: true"
echo "VERSION: ${VERSION:-未知}"
echo "RUNNING: ${RUNNING}"
echo "PORT: ${HTTP_PORT},${DNS_PORT}"
echo "------------------------"

if [ "$RUNNING" != "true" ]; then
    echo "错误：Consul 服务启动失败"
    journalctl -u ${SERVICE_NAME} -n 50 --no-pager 2>/dev/null || true
    exit 1
fi

echo "Consul 安装完成"
