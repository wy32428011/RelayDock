// Package state owns the desktop profile, credential references, and legacy imports.
package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"

	"remoteinstaller.local/core/internal/rpc"
)

const databaseName = "state.db"
const applicationID = 0x52494e53 // RINS
const schemaVersion = 1

// DefaultVersion is used for standalone core builds that do not set a release version.
const DefaultVersion = "0.1.3"

type Store struct {
	DB      *sql.DB
	Dir     string
	Version string // Set at startup when the core is built with a different version.

	vault      secretVault
	service    string
	secretGate chan struct{}
	session    map[string]Credential
	migration  chan struct{}
	preview    *pendingPreview
	transport  http.RoundTripper // nil uses a private transport with the configured proxy
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type scanner interface{ Scan(...any) error }

func requireOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return rpc.Error("STATE_VERIFY", "数据库未按预期写入记录；已取消操作")
	}
	return nil
}

type okResult struct {
	OK bool `json:"ok"`
}

type idParams struct {
	ID string `json:"id"`
}

func Open(dir string) (*Store, error) { return openWithVault(dir, systemVault{}) }

func openWithVault(dir string, vault secretVault) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("未提供新版数据目录")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, databaseName)
	if fi, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("新版数据库必须为普通文件")
	}
	// In WAL mode a read-then-write upgrade can fail without busy waiting.
	// Grab the write reservation at BeginTx; read-only transactions stay deferred.
	db, err := sql.Open("sqlite", sqliteURI(path, "rw", "foreign_keys(1)", "trusted_schema(0)")+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := initialize(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{
		DB: db, Dir: dir, Version: DefaultVersion, vault: vault,
		service:    "RelayDock." + textDigest(canonicalPath(dir))[:32],
		secretGate: make(chan struct{}, 1), session: make(map[string]Credential), migration: make(chan struct{}, 1),
	}
	// A core is started by a single-instance desktop app. Unreferenced vault
	// entries left by an interrupted write are safe to clean on the next open.
	s.collectSecrets(ctx)
	return s, nil
}

func sqliteURI(path, mode string, pragmas ...string) string {
	name := filepath.ToSlash(path)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name // Windows drive path in a file URI.
	}
	q := url.Values{"mode": {mode}, "_pragma": append([]string{"busy_timeout(5000)"}, pragmas...)}
	return (&url.URL{Scheme: "file", Path: name, RawQuery: q.Encode()}).String()
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func textDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func acquire(ctx context.Context, gate chan struct{}) error {
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) Close() error {
	// The stdio server has drained in-flight requests before closing the store.
	s.migration <- struct{}{}
	defer func() { <-s.migration }()
	s.secretGate <- struct{}{}
	defer func() { <-s.secretGate }()
	clear(s.session)
	s.preview = nil
	return s.DB.Close()
}

func initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var appID, version int
	if err := tx.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&appID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	switch {
	case appID == applicationID && version == schemaVersion:
		// Future schema transitions must take a verified backup before upgrading.
	case appID == 0 && version == 0:
		var objects int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return errors.New("该文件不是新版数据库；请使用迁移工具")
		}
		if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
			return err
		}
		for _, seed := range defaultCustomApps() {
			if err := writeCustom(ctx, tx, seed); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d", applicationID, schemaVersion)); err != nil {
			return err
		}
	default:
		return errors.New("不支持的新版数据库版本或标识；未改写数据库")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	var journal string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return err
	}
	if !strings.EqualFold(journal, "wal") {
		return errors.New("无法启用 SQLite WAL")
	}
	return nil
}

const schemaV1 = `
CREATE TABLE hosts (
    id TEXT PRIMARY KEY NOT NULL, name TEXT NOT NULL, address TEXT NOT NULL,
    port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535), username TEXT NOT NULL,
    group_name TEXT NOT NULL, auth TEXT NOT NULL CHECK(auth IN ('password','key')),
    os TEXT NOT NULL, os_version TEXT NOT NULL, arch TEXT NOT NULL,
    fingerprint TEXT NOT NULL, last_seen TEXT NOT NULL, credential_ref TEXT NOT NULL DEFAULT ''
);
CREATE TABLE preferences (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL);
CREATE TABLE preference_credentials (key TEXT PRIMARY KEY NOT NULL, ref TEXT NOT NULL);
CREATE TABLE vault_gc (ref TEXT PRIMARY KEY NOT NULL);
CREATE TABLE custom_apps (
    id TEXT PRIMARY KEY NOT NULL, name TEXT NOT NULL, name_key TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK(kind IN ('SUPPORT','GENERIC','CUSTOM')), description TEXT NOT NULL,
    directory TEXT NOT NULL, frontend_directory TEXT NOT NULL, config_path TEXT NOT NULL,
    start_command TEXT NOT NULL, stop_command TEXT NOT NULL, status_command TEXT NOT NULL,
    logs_path TEXT NOT NULL, enabled INTEGER NOT NULL CHECK(enabled IN (0,1)), sort_order INTEGER NOT NULL
);
CREATE TABLE legacy_map (
    source_id TEXT NOT NULL, table_name TEXT NOT NULL, source_key TEXT NOT NULL,
    target_id TEXT NOT NULL, source_hash TEXT NOT NULL, target_hash TEXT NOT NULL,
    PRIMARY KEY(source_id, table_name, source_key)
) WITHOUT ROWID;
CREATE TABLE legacy_history (
    id TEXT PRIMARY KEY NOT NULL, source_id TEXT NOT NULL, kind TEXT NOT NULL,
    host_key TEXT NOT NULL, task_key TEXT NOT NULL, app_id TEXT NOT NULL, app_version TEXT NOT NULL,
    operation TEXT NOT NULL, state TEXT NOT NULL, stage TEXT NOT NULL, progress TEXT NOT NULL,
    severity TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL, at TEXT NOT NULL,
    orphan INTEGER NOT NULL, omitted INTEGER NOT NULL, body_hash TEXT NOT NULL
);
CREATE INDEX legacy_history_page ON legacy_history(at DESC, kind, id);
CREATE INDEX legacy_history_source ON legacy_history(source_id);
CREATE TABLE legacy_batches (
    preview_id TEXT PRIMARY KEY NOT NULL, source_id TEXT NOT NULL, snapshot_hash TEXT NOT NULL,
    hosts_only INTEGER NOT NULL, accept_updates INTEGER NOT NULL, applied_at TEXT NOT NULL, result TEXT NOT NULL
);`

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

func decodeID(p json.RawMessage) (string, error) {
	var in idParams
	if err := rpc.Decode(p, &in); err != nil {
		return "", err
	}
	if !validID(in.ID) {
		return "", rpc.Error("BAD_PARAMS", "记录 ID 无效")
	}
	return in.ID, nil
}

func (s *Store) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "host.list", "custom.list", "settings.get", "settings.updateCheck":
		if err := rpc.Decode(raw, &struct{}{}); err != nil {
			return nil, err
		}
		switch method {
		case "host.list":
			return s.listHosts(ctx)
		case "custom.list":
			return s.listCustom(ctx)
		case "settings.get":
			return s.getSettings(ctx)
		default:
			return s.checkUpdate(ctx)
		}
	case "host.delete", "custom.delete":
		id, err := decodeID(raw)
		if err != nil {
			return nil, err
		}
		if method == "host.delete" {
			err = s.deleteHost(ctx, id)
		} else {
			_, err = s.DB.ExecContext(ctx, `DELETE FROM custom_apps WHERE id=?`, id)
		}
		return okResult{OK: err == nil}, err
	case "custom.save":
		var p CustomApp
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.saveCustom(ctx, p)
	case "settings.save":
		var p settingsInput
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.saveSettings(ctx, p)
	case "migration.preview":
		var p struct {
			Token string `json:"token"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.previewLegacy(ctx, p.Token)
	case "migration.apply":
		var p applyParams
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.applyLegacy(ctx, p)
	case "migration.history":
		var p historyParams
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.readHistory(ctx, p)
	default:
		return nil, rpc.Error("UNKNOWN_METHOD", "不受支持的本地数据操作")
	}
}

type secretVault interface {
	Set(service, user, value string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type systemVault struct{}

func (systemVault) Set(service, id, v string) error        { return keyring.Set(service, id, v) }
func (systemVault) Get(service, id string) (string, error) { return keyring.Get(service, id) }
func (systemVault) Delete(service, id string) error        { return keyring.Delete(service, id) }
