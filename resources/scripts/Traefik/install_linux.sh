#!/bin/bash
set -e

LOG_FILE="traefik_install.log"
exec > >(tee -a "$LOG_FILE") 2>&1

HTTP_PORT="${HTTP_PORT:-80}"
HTTPS_PORT="${HTTPS_PORT:-443}"
DASHBOARD_PORT="${DASHBOARD_PORT:-8080}"
INSTALL_DIR="${INSTALL_DIR:-/opt/traefik}"
CONFIG_DIR="${CONFIG_DIR:-/etc/traefik}"
ENABLE_DASHBOARD="${ENABLE_DASHBOARD:-false}"
PACKAGE_PATH="${PACKAGE_PATH:-}"
SERVICE_NAME="traefik"

write_progress() {
    echo "PROGRESS:$1:$2"
}

validate_port() {
    local name=$1
    local value=$2
    if ! [[ "$value" =~ ^[0-9]+$ ]] || [ "$value" -lt 1 ] || [ "$value" -gt 65535 ]; then
        echo "错误：$name 必须是 1-65535 之间的数字端口"
        exit 1
    fi
}

validate_path() {
    local name=$1
    local value=$2
    if ! [[ "$value" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
        echo "错误：$name 必须是安全的 Linux 绝对路径"
        exit 1
    fi
}

validate_boolean() {
    local name=$1
    local value=$2
    if [ "$value" != "true" ] && [ "$value" != "false" ]; then
        echo "错误：$name 只能是 true 或 false"
        exit 1
    fi
}

is_port_listening() {
    local port=$1
    if command -v ss >/dev/null 2>&1; then
        ss -tln 2>/dev/null | grep -Eq ":[[:space:]]*${port}\>|:${port}[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
        netstat -tln 2>/dev/null | grep -Eq ":[[:space:]]*${port}\>|:${port}[[:space:]]"
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

# 根据远端主机架构选择同目录中匹配的 Traefik 离线包，避免 ARM 主机误选 amd64 包。
detect_arch() {
    local detected
    detected="$(normalize_arch "$(uname -m 2>/dev/null || true)")"
    [ -n "$detected" ] || return 1
    printf '%s\n' "$detected"
}

traefik_package_matches_arch() {
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

find_traefik_package() {
    local target_arch
    target_arch="$(detect_arch || true)"

    if [ -f "$PACKAGE_PATH" ]; then
        if [ -n "$target_arch" ] && ! traefik_package_matches_arch "$PACKAGE_PATH" "$target_arch"; then
            return 1
        fi

        printf '%s' "$PACKAGE_PATH"
        return 0
    fi

    if [ -d "$PACKAGE_PATH" ]; then
        if [ -n "$target_arch" ]; then
            find "$PACKAGE_PATH" -maxdepth 1 -type f -name 'traefik_v*_linux_*.tar.gz' | sort | while IFS= read -r package_file; do
                if traefik_package_matches_arch "$package_file" "$target_arch"; then
                    printf '%s\n' "$package_file"
                fi
            done | tail -n 1
            return 0
        fi

        find "$PACKAGE_PATH" -maxdepth 1 -type f -name 'traefik_v*_linux_*.tar.gz' | sort | tail -n 1
        return 0
    fi

    return 1
}

write_progress "Initializing" 5
echo "Traefik 安装脚本开始..."
echo "PACKAGE_PATH=${PACKAGE_PATH:-<未指定>}"

if [ "$EUID" -ne 0 ]; then
    echo "错误：请使用 root 权限运行此脚本"
    exit 1
fi

validate_port "HTTP_PORT" "$HTTP_PORT"
validate_port "HTTPS_PORT" "$HTTPS_PORT"
validate_port "DASHBOARD_PORT" "$DASHBOARD_PORT"
validate_path "INSTALL_DIR" "$INSTALL_DIR"
validate_path "CONFIG_DIR" "$CONFIG_DIR"
validate_boolean "ENABLE_DASHBOARD" "$ENABLE_DASHBOARD"

PACKAGE_FILE="$(find_traefik_package || true)"
if [ -z "$PACKAGE_FILE" ] || [ ! -f "$PACKAGE_FILE" ]; then
    echo "错误：未找到匹配当前 CPU 架构的 Traefik 离线安装包，请确认 PACKAGE_PATH 中包含 traefik_v*_linux_amd64.tar.gz 或 traefik_v*_linux_arm64.tar.gz"
    exit 1
fi

echo "使用离线安装包：$PACKAGE_FILE"

write_progress "Preparing" 15
if ! command -v tar >/dev/null 2>&1; then
    tool_dir="$(dirname "$PACKAGE_FILE")"
    if [ -f /etc/debian_version ] && compgen -G "$tool_dir/tar_"*.deb >/dev/null; then
        echo "系统缺少 tar，使用离线依赖包安装..."
        dpkg -i "$tool_dir"/tar_*.deb "$tool_dir"/gzip_*.deb "$tool_dir"/libacl1_*.deb "$tool_dir"/libpcre2-8-0_*.deb "$tool_dir"/libselinux1_*.deb || {
            echo "错误：离线安装 tar 失败，请确认 $tool_dir 中的 deb 依赖完整。"
            exit 1
        }
    fi
fi
if ! command -v tar >/dev/null 2>&1; then
    echo "错误：系统未安装 tar，且离线目录没有可用的 tar deb。离线安装不会联网安装依赖。"
    exit 1
fi
mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
TMP_DIR="/tmp/traefik_extract_$(date +%s)"
mkdir -p "$TMP_DIR"
tar -xzf "$PACKAGE_FILE" -C "$TMP_DIR"
install -m 0755 "$TMP_DIR/traefik" /usr/local/bin/traefik
rm -rf "$TMP_DIR"

if ! id traefik >/dev/null 2>&1; then
    useradd --system --home "$INSTALL_DIR" --shell /sbin/nologin traefik 2>/dev/null || useradd --system --home "$INSTALL_DIR" --shell /usr/sbin/nologin traefik 2>/dev/null || true
fi

mkdir -p "$INSTALL_DIR/data"
chown -R traefik:traefik "$INSTALL_DIR"
chown root:root "$CONFIG_DIR"
chmod 755 "$CONFIG_DIR"

write_progress "Configuring" 45
cat > "$CONFIG_DIR/traefik.yml" <<EOF
entryPoints:
  web:
    address: ":${HTTP_PORT}"
  websecure:
    address: ":${HTTPS_PORT}"
  traefik:
    address: ":${DASHBOARD_PORT}"
providers:
  file:
    filename: "$CONFIG_DIR/dynamic.yml"
    watch: true
log:
  level: INFO
EOF

if [ "$ENABLE_DASHBOARD" = "true" ]; then
cat >> "$CONFIG_DIR/traefik.yml" <<EOF
api:
  dashboard: true
  insecure: true
EOF
fi

cat > "$CONFIG_DIR/dynamic.yml" <<'EOF'
# reserved for dynamic configuration
http:
  routers: {}
  services: {}
EOF

cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=Traefik Proxy
After=network-online.target
Wants=network-online.target

[Service]
User=traefik
Group=traefik
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/usr/local/bin/traefik --configfile=$CONFIG_DIR/traefik.yml
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

if command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port=${HTTP_PORT}/tcp 2>/dev/null || true
    firewall-cmd --permanent --add-port=${HTTPS_PORT}/tcp 2>/dev/null || true
    if [ "$ENABLE_DASHBOARD" = "true" ]; then
        firewall-cmd --permanent --add-port=${DASHBOARD_PORT}/tcp 2>/dev/null || true
    fi
    firewall-cmd --reload 2>/dev/null || true
fi

if command -v ufw >/dev/null 2>&1; then
    ufw allow ${HTTP_PORT}/tcp 2>/dev/null || true
    ufw allow ${HTTPS_PORT}/tcp 2>/dev/null || true
    if [ "$ENABLE_DASHBOARD" = "true" ]; then
        ufw allow ${DASHBOARD_PORT}/tcp 2>/dev/null || true
    fi
fi

if command -v iptables >/dev/null 2>&1; then
    iptables -C INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT 2>/dev/null || true
    iptables -C INPUT -p tcp --dport ${HTTPS_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${HTTPS_PORT} -j ACCEPT 2>/dev/null || true
    if [ "$ENABLE_DASHBOARD" = "true" ]; then
        iptables -C INPUT -p tcp --dport ${DASHBOARD_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${DASHBOARD_PORT} -j ACCEPT 2>/dev/null || true
    fi
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
    echo "等待 Traefik 服务就绪 ($i/15)..."
done

VERSION="$(traefik version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n 1 || echo '未知')"

write_progress "Complete" 100
echo ""
echo "--- MACHINE READABLE ---"
echo "INSTALLED: true"
echo "VERSION: ${VERSION:-未知}"
echo "RUNNING: ${RUNNING}"
echo "PORT: ${HTTP_PORT},${HTTPS_PORT},${DASHBOARD_PORT}"
echo "------------------------"

if [ "$RUNNING" != "true" ]; then
    echo "错误：Traefik 服务启动失败"
    journalctl -u ${SERVICE_NAME} -n 50 --no-pager 2>/dev/null || true
    exit 1
fi

echo "Traefik 安装完成"
