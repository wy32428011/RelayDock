package operations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

const maxID = 160

var (
	idPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]*$`)
	envPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	mqttUser     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,127}$`)
	ansiSequence = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
	secretField  = regexp.MustCompile(`(?i)(["']?(?:[a-z0-9_-]*(?:password|passphrase|secret|token|api[_-]?key|private[_-]?key))["']?\s*[:=]\s*).*$`)
)

func validID(v string) bool { return len(v) <= maxID && idPattern.MatchString(v) }

func validateShape(op operation) error {
	if !validID(op.HostID) || !validID(op.AppID) || !validID(op.RequestKey) || !validKind(op.Kind) ||
		op.Version != "" && !validID(op.Version) || op.ResourceID != "" && !validID(op.ResourceID) {
		return rpc.Error("BAD_PARAMS", "主机、应用、操作类型、版本或请求标识无效")
	}
	if op.SourceMode != "" && op.SourceMode != "offline" && op.SourceMode != "online" {
		return rpc.Error("BAD_PARAMS", "安装来源须为离线或在线")
	}
	if op.Kind != "install" && (op.SourceMode != "" || op.ResourceID != "") {
		return rpc.Error("BAD_PARAMS", "安装来源仅适用于安装任务")
	}
	if len(op.Parameters) > 48 {
		return rpc.Error("BAD_PARAMS", "应用参数过多")
	}
	return nil
}

func (s *Service) prepare(ctx context.Context, op operation) (plan, error) {
	if err := ctx.Err(); err != nil {
		return plan{}, err
	}
	app, ok := s.assets.App(op.AppID)
	if !ok {
		return plan{}, rpc.Error("APP_NOT_FOUND", "应用目录中没有找到该应用")
	}
	h, err := s.hosts.Host(ctx, op.HostID)
	if err != nil {
		return plan{}, err
	}
	if err := trustedHost(h); err != nil {
		return plan{}, err
	}
	version := op.Version
	if version == "" && len(app.Versions) > 0 && op.Kind == "install" {
		version = app.Versions[0]
		if app.ID == "mosquitto" && canonicalOS(h.OS) == "ubuntu" && h.OSVersion == "24.04" {
			version = reviewedMosquittoVersion
		}
	}
	if version != "" && !slices.Contains(app.Versions, version) || version == "" && op.Kind == "install" {
		return plan{}, rpc.Error("VERSION_UNSUPPORTED", "所选版本不在应用目录中")
	}
	params, redact, err := validateParameters(app, op)
	if err != nil {
		return plan{}, err
	}
	p := plan{host: h, appID: app.ID, version: version, kind: op.Kind, parameters: params, redact: redact}
	if op.Kind == "detect" {
		if err := validateDetectionPaths(p); err != nil {
			return plan{}, err
		}
		dir, entry, err := s.assets.Script(h.OS, app.ID, version, "detect")
		if err != nil {
			return plan{}, err
		}
		p.detect, err = readDetection(dir, entry, isWindows(h.OS))
		if err != nil {
			return plan{}, err
		}
		p.summary = targetSummary(h, app.Label) + " · 运行只读状态检测"
		return p, nil
	}
	if !op.Confirmed {
		return plan{}, rpc.Error("CONFIRMATION_REQUIRED", "请确认目标、来源和操作影响")
	}
	if op.Kind == "install" && op.SourceMode == "online" {
		return plan{}, rpc.Error("ONLINE_UNSUPPORTED", "当前没有开放在线安装的应用与远端组合")
	}
	if !supportsMosquittoWrite(p) || p.version != "" && p.version != reviewedMosquittoVersion {
		if op.Kind == "uninstall" {
			return plan{}, rpc.Error("UNINSTALL_UNSUPPORTED", "仅开放 Ubuntu 24.04 上由本版安装的 Mosquitto 2.1.2 保留数据卸载")
		}
		return plan{}, rpc.Error("INSTALL_UNSUPPORTED", "仅开放 Ubuntu 24.04 的 Mosquitto 2.1.2 离线安装")
	}
	if op.Kind == "uninstall" {
		if op.KeepData != nil && !*op.KeepData {
			return plan{}, rpc.Error("UNINSTALL_UNSUPPORTED", "Mosquitto 仅开放保留数据卸载")
		}
		p.summary = targetSummary(h, app.Label) + " · 移除 Mosquitto 服务包；保留持久化数据、配置及依赖包；需要远端管理员身份"
		return p, nil
	}
	if (params["USERNAME"] == "") != (params["PASSWORD"] == "") {
		return plan{}, rpc.Error("BAD_PARAMS", "Mosquitto 用户名和密码须同时填写或同时留空")
	}
	if u := params["USERNAME"]; u != "" && !mqttUser.MatchString(u) {
		return plan{}, rpc.Error("BAD_PARAMS", "Mosquitto 用户名只能包含字母、数字及 _ . @ -，且不能以连字符开头")
	}
	if pass := params["PASSWORD"]; pass != "" && (strings.TrimSpace(pass) != pass || strings.Contains(pass, ":") || len(pass) > 256) {
		return plan{}, rpc.Error("BAD_PARAMS", "Mosquitto 密码须在 256 字节以内，不得包含冒号或首尾空白")
	}
	if params["MQTT_PORT"] == "" {
		params["MQTT_PORT"] = "1883"
	}
	p.bundle, err = s.assets.Resolve(ctx, app.ID, version, h.OS, h.OSVersion, h.Arch, op.ResourceID)
	if err != nil {
		return plan{}, err
	}
	if err := checkBundle(p); err != nil {
		return plan{}, err
	}
	if _, err := s.pickPayload(p.bundle, canonicalArch(h.Arch)); err != nil {
		return plan{}, err
	}
	scope := "未设置认证，监听限于本机回环地址"
	if params["USERNAME"] != "" {
		scope = "启用密码认证，监听所有 IPv4 接口（不启用 TLS）"
	}
	trustLabel := "已核对"
	if p.bundle.Trust == "signed" {
		trustLabel = "已签名"
	}
	p.summary = fmt.Sprintf("%s %s · 离线资源 %s（%s） · 端口 %s，%s；需要远端管理员身份",
		targetSummary(h, app.Label), version, safeText(p.bundle.ID, maxID), trustLabel, params["MQTT_PORT"], scope)
	return p, nil
}

func checkBundle(p plan) error {
	b := p.bundle
	if b.AppID != p.appID || b.Version != p.version || canonicalOS(b.OS) != canonicalOS(p.host.OS) ||
		b.OSVersion != p.host.OSVersion || canonicalArch(b.Arch) != canonicalArch(p.host.Arch) || b.Dir == "" ||
		(b.Trust != "signed" && b.Trust != "reviewed") {
		return rpc.Error("RESOURCE_MISMATCH", "离线资源的应用、版本、平台、架构或信任状态不匹配")
	}
	return nil
}

func trustedHost(h state.Host) error {
	if strings.TrimSpace(h.Fingerprint) == "" {
		return rpc.Error("HOST_KEY_REQUIRED", "请重新测试连接并确认主机指纹")
	}
	if !h.CredentialReady {
		return rpc.Error("AUTH_REQUIRED", "请补录并验证主机凭据")
	}
	if v := canonicalOS(h.OS); v == "" || v == "unknown" {
		return rpc.Error("PLATFORM_UNKNOWN", "请重新测试连接以识别远端操作系统")
	}
	return nil
}

func sameTarget(a, b state.Host) bool {
	return a.ID == b.ID && a.Address == b.Address && a.Port == b.Port && a.Username == b.Username &&
		a.Auth == b.Auth && a.Fingerprint == b.Fingerprint && a.OS == b.OS && a.OSVersion == b.OSVersion && a.Arch == b.Arch
}

func canonicalOS(os string) string { return strings.ToLower(strings.TrimSpace(os)) }
func isWindows(os string) bool     { return strings.HasPrefix(canonicalOS(os), "windows") }

func canonicalArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "amd64", "x86_64", "x64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return ""
	}
}

func supportsMosquittoWrite(p plan) bool {
	return p.appID == "mosquitto" && canonicalOS(p.host.OS) == "ubuntu" && p.host.OSVersion == "24.04" && canonicalArch(p.host.Arch) != ""
}

func targetSummary(h state.Host, label string) string {
	return fmt.Sprintf("%s / %s %s %s / %s", safeText(h.Name, 120), safeText(h.OS, 40), safeText(h.OSVersion, 40), safeText(h.Arch, 24), safeText(label, 100))
}

func parameterKey(key string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(key), " ", "_"))
}

func validateParameters(app assets.App, op operation) (map[string]string, redactor, error) {
	known := make(map[string]assets.Param, len(app.Params))
	for _, param := range app.Params {
		known[parameterKey(param.Key)] = param
	}
	values := make(map[string]string, len(op.Parameters))
	for raw, value := range op.Parameters {
		key := parameterKey(raw)
		if _, ok := known[key]; !ok || !envPattern.MatchString(key) {
			return nil, redactor{}, rpc.Error("BAD_PARAMS", "提交包含应用目录未定义的参数")
		}
		if _, exists := values[key]; exists {
			return nil, redactor{}, rpc.Error("BAD_PARAMS", "提交包含重复的应用参数")
		}
		values[key] = value
	}
	var secrets []string
	for key, param := range known {
		value, provided := values[key]
		if !provided && op.Kind == "install" && param.Default != "" {
			value, provided = param.Default, true
		}
		if op.Kind == "install" && param.Required && strings.TrimSpace(value) == "" {
			return nil, redactor{}, parameterError(param)
		}
		if !provided {
			continue
		}
		if len(value) > 2048 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, redactor{}, parameterError(param)
		}
		if value != "" {
			switch param.Type {
			case "number":
				n, err := strconv.ParseInt(value, 10, 32)
				if key == "PORT" || strings.HasSuffix(key, "_PORT") {
					if err != nil || n < 1 || n > 65535 {
						return nil, redactor{}, parameterError(param)
					}
					value = strconv.FormatInt(n, 10)
				} else if err != nil && !(param.Default == "auto" && value == "auto") {
					return nil, redactor{}, parameterError(param)
				}
			case "boolean":
				value = strings.ToLower(value)
				if value != "true" && value != "false" {
					return nil, redactor{}, parameterError(param)
				}
			case "select":
				if !slices.Contains(param.Options, value) {
					return nil, redactor{}, parameterError(param)
				}
			}
		}
		if param.Type == "secret" || sensitiveKey(key) {
			if value != "" {
				secrets = append(secrets, value)
			}
			if op.Kind != "install" {
				delete(values, key)
				continue
			}
		}
		values[key] = value
	}
	return values, newRedactor(secrets), nil
}

func parameterError(p assets.Param) error {
	label := p.Label
	if label == "" {
		label = p.Key
	}
	return rpc.Error("BAD_PARAMS", "请检查应用参数“"+safeText(label, 80)+"”")
}

func validateDetectionPaths(p plan) error {
	for _, key := range []string{"DATA_DIR", "INSTALL_DIR", "CONFIG_DIR"} {
		value := p.parameters[key]
		if value == "" {
			continue
		}
		if strings.HasPrefix(p.appID, "jdk") && key == "INSTALL_DIR" {
			allowed := "/opt/" + p.appID
			if isWindows(p.host.OS) {
				allowed = `C:\Program Files\` + p.appID
			}
			if !strings.EqualFold(value, allowed) || !isWindows(p.host.OS) && value != allowed {
				return rpc.Error("BAD_PARAMS", "JDK 状态检测只能使用该版本的标准安装目录")
			}
			continue
		}
		if !isWindows(p.host.OS) && (!path.IsAbs(value) || path.Clean(value) != value || value == "/") {
			return rpc.Error("BAD_PARAMS", "检测目录须为远端绝对路径且不能包含路径跳转")
		}
	}
	return nil
}

func readDetection(dir, entry string, windows bool) (string, error) {
	bad := rpc.Error("SCRIPT_UNAVAILABLE", "应用的内置状态检测脚本不可读")
	if !filepath.IsAbs(dir) || !validID(entry) || filepath.Base(entry) != entry ||
		windows && !strings.HasSuffix(strings.ToLower(entry), ".ps1") || !windows && !strings.HasSuffix(entry, ".sh") {
		return "", bad
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", bad
	}
	defer root.Close()
	info, err := root.Lstat(entry)
	if err != nil || !info.Mode().IsRegular() {
		return "", bad
	}
	f, err := root.Open(entry)
	if err != nil {
		return "", bad
	}
	defer f.Close()
	const maxScript = 128 * 1024
	b, err := io.ReadAll(io.LimitReader(f, maxScript+1))
	if err != nil || len(b) > maxScript {
		return "", bad
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	if len(b) == 0 || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "", bad
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n"), nil
}

func sensitiveKey(key string) bool {
	for _, part := range []string{"PASSWORD", "SECRET", "TOKEN", "KEY"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

type redactor struct{ values *strings.Replacer }

func newRedactor(secrets []string) redactor {
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, len(secrets)*8)
	for _, secret := range secrets {
		quoted, _ := json.Marshal(secret)
		for _, v := range []string{secret, string(quoted[1 : len(quoted)-1]), shellQuote(secret),
			strings.ReplaceAll(secret, "'", "''"), base64.StdEncoding.EncodeToString([]byte(secret))} {
			pairs = append(pairs, v, "[已脱敏]")
		}
	}
	return redactor{values: strings.NewReplacer(pairs...)}
}

func (r redactor) text(v string, limit int) string {
	v = ansiSequence.ReplaceAllString(v, "")
	v = secretField.ReplaceAllString(v, "${1}[已脱敏]")
	if r.values != nil {
		v = r.values.Replace(v)
	}
	return safeText(v, limit)
}

func safeText(v string, limit int) string {
	v = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, v))
	if len(v) <= limit {
		return v
	}
	for !utf8.RuneStart(v[limit]) {
		limit--
	}
	return v[:limit] + "…"
}

func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'" }
