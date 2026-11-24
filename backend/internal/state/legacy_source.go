package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

var legacyTables = []string{"hosts", "settings", "custom_apps", "tasks", "logs", "install_history"}

const maxSnapshotRows = 250_000
const maxSnapshotBytes = 128 << 20
const sourcePageSize = 512

// An in-memory preview is built from a single read transaction, including
// committed WAL frames if present. The destination never attaches or copies
// the old SQLite file and never receives raw secrets or arbitrary log text.
type legacySnapshot struct {
	id, pathID, digest string
	tables             map[string]*legacyTable
	warnings           []string
}

type legacyTable struct {
	total, rejected int
	usable          bool
	rows            []legacyRow
}

type legacyRow struct {
	key, target, label, hash string
	value                    any
}

type legacyHistory struct {
	Host, Task, App, Version, Operation, State, Stage, Progress, Severity, Start, End, At string
	Orphan, Omitted                                                                       bool
}

type sourceColumns struct {
	required, regular, presence []string
}

var sourceSchema = map[string]sourceColumns{
	"hosts": {
		required: []string{"id", "name", "ip_address", "port", "username", "auth_type"},
		regular:  []string{"id", "name", "ip_address", "port", "username", "auth_type", "os_type", "os_version", "cpu_architecture", "group_name"},
	},
	"settings": {required: []string{"key", "value"}, regular: []string{"key", "value"}},
	"custom_apps": {
		required: []string{"id", "name", "app_type"},
		regular: []string{"id", "app_key", "name", "app_type", "remote_directory", "remote_frontend_directory", "config_file_path",
			"config_directory", "config_file_name", "log_directory", "is_enabled", "sort_order"},
		presence: []string{"description", "start_command", "stop_command", "status_command"},
	},
	"tasks": {
		required: []string{"id", "host_id", "app_id", "status"},
		regular:  []string{"id", "host_id", "app_id", "app_version", "status", "stage", "progress", "start_time", "end_time"},
		presence: []string{"error_message"},
	},
	"logs": {
		required: []string{"id", "timestamp", "level"},
		regular:  []string{"id", "task_id", "timestamp", "level"},
		presence: []string{"message"},
	},
	"install_history": {
		required: []string{"id", "host_id", "application_id", "operation_type", "status"},
		regular:  []string{"id", "host_id", "application_id", "application_version", "operation_type", "status", "start_time", "end_time"},
		presence: []string{"error_message", "log_content"},
	},
}

var legacyPreferences = map[string]string{
	"currenttheme": "theme", "connectiontimeout": "connectionTimeoutSeconds", "retrycount": "retryCount",
	"retryinterval": "retryIntervalSeconds", "maxconcurrenttasks": "maxConcurrentHosts", "maxcachesizemb": "cacheLimitMiB",
}

var otherOldSettings = map[string]string{
	"repositoryurl": "仓库地址需在新版重新确认", "updatecheckurl": "更新地址需在新版重新确认",
	"repositorytoken": "旧仓库令牌不迁移", "proxypassword": "旧代理密码不迁移", "proxyusername": "代理用户名需在新版重新确认",
	"useproxy": "代理设置需在新版重新确认", "proxytype": "代理设置需在新版重新确认", "proxyhost": "代理设置需在新版重新确认",
	"proxyport": "代理设置需在新版重新确认", "cachedirectory": "缓存目录需在新版重新确认",
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func legacyFault() error {
	return rpc.Error("MIGRATION_SOURCE", "无法读取一致的旧版 SQLite 快照；请检查文件，关闭旧版后重试或选择 SQLite 一致性备份")
}

func (s *Store) loadLegacy(ctx context.Context, path string) (*legacySnapshot, error) {
	if !filepath.IsAbs(path) || !validText(path, 32768, false) || strings.HasPrefix(filepath.VolumeName(path), `\\`) {
		return nil, rpc.Error("BAD_PARAMS", "请选择本机的旧版 SQLite 数据库文件")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil || strings.HasPrefix(filepath.VolumeName(path), `\\`) {
		return nil, legacyFault()
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 100 {
		return nil, legacyFault()
	}
	current, err := os.Stat(filepath.Join(s.Dir, databaseName))
	if err != nil {
		return nil, err
	}
	if os.SameFile(fi, current) {
		return nil, rpc.Error("BAD_PARAMS", "请选择旧版数据库，不能导入当前新版数据库")
	}
	original, err := inspectLegacySource(path)
	if err != nil || !os.SameFile(fi, original.file) {
		return nil, legacyFault()
	}
	dsn := sqliteURI(path, "ro", "query_only(1)", "trusted_schema(0)")
	// A closed, checkpointed WAL-mode file has no sidecars. SQLite would
	// normally create new -wal/-shm even with mode=ro. Treat this offline
	// input as immutable, and check again that it remained closed/stable.
	frozen := original.header[18] == 2 && !original.wal && !original.shm
	if frozen {
		dsn += "&immutable=1"
	} else if original.wal != original.shm {
		return nil, rpc.Error("MIGRATION_SOURCE", "旧库的 WAL 文件组不完整；请退出旧版或选择完整的 SQLite 一致性备份")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, legacyFault()
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, legacyFault()
	}
	defer tx.Rollback()
	snapshot, err := readLegacy(ctx, tx, textDigest(canonicalPath(path)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var fault *rpc.Fault
		if errors.As(err, &fault) {
			return nil, err
		}
		return nil, legacyFault()
	}
	if frozen {
		after, err := inspectLegacySource(path)
		if err != nil || after.wal || after.shm || after.header != original.header || !os.SameFile(after.file, original.file) ||
			after.file.Size() != original.file.Size() || !after.file.ModTime().Equal(original.file.ModTime()) {
			return nil, rpc.Error("MIGRATION_SOURCE", "读取期间旧库发生变化；请关闭旧版或选择 SQLite 一致性备份")
		}
	}
	return snapshot, nil
}

type sourceFile struct {
	file     os.FileInfo
	header   [100]byte
	wal, shm bool
}

func inspectLegacySource(path string) (sourceFile, error) {
	var out sourceFile
	file, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer file.Close()
	out.file, err = file.Stat()
	if err != nil {
		return out, err
	}
	if !out.file.Mode().IsRegular() {
		return out, legacyFault()
	}
	if _, err := io.ReadFull(file, out.header[:]); err != nil {
		return out, err
	}
	if string(out.header[:16]) != "SQLite format 3\x00" {
		return out, legacyFault()
	}
	for _, aux := range []struct {
		suffix string
		exists *bool
	}{{"-wal", &out.wal}, {"-shm", &out.shm}} {
		info, err := os.Lstat(path + aux.suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		if !info.Mode().IsRegular() {
			return out, legacyFault()
		}
		*aux.exists = true
	}
	return out, nil
}

type sourceReader struct {
	id          string
	hosts       map[string]bool
	tasks       map[string]string
	unknownEnum int
	orphan      int
	omitted     int
}

func readLegacy(ctx context.Context, tx *sql.Tx, sourceID string) (*legacySnapshot, error) {
	var sqliteAppID int
	var check string
	if err := tx.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&sqliteAppID); err != nil {
		return nil, err
	}
	if sqliteAppID == applicationID {
		return nil, rpc.Error("MIGRATION_SOURCE", "该文件是新版数据库，不是旧版导入源")
	}
	if err := tx.QueryRowContext(ctx, `PRAGMA quick_check(1)`).Scan(&check); err != nil {
		return nil, err
	}
	if check != "ok" {
		return nil, legacyFault()
	}

	r := sourceReader{id: sourceID, hosts: make(map[string]bool), tasks: make(map[string]string)}
	snap := &legacySnapshot{id: sourceID, pathID: sourceID, tables: make(map[string]*legacyTable)}
	var total, approx int
	h := sha256.New()
	for _, name := range legacyTables {
		item := &legacyTable{}
		snap.tables[name] = item
		columns, err := tableColumns(ctx, tx, name)
		if err != nil {
			return nil, err
		}
		if columns == nil {
			if name == "hosts" {
				return nil, rpc.Error("MIGRATION_SOURCE", "未找到兼容的旧版主机表")
			}
			snap.warnings = append(snap.warnings, "缺少旧表 "+name+"，不会据此删除新版数据。")
			fmt.Fprintln(h, name, "absent")
			continue
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.`+quoted(name)).Scan(&item.total); err != nil {
			return nil, err
		}
		if item.total > maxSnapshotRows-total {
			return nil, snapshotLimit()
		}
		total += item.total
		fmt.Fprintln(h, name, item.total)
		spec := sourceSchema[name]
		item.usable = !columns["_rowid_"]
		for _, field := range spec.required {
			item.usable = item.usable && columns[field]
		}
		if !item.usable {
			if name == "hosts" {
				return nil, rpc.Error("MIGRATION_SOURCE", "旧版主机表缺少标识或认证方式等必要字段")
			}
			item.rejected = item.total
			snap.warnings = append(snap.warnings, "旧表 "+name+" 缺少必要列，整表跳过。")
			fmt.Fprintln(h, "incompatible")
			continue
		}
		if name == "hosts" && (!columns["os_version"] || !columns["cpu_architecture"]) {
			snap.warnings = append(snap.warnings, "旧主机表缺少系统版本或架构列，缺失字段保留为未知，重新连接后可补齐。")
		}
		selected := projection(name, spec, columns)
		fields := append(slices.Clone(spec.regular), spec.presence...)
		seen := make(map[string]bool)
		var after int64
		for {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			query := `SELECT _rowid_, ` + strings.Join(selected, ",") + ` FROM main.` + quoted(name)
			args := []any{sourcePageSize}
			if len(item.rows) != 0 {
				query += ` WHERE _rowid_>?`
				args = []any{after, sourcePageSize}
			}
			rows, err := tx.QueryContext(ctx, query+` ORDER BY _rowid_ LIMIT ?`, args...)
			if err != nil {
				return nil, err
			}
			before := len(item.rows)
			next, err := r.readPage(rows, name, fields, item, seen, h, &approx)
			if err != nil {
				return nil, err
			}
			if len(item.rows) == item.total {
				break
			}
			if len(item.rows) == before || len(item.rows) > item.total {
				return nil, legacyFault()
			}
			after = next
		}
		if len(item.rows) != item.total {
			return nil, legacyFault()
		}
	}
	snap.digest = hex.EncodeToString(h.Sum(nil))
	snap.warnings = append(snap.warnings, "旧密码、私钥路径、仓库和代理凭据均不迁移；主机须补录凭据并确认指纹。")
	if r.orphan != 0 {
		snap.warnings = append(snap.warnings, fmt.Sprintf("保留 %d 条关联缺失的历史记录并标记为孤儿；不会运行历史任务。", r.orphan))
	}
	if r.omitted != 0 {
		snap.warnings = append(snap.warnings, fmt.Sprintf("%d 条记录的历史正文、错误详情或旧版应用描述/命令已省略，以免保留秘密。应用命令需人工复核后重新录入。", r.omitted))
	}
	if r.unknownEnum != 0 {
		snap.warnings = append(snap.warnings, fmt.Sprintf("%d 条记录含有未知的操作、状态、日志级别或主机 OS 标识，已标为未知。", r.unknownEnum))
	}
	return snap, nil
}

func snapshotLimit() error {
	return rpc.Error("MIGRATION_LIMIT", "旧库的安全预览超过 25 万行或 128 MiB；请先生成范围更小的一致性备份")
}

func quoted(name string) string { return `"` + name + `"` } // only used with fixed allowlisted identifiers

func tableColumns(ctx context.Context, q queryer, table string) (map[string]bool, error) {
	var kind string
	var withoutRowID bool
	err := q.QueryRowContext(ctx, `SELECT type,wr FROM pragma_table_list WHERE schema='main' AND name=? COLLATE NOCASE`, table).Scan(&kind, &withoutRowID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (kind != "table" || withoutRowID) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT name,hidden FROM pragma_table_xinfo(?, 'main')`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var field string
		var hidden int
		if err := rows.Scan(&field, &hidden); err != nil {
			return nil, err
		}
		if hidden == 0 {
			cols[strings.ToLower(field)] = true
		}
	}
	return cols, rows.Err()
}

func projection(table string, spec sourceColumns, columns map[string]bool) []string {
	out := make([]string, 0, len(spec.regular)+len(spec.presence))
	for _, col := range spec.regular {
		if !columns[col] {
			out = append(out, "NULL")
			continue
		}
		q := quoted(col)
		// Do not send megabyte-size or blob fields across the driver. Oversized
		// values become invalid/missing fields, rather than truncated identities.
		value := `CASE WHEN typeof(` + q + `) IN ('text','integer','real') AND length(CAST(` + q + ` AS BLOB)) <= 8192 THEN CAST(` + q + ` AS TEXT) END`
		if table == "settings" && col == "value" {
			keys := sortedKeys(legacyPreferences)
			value = `CASE WHEN lower("key") IN ('` + strings.Join(keys, "','") + `') THEN ` + value + ` END`
		}
		out = append(out, value)
	}
	for _, col := range spec.presence {
		if !columns[col] {
			out = append(out, "NULL")
			continue
		}
		q := quoted(col)
		// SQL reports only whether text was omitted, not its content nor a hash.
		out = append(out, `CASE WHEN `+q+` IS NOT NULL AND `+q+`<>'' THEN '1' END`)
	}
	return out
}

func (r *sourceReader) readPage(rows *sql.Rows, table string, names []string, item *legacyTable, seen map[string]bool, digest interface{ Write([]byte) (int, error) }, approx *int) (int64, error) {
	defer rows.Close()
	fields := make([]sql.NullString, len(names))
	targets := make([]any, len(names)+1)
	var rowID int64
	targets[0] = &rowID
	m := make(map[string]string, len(names))
	for i := range fields {
		targets[i+1] = &fields[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return 0, err
		}
		for i, name := range names {
			m[name] = fields[i].String
		}
		rec := r.record(table, rowID, m)
		if rec.target != "" && seen[rec.key] {
			return 0, rpc.Error("MIGRATION_SOURCE", "旧库含重复的行标识，无法安全对账")
		}
		if rec.target != "" {
			seen[rec.key] = true
		}
		encoded, err := json.Marshal(rec.value)
		if err != nil {
			return 0, err
		}
		rec.hash = textDigest(string(encoded))
		*approx += 640 + len(encoded) + len(rec.key) + len(rec.target) + len(rec.label)
		if *approx > maxSnapshotBytes {
			return 0, snapshotLimit()
		}
		fmt.Fprintf(digest, "%q %q %q\n", rec.key, rec.hash, rec.label)
		item.rows = append(item.rows, rec)
	}
	return rowID, rows.Err()
}

func (r *sourceReader) record(table string, rowID int64, m map[string]string) legacyRow {
	key := m["id"]
	if table == "settings" {
		return preferenceRecord(rowID, m["key"], m["value"])
	}
	if !validID(key) {
		return unnamedRow(rowID, "旧行缺少受支持的主键，已跳过")
	}
	switch table {
	case "hosts":
		osName := legacyOS(m["os_type"])
		if osName == "unknown" && m["os_type"] != "" {
			r.unknownEnum++
		}
		port, _ := strconv.Atoi(m["port"])
		h := Host{ID: key, Name: strings.TrimSpace(m["name"]), Address: strings.TrimSpace(m["ip_address"]),
			Port: port, Username: strings.TrimSpace(m["username"]), Group: strings.TrimSpace(m["group_name"]),
			Auth: legacyAuth(m["auth_type"]), OS: osName,
			OSVersion: strings.TrimSpace(m["os_version"]), Arch: legacyArch(m["cpu_architecture"])}
		if !validHost(h) {
			return legacyRow{key: key, label: "主机地址、账号或认证方式无效，已跳过"}
		}
		r.hosts[key] = true
		return legacyRow{key: key, target: key, label: h.Name, value: h}
	case "custom_apps":
		id := m["app_key"]
		if id == "" {
			id = key
		}
		if strings.EqualFold(id, "SUPPORT") || strings.EqualFold(id, "XXL_JOB") {
			id = strings.ToUpper(id)
		}
		app := customRow{CustomApp: CustomApp{ID: id, Name: strings.TrimSpace(m["name"]), Kind: strings.ToUpper(strings.TrimSpace(m["app_type"])),
			Directory: legacyPath(m["remote_directory"]), FrontendDirectory: legacyPath(m["remote_frontend_directory"]),
			ConfigPath: legacyPath(m["config_file_path"]), LogsPath: legacyPath(m["log_directory"]), Enabled: true}, Sort: 100}
		if app.ConfigPath == "" {
			app.ConfigPath = joinLegacyPath(m["config_directory"], m["config_file_name"])
		}
		if raw := strings.ToLower(m["is_enabled"]); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return legacyRow{key: key, label: "旧应用启用状态无效，已跳过"}
			}
			app.Enabled = b
		}
		if m["sort_order"] != "" {
			if n, err := strconv.ParseInt(m["sort_order"], 10, 32); err == nil {
				app.Sort = int(n)
			}
		}
		for _, seed := range defaultCustomApps() {
			if id == seed.ID {
				app.Description = seed.Description
			}
		}
		if !validCustom(app.CustomApp) {
			return legacyRow{key: key, label: "旧应用标识、名称、类型或路径无效，已跳过"}
		}
		if anyPresent(m, "description", "start_command", "stop_command", "status_command") {
			r.omitted++
		}
		return legacyRow{key: key, target: id, label: app.Name, value: app}
	default:
		return r.historyRow(table, key, m)
	}
}

func unnamedRow(rowID int64, label string) legacyRow {
	return legacyRow{key: "row-" + strconv.FormatInt(rowID, 10), label: label}
}

func preferenceRecord(rowID int64, key, value string) legacyRow {
	name := strings.ToLower(key)
	newKey, exists := legacyPreferences[name]
	if !exists {
		if reason, known := otherOldSettings[name]; known {
			return legacyRow{key: name, label: reason}
		}
		return unnamedRow(rowID, "未知旧设置未读取值，已跳过")
	}
	value = strings.TrimSpace(value)
	if newKey == "theme" {
		switch strings.ToLower(value) {
		case "0", "dark":
			value = "dark"
		case "1", "light":
			value = "light"
		case "2", "system":
			value = "system"
		}
	} else if n, err := strconv.Atoi(value); err == nil {
		value = strconv.Itoa(n)
	}
	if !validPreference(newKey, value) {
		return legacyRow{key: name, label: "旧设置超出有效范围，已跳过"}
	}
	return legacyRow{key: name, target: newKey, label: newKey, value: value}
}

func legacyOS(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "windows", "windows server":
		return "windows"
	case "1", "linux":
		return "linux"
	case "2", "centos":
		return "centos"
	case "3", "ubuntu":
		return "ubuntu"
	default:
		return "unknown"
	}
}

func legacyArch(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "x86_64", "x64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "x86", "i386", "i686", "386":
		return "386"
	case "armv7l", "arm":
		return "arm"
	default:
		return ""
	}
}

func legacyAuth(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "password":
		return "password"
	case "1", "privatekey", "key":
		return "key"
	default:
		return ""
	}
}

func legacyPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if !validText(raw, 2048, false) || strings.Contains(raw, "://") || strings.ContainsAny(raw, "?#") {
		return ""
	}
	return raw
}

func joinLegacyPath(dir, name string) string {
	dir, name = legacyPath(dir), legacyPath(name)
	if dir == "" || name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return ""
	}
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	return legacyPath(strings.TrimRight(dir, `/\`) + sep + name)
}

func anyPresent(m map[string]string, fields ...string) bool {
	for _, field := range fields {
		if m[field] == "1" {
			return true
		}
	}
	return false
}

func (r *sourceReader) historyRow(table, key string, m map[string]string) legacyRow {
	v := legacyHistory{Host: legacyTag(m["host_id"]), Start: legacyTime(m["start_time"]), End: legacyTime(m["end_time"])}
	v.At = v.Start
	if v.At == "" {
		v.At = v.End
	}
	switch table {
	case "tasks":
		v.Task, v.App, v.Version = key, legacyTag(m["app_id"]), legacyTag(m["app_version"])
		v.Operation, v.State = "install", taskState(m["status"])
		v.Stage = enumValue(m["stage"], "preparing", "connecting", "uploading", "extracting", "configuring", "installing", "starting", "verifying", "completed", "failed", "cancelled")
		if n, err := strconv.ParseFloat(m["progress"], 64); err == nil && !math.IsNaN(n) && n >= 0 && n <= 100 {
			v.Progress = strconv.FormatFloat(n, 'f', -1, 64)
		}
		v.Orphan, v.Omitted = !r.hosts[v.Host], anyPresent(m, "error_message")
		r.tasks[key] = v.Host
	case "logs":
		v.Task, v.At = legacyTag(m["task_id"]), legacyTime(m["timestamp"])
		v.Severity = enumValue(m["level"], "info", "success", "warning", "error")
		var parent bool
		v.Host, parent = r.tasks[v.Task]
		v.Orphan, v.Omitted = m["task_id"] != "" && (!parent || !r.hosts[v.Host]), anyPresent(m, "message")
	case "install_history":
		v.App, v.Version = legacyTag(m["application_id"]), legacyTag(m["application_version"])
		v.Operation, v.State = enumValue(m["operation_type"], "install", "uninstall"), taskState(m["status"])
		v.Orphan, v.Omitted = !r.hosts[v.Host], anyPresent(m, "error_message", "log_content")
	}
	if v.Orphan {
		r.orphan++
	}
	if v.Omitted {
		r.omitted++
	}
	if v.Operation == "unknown" || v.State == "unknown" || v.Severity == "unknown" {
		r.unknownEnum++
	}
	return legacyRow{key: key, target: historyID(r.id, table, key), value: v}
}

func legacyTag(raw string) string {
	if validID(raw) {
		return raw
	}
	return ""
}

var legacyDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})?)?$`)

func legacyTime(raw string) string {
	if legacyDatePattern.MatchString(raw) {
		return raw
	} // Preserve the original time zone semantics.
	return ""
}

func taskState(raw string) string {
	return enumValue(raw, "interrupted", "interrupted", "interrupted", "succeeded", "failed", "cancelled")
}

func enumValue(raw string, values ...string) string {
	i, err := strconv.Atoi(strings.TrimSpace(raw))
	if err == nil && i >= 0 && i < len(values) {
		return values[i]
	}
	return "unknown"
}
