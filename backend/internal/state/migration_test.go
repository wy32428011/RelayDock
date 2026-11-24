package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const oldSecret = "SYNTHETIC-LEGACY-DO-NOT-COPY-c7b8e990"

func oldDatabase(t *testing.T, wal bool) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "合成 # 旧库.db")
	db, err := sql.Open("sqlite", sqliteURI(path, "rwc", "wal_autocheckpoint(0)"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if wal {
		var mode string
		if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("WAL setup: %s %v", mode, err)
		}
	}
	return path, db
}

func execOld(t *testing.T, db *sql.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(sql, args...); err != nil {
		t.Fatal(err)
	}
}

const oldSixTables = `
CREATE TABLE hosts(id TEXT PRIMARY KEY,name TEXT,ip_address TEXT,port INT,username TEXT,auth_type INT,os_type INT,
    group_name TEXT,os_version TEXT,cpu_architecture TEXT, encrypted_password TEXT,key_path TEXT,encrypted_key_passphrase TEXT,status_message TEXT);
CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT);
CREATE TABLE custom_apps(id TEXT PRIMARY KEY,app_key TEXT UNIQUE,name TEXT,description TEXT,app_type TEXT,
    remote_directory TEXT,remote_frontend_directory TEXT,config_file_path TEXT,config_directory TEXT,config_file_name TEXT,log_directory TEXT,
    is_enabled INT,sort_order INT,start_command TEXT,stop_command TEXT);
CREATE TABLE tasks(id TEXT PRIMARY KEY,host_id TEXT,app_id TEXT,app_version TEXT,status INT,stage INT,progress REAL,start_time TEXT,end_time TEXT,error_message TEXT);
CREATE TABLE logs(id INTEGER PRIMARY KEY,task_id TEXT,timestamp TEXT,level INT,message TEXT);
CREATE TABLE install_history(id INTEGER PRIMARY KEY,host_id TEXT,application_id TEXT,application_version TEXT,operation_type INT,status INT,start_time TEXT,end_time TEXT,error_message TEXT,log_content TEXT);
`

func fullFixture(t *testing.T, historicalLogs int) (string, *sql.DB) {
	t.Helper()
	path, db := oldDatabase(t, true)
	execOld(t, db, oldSixTables)
	execOld(t, db, `INSERT INTO hosts VALUES
		('h-a', '合成 Ubuntu', '192.0.2.19', 22, 'demo', 0, 3, '合成组', '24.04', 'aarch64', ?, ?, ?, ?),
		('h-b', '合成 Windows', '192.0.2.21', 2201, 'demo', 1, 0, '合成组', '2022', 'x64', ?, ?, ?, ?)`,
		oldSecret, oldSecret, oldSecret, oldSecret, oldSecret, oldSecret, oldSecret, oldSecret)
	execOld(t, db, `INSERT INTO settings VALUES('CurrentTheme','1'),('ConnectionTimeout','45'),('RetryCount','1'),('RetryInterval','2'),
		('MaxConcurrentTasks','5'),('MaxCacheSizeMB','2048'),('RepositoryToken',?),('ProxyPassword',?),('RepositoryUrl',?),('UnknownFutureKey',?)`,
		oldSecret, oldSecret, "https://test.invalid/"+oldSecret, oldSecret)
	execOld(t, db, `INSERT INTO custom_apps VALUES
		('old-support','SUPPORT','Support',?,'SUPPORT','/opt/zeus-support','/var/www/zeus-support',NULL,'/opt/zeus-support/conf','application-prod.properties','/opt/zeus-support/log',0,10,?,?),
		('old-generic','DEMO_GENERIC','合成通用部署',?,'GENERIC','C:\demo',NULL,NULL,'C:\demo\etc','demo.ini','C:\demo\log',1,30,?,NULL),
		('old-custom','DEMO_CUSTOM','合成自定部署',NULL,'CUSTOM','/var/demo',NULL,NULL,NULL,NULL,NULL,1,40,NULL,NULL)`,
		oldSecret, "echo "+oldSecret, oldSecret, oldSecret, oldSecret)
	execOld(t, db, `WITH RECURSIVE counter(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM counter WHERE n<141)
		INSERT INTO tasks SELECT printf('t-%03d',n),'h-a','redis','7.2.0',CASE WHEN n=1 THEN 1 ELSE 3 END,7,42.5,
		datetime('2025-05-01 12:30:30',printf('+%d seconds',n)),NULL,? FROM counter`, oldSecret)
	execOld(t, db, `WITH RECURSIVE counter(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM counter WHERE n<?)
		INSERT INTO logs SELECT n,CASE WHEN n=1 THEN 'missing-task' ELSE 't-002' END,
		datetime('2025-05-02 12:30:30',printf('+%d seconds',n)),2,? FROM counter`, historicalLogs, oldSecret)
	execOld(t, db, `INSERT INTO install_history VALUES(1,'h-a','redis','7.2.0',1,4,'2025-05-03 00:00:00','2025-05-03 00:12:00',?,?),
		(2,'absent-host','nginx','1.26.0',0,3,'2025-05-03 00:00:00',NULL,?,?)`, oldSecret, oldSecret, oldSecret, oldSecret)
	return path, db
}

func runPreview(t *testing.T, s *Store, path string) migrationPreview {
	t.Helper()
	return call[migrationPreview](t, s, "migration.preview", map[string]string{"token": path})
}

func runApply(t *testing.T, s *Store, preview migrationPreview, hostsOnly, acceptUpdates bool) applyResult {
	t.Helper()
	return call[applyResult](t, s, "migration.apply", applyParams{PreviewID: preview.PreviewID, HostsOnly: hostsOnly, AcceptUpdates: acceptUpdates})
}

func containsChange(p migrationPreview, table, key, kind string) bool {
	for _, change := range p.Changes {
		if change.Table == table && change.Key == key && change.Kind == kind {
			return true
		}
	}
	return false
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLegacyWALSnapshotSixTablesAndIdempotence(t *testing.T) {
	s, v := testStore(t)
	const largeEnoughToPage = 1201
	path, old := fullFixture(t, largeEnoughToPage)
	dbBefore := fileBytes(t, path)
	walBefore := fileBytes(t, path+"-wal")
	if len(walBefore) < 32 {
		t.Fatal("fixture has no uncheckpointed WAL frames")
	}
	preview := runPreview(t, s, path)
	if !bytes.Equal(dbBefore, fileBytes(t, path)) || !bytes.Equal(walBefore, fileBytes(t, path+"-wal")) {
		t.Fatal("source database or WAL changed during preview")
	}
	if countRows(t, s.DB, "hosts") != 0 || countRows(t, s.DB, "legacy_batches") != 0 || len(v.entries) != 0 {
		t.Fatal("preview modified destination data or used the vault")
	}
	want := map[string]int{"hosts": 2, "settings": 10, "custom_apps": 3, "tasks": 141, "logs": largeEnoughToPage, "install_history": 2}
	if !reflect.DeepEqual(preview.Totals, want) || preview.NeedsCredentials != 2 || len(preview.Changes) != maxPreviewChanges {
		t.Fatalf("bad preview totals, credentials or cap: %v %d %d", preview.Totals, preview.NeedsCredentials, len(preview.Changes))
	}
	previewWire, _ := json.Marshal(preview)
	if bytes.Contains(previewWire, []byte(oldSecret)) {
		t.Fatal("legacy marker in preview")
	}
	// A new commit to the source after preview must not alter the approved snapshot.
	execOld(t, old, `INSERT INTO logs(task_id,timestamp,level,message) VALUES('t-002','2027-01-01 00:00:00',2,?)`, oldSecret)
	latestSource := fileBytes(t, path+"-wal")
	imported := runApply(t, s, preview, false, true)
	if imported.Imported["logs"] != largeEnoughToPage || imported.Imported["settings"] != 6 || imported.Skipped["settings"] != 4 {
		t.Fatalf("bad apply reconciliation: %#v", imported)
	}
	for table, total := range want {
		if imported.Imported[table]+imported.Skipped[table] != total {
			t.Fatalf("unreconciled %s", table)
		}
	}
	if !bytes.Equal(dbBefore, fileBytes(t, path)) || !bytes.Equal(latestSource, fileBytes(t, path+"-wal")) {
		t.Fatal("apply modified source")
	}
	for _, host := range call[[]Host](t, s, "host.list", nil) {
		if host.CredentialReady || host.Fingerprint != "" || host.LastSeen != "" {
			t.Fatalf("legacy host improperly trusted: %s", host.ID)
		}
		if host.ID == "h-a" && (host.Arch != "arm64" || host.OS != "ubuntu" || host.Group != "合成组") {
			t.Fatalf("host metadata dropped: %#v", host)
		}
		if host.ID == "h-b" && (host.Arch != "amd64" || host.OS != "windows" || host.Auth != "key") {
			t.Fatalf("host metadata dropped: %#v", host)
		}
	}
	cfg := call[Settings](t, s, "settings.get", nil)
	if cfg.Theme != "light" || cfg.ConnectionTimeoutSeconds != 45 || cfg.MaxConcurrentHosts != 5 || cfg.RepositoryURL != "" || cfg.ProxyPasswordSet || cfg.RepositoryTokenSet {
		t.Fatalf("safe setting migration mismatch: %#v", cfg)
	}
	apps := call[[]CustomApp](t, s, "custom.list", nil)
	if len(apps) != 4 || apps[0].ID != "SUPPORT" || apps[0].Enabled || apps[2].ConfigPath != `C:\demo\etc\demo.ini` || apps[2].StartCommand != "" || apps[3].Kind != "CUSTOM" {
		t.Fatalf("stable seeds, definitions or ordering not retained: %#v", apps)
	}
	kinds := assertAllHistoryPages(t, s, 141+largeEnoughToPage+2)
	if kinds["tasks"] != 141 || kinds["logs"] != largeEnoughToPage || kinds["install_history"] != 2 {
		t.Fatalf("history kind totals: %v", kinds)
	}
	assertNoSecrets(t, s, oldSecret)
	if len(v.entries) != 0 {
		t.Fatal("legacy migration created credential entries")
	}
	// After reopening (including a crash after commit before sending a response),
	// the same preview and options retrieve the durable result, not a repeat write.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s.Dir, v)
	if replay := runApply(t, s, preview, false, true); !reflect.DeepEqual(replay, imported) {
		t.Fatalf("durable replay mismatch: %#v", replay)
	}
	if countRows(t, s.DB, "legacy_batches") != 1 {
		t.Fatal("replay inserted new batch")
	}
	fresh := runPreview(t, s, path)
	if again := runApply(t, s, fresh, false, true); again.Imported["logs"] != 1 || again.Imported["hosts"] != 0 || again.Imported["tasks"] != 0 {
		t.Fatalf("new snapshot did not reuse stable rows: %#v", again)
	}
	if countRows(t, s.DB, "legacy_history") != 141+largeEnoughToPage+3 {
		t.Fatal("duplicate history after re-import")
	}
}

func assertAllHistoryPages(t *testing.T, s *Store, total int) map[string]int {
	t.Helper()
	rows := make(map[string]bool)
	kinds := make(map[string]int)
	var orphan, interrupted, omitted bool
	latest := "9999"
	for offset := 0; ; offset += 137 {
		page := call[[]legacyRecord](t, s, "migration.history", map[string]int{"limit": 137, "offset": offset})
		for _, item := range page {
			if rows[item.ID] || item.At > latest {
				t.Fatalf("unstable page: offset %d", offset)
			}
			rows[item.ID], latest = true, item.At
			kinds[item.Kind]++
			orphan = orphan || strings.Contains(item.Detail, "孤儿记录")
			interrupted = interrupted || strings.Contains(item.Summary, "待复查") && strings.Contains(item.Detail, "旧任务 ID：t-001")
			omitted = omitted || strings.Contains(item.Detail, "正文或错误详情已省略")
			if strings.Contains(item.Detail+item.Summary, oldSecret) {
				t.Fatal("sensitive history string")
			}
		}
		if len(page) < 137 {
			break
		}
	}
	if len(rows) != total || !orphan || !interrupted || !omitted {
		t.Fatalf("history incomplete: count %d/%d, orphan %v, interrupted %v, redacted %v", len(rows), total, orphan, interrupted, omitted)
	}
	return kinds
}

func minimalOld(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path, db := oldDatabase(t, false)
	execOld(t, db, `CREATE TABLE hosts(id TEXT PRIMARY KEY,name TEXT,ip_address TEXT,port INT,username TEXT,auth_type INT,os_type INT);
		INSERT INTO hosts VALUES('old-host','演练主机','192.0.2.200',22,'tester',0,2);`)
	return path, db
}

func TestMigrationConflictsStalenessAndAddressUnpinning(t *testing.T) {
	s, v := testStore(t)
	path, db := minimalOld(t)
	before := runPreview(t, s, path)
	sameID := testHost("old-host")
	if _, err := s.SaveHost(t.Context(), sameID, Credential{Secret: "demo-session"}, false); err != nil {
		t.Fatal(err)
	}
	_, err := callRaw(t, s, "migration.apply", applyParams{PreviewID: before.PreviewID, HostsOnly: true})
	assertFault(t, err, "MIGRATION_STALE")
	if countRows(t, s.DB, "legacy_batches") != 0 {
		t.Fatal("stale migration wrote batch")
	}
	call[okResult](t, s, "host.delete", idParams{ID: sameID.ID})
	initial := runPreview(t, s, path)
	if !strings.Contains(strings.Join(initial.Warnings, " "), "缺少系统版本或架构列") {
		t.Fatal("older schema warning missing")
	}
	start := runApply(t, s, initial, true, false)
	if start.Imported["hosts"] != 1 || len(start.Imported) != 1 {
		t.Fatal("hosts-only imported wrong scope")
	}
	host, err := s.Host(t.Context(), "old-host")
	if err != nil || host.OS != "centos" || host.OSVersion != "" || host.Arch != "" {
		t.Fatalf("recovered old OS: %#v %v", host, err)
	}
	host.Fingerprint, host.LastSeen = "SHA256:fake-pin", "2026-11-01T03:00:00Z"
	if _, err := s.SaveHost(t.Context(), host, Credential{Secret: "SYNTHETIC-OLD-ENDPOINT"}, true); err != nil {
		t.Fatal(err)
	}
	execOld(t, db, `UPDATE hosts SET ip_address='192.0.2.201',port=2200 WHERE id='old-host'`)
	changed := runPreview(t, s, path)
	if !containsChange(changed, "hosts", "old-host", "update") || changed.NeedsCredentials != 1 || !strings.Contains(changed.Changes[0].Label, "192.0.2.200:22") {
		t.Fatalf("address update not displayed: %#v", changed.Changes)
	}
	if declined := runApply(t, s, changed, true, false); declined.Imported["hosts"] != 0 || declined.Skipped["hosts"] != 1 {
		t.Fatalf("unapproved update: %#v", declined)
	}
	if current, _ := s.Host(t.Context(), "old-host"); current.Address != "192.0.2.200" || current.Fingerprint == "" || !current.CredentialReady {
		t.Fatal("unapproved update changed binding")
	}
	if res := runApply(t, s, runPreview(t, s, path), true, true); res.Imported["hosts"] != 1 {
		t.Fatalf("confirmed update: %#v", res)
	}
	current, err := s.Host(t.Context(), "old-host")
	if err != nil || current.Fingerprint != "" || current.LastSeen != "" || current.CredentialReady || current.Port != 2200 || len(v.entries) != 0 {
		t.Fatalf("address update retained old trust: %#v %v", current, err)
	}
	current.Name = "新版单独编辑"
	if _, err := s.SaveHost(t.Context(), current, Credential{Secret: "another-session"}, false); err != nil {
		t.Fatal(err)
	}
	// Reusing an unchanged source must not erase the local-edit baseline.
	runApply(t, s, runPreview(t, s, path), true, true)
	execOld(t, db, `UPDATE hosts SET name='旧版同时编辑' WHERE id='old-host';
		INSERT INTO hosts VALUES('new-other','另一主机','192.0.2.207',22,'tester',0,1);`)
	conflict := runPreview(t, s, path)
	if !containsChange(conflict, "hosts", "old-host", "conflict") {
		t.Fatal("both-sides edit not identified")
	}
	_, err = callRaw(t, s, "migration.apply", applyParams{PreviewID: conflict.PreviewID, HostsOnly: true, AcceptUpdates: true})
	assertFault(t, err, "MIGRATION_CONFLICT")
	if countRows(t, s.DB, "hosts") != 1 {
		t.Fatal("conflict partially committed")
	}
	execOld(t, db, `DELETE FROM hosts WHERE id='old-host'`)
	removed := runPreview(t, s, path)
	if !containsChange(removed, "hosts", "old-host", "missing") {
		t.Fatal("source deletion not reported")
	}
	runApply(t, s, removed, true, true)
	if countRows(t, s.DB, "hosts") != 2 {
		t.Fatal("source deletion propagated silently")
	}
}

func TestMigrationRollbackOnVerificationFailure(t *testing.T) {
	s, _ := testStore(t)
	path, _ := fullFixture(t, 2)
	preview := runPreview(t, s, path)
	execOld(t, s.DB, `CREATE TRIGGER fixture_change AFTER INSERT ON legacy_history WHEN NEW.kind='logs'
		BEGIN UPDATE legacy_history SET severity='wrong' WHERE id=NEW.id; END;`)
	_, err := callRaw(t, s, "migration.apply", applyParams{PreviewID: preview.PreviewID, AcceptUpdates: true})
	assertFault(t, err, "MIGRATION_VERIFY")
	for _, table := range []string{"hosts", "preferences", "legacy_history", "legacy_batches", "legacy_map"} {
		if n := countRows(t, s.DB, table); n != 0 {
			t.Fatalf("%s committed %d rows", table, n)
		}
	}
	execOld(t, s.DB, `DROP TRIGGER fixture_change`)
	if result := runApply(t, s, preview, false, true); result.Imported["logs"] != 2 {
		t.Fatal("rollback state not retryable")
	}
}

func TestLargeLegacyHistoryPages(t *testing.T) {
	if testing.Short() {
		t.Skip("50,000-row fixture")
	}
	s, _ := testStore(t)
	const logs = 50_000
	path, _ := fullFixture(t, logs)
	preview := runPreview(t, s, path)
	if len(preview.Changes) > maxPreviewChanges || preview.Totals["logs"] != logs {
		t.Fatal("large preview did not report full counts")
	}
	runApply(t, s, preview, false, true)
	kinds := assertAllHistoryPages(t, s, logs+143)
	if kinds["logs"] != logs {
		t.Fatalf("logs imported %d/%d", kinds["logs"], logs)
	}
}

func TestClosedWALPreviewLeavesNoSidecars(t *testing.T) {
	s, _ := testStore(t)
	path, old := fullFixture(t, 2)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("fixture left a %s file: %v", suffix, err)
		}
	}
	runPreview(t, s, path)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("read-only preview left a %s file: %v", suffix, err)
		}
	}
}

func TestIdenticalBackupPathReusesHistoryAndKeepsOrigin(t *testing.T) {
	s, _ := testStore(t)
	original, old := fullFixture(t, 3)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	runApply(t, s, runPreview(t, s, original), false, true)
	copyPath := filepath.Join(t.TempDir(), "renamed-backup.db")
	if err := os.WriteFile(copyPath, fileBytes(t, original), 0600); err != nil {
		t.Fatal(err)
	}
	added := runApply(t, s, runPreview(t, s, copyPath), false, true)
	for table, n := range added.Imported {
		if n != 0 {
			t.Fatalf("renamed snapshot duplicated %s", table)
		}
	}
	if countRows(t, s.DB, "legacy_history") != 146 {
		t.Fatal("renamed backup duplicated history")
	}
	copyDB, err := sql.Open("sqlite", sqliteURI(copyPath, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copyDB.Close() })
	execOld(t, copyDB, `UPDATE hosts SET ip_address='192.0.2.30' WHERE id='h-a'`)
	preview := runPreview(t, s, copyPath)
	if !containsChange(preview, "hosts", "h-a", "update") {
		t.Fatal("known source lost its original baseline after update")
	}
	if res := runApply(t, s, preview, false, true); res.Imported["hosts"] != 1 || res.Imported["logs"] != 0 {
		t.Fatalf("alias update did not reuse unrelated rows: %#v", res)
	}
}

func TestIncompleteWALBundleIsNotRepairedInPlace(t *testing.T) {
	s, _ := testStore(t)
	path, _ := fullFixture(t, 2)
	copyPath := filepath.Join(t.TempDir(), "copied-without-shm.db")
	for _, suffix := range []string{"", "-wal"} {
		if err := os.WriteFile(copyPath+suffix, fileBytes(t, path+suffix), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := callRaw(t, s, "migration.preview", map[string]string{"token": copyPath})
	assertFault(t, err, "MIGRATION_SOURCE")
	if _, err := os.Stat(copyPath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("preview wrote missing SHM: %v", err)
	}
}

func TestMigrationBadSourceHasNoSideEffects(t *testing.T) {
	s, _ := testStore(t)
	p := filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(p, []byte(strings.Repeat("not sqlite", 20)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, code string }{{p, "MIGRATION_SOURCE"}, {"../not-authorized.db", "BAD_PARAMS"}, {filepath.Join(s.Dir, databaseName), "BAD_PARAMS"}} {
		t.Run(fmt.Sprintf("%s-%t", tc.code, filepath.IsAbs(tc.path)), func(t *testing.T) {
			_, err := callRaw(t, s, "migration.preview", map[string]string{"token": tc.path})
			assertFault(t, err, tc.code)
		})
	}
	if countRows(t, s.DB, "legacy_batches") != 0 {
		t.Fatal("failed preview wrote a batch")
	}
}
