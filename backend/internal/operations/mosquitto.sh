#!/usr/bin/env bash
# The Go operation supplies this body. Resources provide package files only.
set -euo pipefail
umask 077
mode=$1
arch=$2
version=$3
work=${4:-}
port=${5:-}
username=${6:-}
unit=mosquitto.service
etc=/etc/mosquitto
conf=$etc/conf.d/remote-installer.conf
passwd=$etc/remote-installer.passwd
data=/var/lib/mosquitto
state_dir=/var/lib/remote-installer
marker=$state_dir/mosquitto.managed

fail() { printf '%s\n' "$1" >&2; exit 1; }
progress() { printf 'PROGRESS:%s:%s\n' "$1" "$2"; }
status() { printf '%s:%s\n' "$1" "$2"; }

platform() {
    [[ -r /etc/os-release ]] || fail '无法读取远端系统版本'
    grep -Eq '^ID=(ubuntu|"ubuntu")$' /etc/os-release &&
        grep -Eq '^VERSION_ID=(24\.04|"24\.04")$' /etc/os-release &&
        [[ $(dpkg --print-architecture) == "$arch" ]] || fail '远端系统版本或 CPU 架构已变更'
}

write_preflight() {
    platform
    [[ $(id -u) == 0 ]] || fail '该操作需要以已经授权的远端管理员身份连接'
    local audit
    audit=$(dpkg --audit) || fail '无法审查远端软件包状态'
    [[ -z $audit ]] || fail '存在待修复的系统包，先修复后再操作 Mosquitto'
    systemctl list-units --type=service --no-pager --no-legend >/dev/null || fail '远端 systemd 无法响应'
    if [[ -e $state_dir || -L $state_dir ]]; then
        [[ -d $state_dir && ! -L $state_dir && $(stat -c '%u:%a' -- "$state_dir") == 0:700 ]] ||
            fail '操作记录目录的归属或权限异常'
    fi
}

pkg_info() { dpkg-query -W -f='${db:Status-Status}\t${Version}' -- "$1" 2>/dev/null; }
pkg_installed() { [[ $(pkg_info "$1" || true) == installed$'\t'* ]]; }
active_units() { systemctl list-units --type=service --state=active --no-pager --no-legend --plain "$@"; }

process_active() {
    local code=0
    pgrep -x mosquitto >/dev/null || code=$?
    # pgrep exit 1 represents the absence of a process.
    [[ $code == 0 || $code == 1 ]] || fail '无法查询 Mosquitto 进程'
    return "$code"
}

check_install() {
    write_preflight
    local load
    load=$(systemctl show --property=LoadState --value "$unit") || [[ $load == not-found ]] || fail '无法读取 Mosquitto 服务状态'
    if pkg_installed mosquitto || [[ $load != not-found ]] || process_active || command -v mosquitto >/dev/null ||
        [[ -e /usr/sbin/mosquitto || -e $etc || -L $etc || -e $data || -L $data || -e $marker || -L $marker ]]; then
        fail '发现现有 Mosquitto、配置或数据；仅允许在无现存安装和残留的目标上首次安装'
    fi
}

version_matches() {
    local actual=${1#*:}
    [[ $actual == "$2" || $actual == "$2"[-+~]* ]]
}

package_signature() {
    local fragment dropins file sum path
    fragment=$(systemctl show --property=FragmentPath --value "$unit") || return 1
    dropins=$(systemctl show --property=DropInPaths --value "$unit") || return 1
    [[ $fragment == /lib/systemd/system/mosquitto.service || $fragment == /usr/lib/systemd/system/mosquitto.service ]] || return 1
    [[ -f $fragment && ! -L $fragment && -z $dropins ]] || return 1
    [[ -x /usr/sbin/mosquitto && ! -L /usr/sbin/mosquitto ]] || return 1
    [[ $(dpkg-query -S -- /usr/sbin/mosquitto) == 'mosquitto: /usr/sbin/mosquitto' ]] || return 1
    # These are the audited prerm/remove and postrm/remove bodies from the
    # pinned Noble packages. The postrm/purge branch is never invoked here.
    for file in prerm postrm; do
        path=/var/lib/dpkg/info/mosquitto.$file
        [[ -f $path && ! -L $path ]] || return 1
        sum=$(sha256sum -- "$path") || return 1
        case "$file:${sum%% *}" in
            prerm:ebc077d4d826afaacfdbd894d7237f0d0209c4e3f7ab705a5622483b6a2445bd|\
            postrm:18b50ce75f148b841608c6cdd88dd15a99951d7122b2fb6d82d6c4aea7c3525a) ;;
            *) return 1 ;;
        esac
    done
    # Reject a service or daemon replaced since this install, and any custom
    # persistence location introduced by editing the primary configuration.
    [[ -f $etc/mosquitto.conf && ! -L $etc/mosquitto.conf && -f $conf && ! -L $conf ]] || return 1
    file=$(find "$etc/conf.d" -maxdepth 1 -name '*.conf' ! -name 'remote-installer.conf' -print -quit) || return 1
    [[ -z $file ]] || return 1
    sha256sum -- /usr/sbin/mosquitto "$fragment" "$etc/mosquitto.conf" "$conf"
}

check_remove() {
    write_preflight
    [[ -f $marker && ! -L $marker && $(stat -c '%u:%a' -- "$marker") == 0:600 ]] ||
        fail '只允许卸载由本客户端安装且归属已登记的 Mosquitto'
    local current expected signature
    current=$(pkg_info mosquitto) || fail '软件包归属不可验证'
    [[ $current == installed$'\t'* ]] || fail '软件包不处于完整安装状态'
    expected=$(<"$marker")
    signature=$(package_signature) || fail 'Mosquitto 服务、二进制或软件包脚本归属变化'
    [[ $expected == "$current"$'\n'"$signature" ]] || fail 'Mosquitto 软件包或卸载脚本与登记记录不符'
    [[ -z $version ]] || version_matches "${current#*$'\t'}" "$version" || fail '已安装的版本与卸载请求不符'
    [[ -d $data && ! -L $data && -d $etc && ! -L $etc && -d $etc/conf.d && ! -L $etc/conf.d ]] ||
        fail '配置或持久化数据路径的类型不符'
    local property dependents
    for property in RequiredBy RequisiteOf BoundBy ConsistsOf PropagatesStopTo; do
        dependents=$(systemctl show --property="$property" --value "$unit") || fail '无法核实服务停止范围'
        [[ -z $dependents ]] || fail '检测到可能随 Mosquitto 停止的非目标服务，拒绝卸载'
    done
    [[ $(dpkg-query -W -f='${Conffiles}' mosquitto) == *"$etc/mosquitto.conf "* ]] || fail '无法核实主配置文件的保留规则'
    dpkg --no-act --remove mosquitto >/dev/null || fail '软件包模拟移除未通过，服务未停止'
    preservation_hash >/dev/null || fail '无法在卸载前核对配置和持久化数据'
}

check_work() {
    [[ $work =~ ^/tmp/remote-installer\.[a-f0-9]{32}$ && -d $work && ! -L $work &&
       $(stat -c '%u:%a' -- "$work") == 0:700 ]] || fail '任务暂存目录归属无效'
}

select_packages() {
    local file pkg pkg_arch package_version basename
    local -a candidates=("$work"/packages/*.deb)
    local -A added=()
    for file in "${candidates[@]}"; do
        [[ -f $file && ! -L $file ]] || fail '离线资源缺少可读取的 DEB 文件'
        basename=${file##*/}
        pkg=${basename%%_*}
        # Only the broker and known Noble dependencies may be selected. OS
        # packages already present on the host are never explicitly upgraded.
        case $pkg in
            mosquitto|libmosquitto1|libcjson1|libmicrohttpd12t64|libedit2|libsqlite3-0|libbsd0|libmd0) ;;
            *) fail '发现未授权的 DEB 软件包' ;;
        esac
        [[ $(dpkg-deb -f "$file" Package) == "$pkg" ]] || fail 'DEB 包名与离线清单不符'
        pkg_arch=$(dpkg-deb -f "$file" Architecture)
        [[ $pkg_arch == all || $pkg_arch == "$arch" ]] && [[ $basename == *_"$pkg_arch".deb ]] || fail 'DEB 包体的 CPU 架构与离线清单不符'
        if [[ $pkg == mosquitto ]]; then
            [[ $pkg_arch == "$arch" ]] || fail 'Mosquitto 主包必须带有目标 CPU 架构'
            package_version=$(dpkg-deb -f "$file" Version)
            version_matches "$package_version" "$version" || fail 'Mosquitto 主包的实际版本与所选版本不符'
            main_file=$file
        elif pkg_installed "$pkg"; then
            continue
        fi
        [[ ! ${added[$pkg]+duplicate} ]] || fail '离线资源包含重复的软件包'
        added[$pkg]=1
        packages+=("$file")
    done
    [[ -n $main_file ]] || fail '离线资源中缺少该版本和架构的 Mosquitto 主包'
}

install_broker() {
    local secret='' main_file='' apt_plan action name rest auth_work
    local -a packages=()
    local -A approved=()
    check_install
    check_work
    [[ $port =~ ^[0-9]{1,5}$ && $port -ge 1 && $port -le 65535 ]] || fail '监听端口无效'
    if [[ -n $username ]]; then
        [[ $username =~ ^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,127}$ ]] || fail '用户名无效'
        [[ $(stat -fc '%T' -- /dev/shm) == tmpfs ]] || fail '目标主机没有可用的内存暂存目录'
        IFS= read -r secret && [[ -n $secret ]] || fail '未收到本次认证配置'
    fi
    select_packages
    mkdir -p -- "$work/apt/lists/partial" "$work/apt/cache/archives/partial"
    # Override both the apt settings load paths and the sources/lists caches.
    # Only file arguments staged by this task can supply packages.
    export APT_CONFIG="$work/apt.conf" DEBIAN_FRONTEND=noninteractive
    cat >"$APT_CONFIG" <<EOF
Dir::Etc::main "/dev/null";
Dir::Etc::parts "-";
Dir::Etc::sourcelist "/dev/null";
Dir::Etc::sourceparts "-";
Dir::State::lists "$work/apt/lists";
Dir::Cache "$work/apt/cache";
APT::Sandbox::User "root";
Acquire::Retries "0";
EOF
    for file in "${packages[@]}"; do
        name=${file##*/}
        approved[${name%%_*}]=1
    done
    apt_plan=$(apt-get --simulate --no-download --no-remove --no-upgrade --no-install-recommends install "${packages[@]}") ||
        fail '本地包和已安装的系统包不能满足离线依赖'
    while read -r action name rest; do
        case $action in
            Remv|Purg) fail '包管理器计划移除现有软件包，已停止安装' ;;
            Inst|Conf)
                name=${name%%:*}
                [[ ${approved[$name]:-} == 1 ]] && ! pkg_installed "$name" ||
                    fail '包管理器计划变更既有或非目标软件包，已停止安装' ;;
        esac
    done <<<"$apt_plan"
    progress '安装离线软件包' 35
    apt-get -y --no-download --no-remove --no-upgrade --no-install-recommends -o Dpkg::Options::=--force-confold install "${packages[@]}"
    progress '应用 MQTT 配置' 70
    local actual
    actual=$(pkg_info mosquitto)
    [[ $actual == installed$'\t'* ]] && version_matches "${actual#*$'\t'}" "$version" || fail '实际安装的包版本不符'
    [[ -d $etc/conf.d && ! -L $etc && ! -L $etc/conf.d && ! -e $conf && ! -L $conf && ! -e $passwd && ! -L $passwd ]] ||
        fail '配置目录或目标文件与预期不符'
    grep -Eq '^[[:space:]]*include_dir[[:space:]]+/etc/mosquitto/conf\.d([[:space:]]|$)' "$etc/mosquitto.conf" || fail '主配置未加载约定的配置目录'
    if [[ -n $username ]]; then
        command -v mosquitto_passwd >/dev/null || fail '离线主包缺少认证工具'
        auth_work=$(mktemp -d /dev/shm/remote-installer-mqtt.XXXXXXXXXXXXXXXX) || fail '无法创建内存暂存目录'
        trap 'rm -rf --one-file-system -- "$auth_work"' EXIT
        trap 'exit 1' HUP INT TERM
        printf '%s:%s\n' "$username" "$secret" >"$auth_work/passwd"
        unset secret
        mosquitto_passwd -U "$auth_work/passwd" </dev/null >/dev/null 2>&1 || fail '无法生成 MQTT 密码散列'
        install -o root -g mosquitto -m 0640 -- "$auth_work/passwd" "$passwd"
        rm -rf --one-file-system -- "$auth_work"
        trap - EXIT HUP INT TERM
        printf 'listener %s 0.0.0.0\nallow_anonymous false\npassword_file %s\n' "$port" "$passwd" >"$work/broker.conf"
    else
        printf 'listener %s 127.0.0.1\nallow_anonymous true\n' "$port" >"$work/broker.conf"
    fi
    install -o root -g root -m 0644 -- "$work/broker.conf" "$conf"
    systemctl daemon-reload
    local signature
    signature=$(package_signature) || fail '已安装，但未核实包和服务归属，请查看日志'
    if [[ ! -e $state_dir ]]; then mkdir -m 0700 -- "$state_dir"; fi
    [[ ! -e $marker && ! -L $marker ]] || fail '已有同名操作记录'
    printf '%s\n%s\n' "$actual" "$signature" >"$work/managed"
    install -o root -g root -m 0600 -- "$work/managed" "$marker"
    systemctl restart "$unit"
    systemctl is-active --quiet "$unit" || fail '软件包已安装，但 Mosquitto 服务未运行'
    status INSTALLED true
    status RUNNING true
    status VERSION "$version"
    status PORT "$port"
    status STAGE SUCCESS
}

preservation_hash() (
    set -o pipefail
    local special mounted
    [[ -d $data && ! -L $data && -d $etc && ! -L $etc ]] || return 1
    mounted=$(findmnt -rn -o TARGET -l) || return 1
    while IFS= read -r m; do
        case "$m" in "$data/"*|"$etc/"*) return 1 ;; esac
    done <<<"$mounted"
    special=$(find "$data" "$etc" -xdev ! -type d ! -type f -print -quit) || return 1
    [[ -z $special ]] || return 1
    # Hash an ordered inventory, file contents and ownership/modes. Do not
    # print configuration, passwords or data files. Package-owned empty
    # directories may be removed by dpkg; user files must stay intact.
    find "$data" "$etc" -xdev -type f -printf '%p\t%U\t%G\t%m\0' | sort -z | sha256sum || return 1
    find "$data" "$etc" -xdev -type f -print0 | sort -z | xargs -0 -r sha256sum -- | sha256sum
)

other_packages() {
    dpkg-query -W -f='${Package}:${Architecture}\t${db:Status-Status}\t${Version}\n' |
        awk -F '\t' '$1 !~ /^mosquitto:/ && $2 == "installed"' | sort | sha256sum
}

other_services() {
    active_units | awk '$1 != "mosquitto.service" { print $1 }' | sort | sha256sum
}

remove_broker() {
    check_remove
    local before_data before_packages before_services rc=0 after_data after_packages after_services
    before_packages=$(other_packages) || fail '无法记录非目标软件包'
    before_services=$(other_services) || fail '无法记录非目标服务'
    progress '停止 Mosquitto 服务' 30
    systemctl stop "$unit"
    # The broker flushes its persistent messages while stopping. The data
    # snapshot used for the removal comparison must be taken after that flush.
    before_data=$(preservation_hash | sha256sum) || fail '无法核对停止后的持久化数据'
    progress '移除 Mosquitto 主包' 55
    dpkg --no-triggers --remove mosquitto || rc=$?
    after_data=$(preservation_hash | sha256sum) || fail '无法复查持久化数据与保留配置'
    after_packages=$(other_packages) || fail '无法复查非目标软件包'
    after_services=$(other_services) || fail '无法复查非目标服务'
    if [[ $before_data == "$after_data" ]]; then status DATA_PRESERVED true; else status DATA_PRESERVED false; fi
    if [[ $before_packages == "$after_packages" && $before_services == "$after_services" ]]; then
        status NON_TARGETS_UNCHANGED true
    else
        status NON_TARGETS_UNCHANGED false
    fi
    if ! pkg_installed mosquitto && [[ $before_data == "$after_data" && $before_packages == "$after_packages" &&
           $before_services == "$after_services" ]]; then
        rm -- "$marker"
    fi
    return "$rc"
}

inspect_broker() {
    platform
    [[ -r /var/lib/dpkg/status ]] || fail '无法读取远端软件包状态'
    local installed=false running=false residue=false ver='' units info configured code=0
    info=$(pkg_info mosquitto) || code=$?
    [[ $code == 0 || $code == 1 ]] || fail '无法查询 Mosquitto 软件包状态'
    if [[ $info == installed$'\t'* ]]; then
        installed=true
        ver=${info#*$'\t'}
        ver=${ver#*:}
        ver=${ver%%[-+~]*}
    elif [[ -x /usr/sbin/mosquitto ]] || command -v mosquitto >/dev/null; then
        installed=true
    fi
    units=$(active_units "$unit") || fail '无法读取 Mosquitto 服务状态'
    if [[ -n $units ]] || process_active; then installed=true; running=true; fi
    if [[ $installed == false && ( -e $etc || -e $data || -e $marker || $info == config-files$'\t'* ) ]]; then residue=true; fi
    status INSTALLED "$installed"
    status RUNNING "$running"
    status RESIDUE "$residue"
    if [[ -n $ver ]]; then status VERSION "$ver"; fi
    if [[ -f $conf && ! -L $conf && -r $conf ]]; then
        configured=$(awk '$1 == "listener" { print $2; exit }' "$conf")
        if [[ $configured =~ ^[0-9]{1,5}$ ]]; then status PORT "$configured"; fi
    fi
    return 0
}

case $mode in
    detect) inspect_broker ;;
    check-install) check_install ;;
    check-uninstall) check_remove ;;
    install|uninstall)
        write_preflight
        exec 9>>/run/remote-installer-mosquitto.lock
        flock -n 9 || fail '同一主机上已有 Mosquitto 写操作'
        if [[ $mode == install ]]; then install_broker; else remove_broker; fi ;;
    *) fail '无效的内置操作' ;;
esac
