package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"time"

	"remoteinstaller.local/core/internal/rpc"
)

const maxPreviewChanges = 240
const previewLifetime = 15 * time.Minute

type migrationChange struct {
	Table string `json:"table"`
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

type migrationPreview struct {
	PreviewID        string            `json:"previewId"`
	Totals           map[string]int    `json:"totals"`
	NeedsCredentials int               `json:"needsCredentials"`
	Changes          []migrationChange `json:"changes"`
	Warnings         []string          `json:"warnings"`
}

type pendingPreview struct {
	id, fullPlan, hostPlan string
	created                time.Time
	snapshot               *legacySnapshot
}

type applyParams struct {
	PreviewID     string `json:"previewId"`
	HostsOnly     bool   `json:"hostsOnly"`
	AcceptUpdates bool   `json:"acceptUpdates"`
}

type applyResult struct {
	Imported map[string]int `json:"imported"`
	Skipped  map[string]int `json:"skipped"`
	Message  string         `json:"message"`
}

type lineage struct{ target, sourceHash, targetHash string }

// targetView is the destination state against which this particular source is
// compared. Its maps are scoped to one SQLite read or write transaction.
type targetView struct {
	hosts    map[string]hostRow
	custom   map[string]customRow
	byName   map[string]string
	settings map[string]string
	history  map[string]string
	lineages map[string]map[string]lineage
}

type plannedRow struct {
	row                              legacyRow
	kind, label, current, binding    string
	prev                             lineage
	resetCredential, needsCredential bool
}

type legacyPlan struct {
	targets             targetView
	rows                map[string][]plannedRow
	missing             []migrationChange
	fullHash, hostsHash string
}

func (s *Store) previewLegacy(ctx context.Context, path string) (migrationPreview, error) {
	if err := acquire(ctx, s.migration); err != nil {
		return migrationPreview{}, err
	}
	defer func() { <-s.migration }()
	s.preview = nil
	snap, err := s.loadLegacy(ctx, path)
	if err != nil {
		return migrationPreview{}, err
	}
	if err := acquire(ctx, s.secretGate); err != nil {
		return migrationPreview{}, err
	}
	defer func() { <-s.secretGate }()
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return migrationPreview{}, err
	}
	defer tx.Rollback()
	origin, err := selectOrigin(ctx, tx, snap)
	if err != nil {
		return migrationPreview{}, err
	}
	snap.setOrigin(origin)
	p, err := s.planLegacy(ctx, tx, snap)
	if err != nil {
		return migrationPreview{}, err
	}
	id := rpc.ID()
	out := p.presentation(id, snap)
	s.preview = &pendingPreview{id: id, fullPlan: p.fullHash, hostPlan: p.hostsHash, created: time.Now(), snapshot: snap}
	return out, nil
}

func readTarget(ctx context.Context, q queryer, source string) (targetView, error) {
	var out targetView
	var err error
	out.hosts, err = queryMap(ctx, q, `SELECT `+hostColumns+` FROM hosts`, func(r scanner) (string, hostRow, error) {
		h, e := scanHost(r)
		return h.ID, h, e
	})
	if err != nil {
		return out, err
	}
	out.custom, err = queryMap(ctx, q, `SELECT `+customColumns+` FROM custom_apps`, func(r scanner) (string, customRow, error) {
		a, e := scanCustom(r)
		return a.ID, a, e
	})
	if err != nil {
		return out, err
	}
	out.byName = make(map[string]string, len(out.custom))
	for id, app := range out.custom {
		out.byName[nameKey(app.Name)] = id
	}
	out.settings, err = loadKV(ctx, q, `SELECT key,value FROM preferences`)
	if err != nil {
		return out, err
	}
	out.history, err = queryMap(ctx, q, `SELECT id,body_hash FROM legacy_history WHERE source_id=?`, func(r scanner) (string, string, error) {
		var id, v string
		err := r.Scan(&id, &v)
		return id, v, err
	}, source)
	if err != nil {
		return out, err
	}
	out.lineages = make(map[string]map[string]lineage)
	rows, err := q.QueryContext(ctx, `SELECT table_name,source_key,target_id,source_hash,target_hash FROM legacy_map WHERE source_id=?`, source)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var table, key string
		var l lineage
		if err := rows.Scan(&table, &key, &l.target, &l.sourceHash, &l.targetHash); err != nil {
			return out, err
		}
		if out.lineages[table] == nil {
			out.lineages[table] = make(map[string]lineage)
		}
		out.lineages[table][key] = l
	}
	return out, rows.Err()
}

func queryMap[V any](ctx context.Context, q queryer, statement string, row func(scanner) (string, V, error), args ...any) (map[string]V, error) {
	rows, err := q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]V)
	for rows.Next() {
		key, value, err := row(rows)
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func valueDigest(v any) string {
	b, _ := json.Marshal(v) // only fixed, JSON-compatible metadata types enter the plan
	return textDigest(string(b))
}

func hostDigest(h Host) string {
	h.Fingerprint, h.LastSeen, h.CredentialReady = "", "", false
	return valueDigest(h)
}

func (s *Store) planLegacy(ctx context.Context, q queryer, source *legacySnapshot) (legacyPlan, error) {
	targets, err := readTarget(ctx, q, source.id)
	if err != nil {
		return legacyPlan{}, err
	}
	p := legacyPlan{targets: targets, rows: make(map[string][]plannedRow)}
	seeds := make(map[string]string)
	for _, seed := range defaultCustomApps() {
		seeds[seed.ID] = valueDigest(seed)
	}
	appIDs, appNames := make(map[string]int), make(map[string]int)
	for _, row := range source.tables["custom_apps"].rows {
		if app, ok := row.value.(customRow); ok {
			appIDs[app.ID]++
			appNames[nameKey(app.Name)]++
		}
	}
	all := sha256.New()
	for _, table := range legacyTables {
		if err := ctx.Err(); err != nil {
			return legacyPlan{}, err
		}
		t := source.tables[table]
		_, _ = fmt.Fprintln(all, table, t.total, t.rejected)
		seen := make(map[string]bool, len(t.rows))
		for _, row := range t.rows {
			if err := ctx.Err(); err != nil {
				return legacyPlan{}, err
			}
			seen[row.key] = true
			prev := targets.lineages[table][row.key]
			entry := plannedRow{row: row, label: row.label, prev: prev}
			var forcedConflict string
			var untouchedSeed bool
			switch v := row.value.(type) {
			case Host:
				current, exists := targets.hosts[row.target]
				if exists {
					entry.current = hostDigest(current.Host)
					entry.resetCredential = !sameEndpoint(current.Host, v)
					if entry.resetCredential {
						_, session := s.session[row.target]
						entry.binding = valueDigest([]any{current.Fingerprint, current.ref, session})
					}
				}
				_, session := s.session[row.target]
				entry.needsCredential = !exists || !validRef(current.ref) && !session
			case customRow:
				if current, exists := targets.custom[row.target]; exists {
					entry.current = valueDigest(current)
					untouchedSeed = seeds[row.target] == entry.current
				}
				if owner := targets.byName[nameKey(v.Name)]; owner != "" && owner != row.target {
					forcedConflict = "自定义应用与新版中另一个稳定键同名"
				}
				if appIDs[v.ID] > 1 || appNames[nameKey(v.Name)] > 1 {
					forcedConflict = "旧版自定义应用含重复稳定键或重名"
				}
			case string:
				if val, exists := targets.settings[row.target]; exists {
					entry.current = valueDigest(val)
				}
			case legacyHistory:
				entry.current = targets.history[row.target]
			}
			switch {
			case row.target == "":
				entry.kind = "missing" // Invalid, unsupported or deliberately omitted source row.
			case prev.target != "" && prev.target != row.target:
				entry.kind, entry.label = "conflict", "稳定键已变化，不会自动重命名新版记录"
			case forcedConflict != "":
				entry.kind, entry.label = "conflict", forcedConflict
			case prev.sourceHash == row.hash && entry.current != "":
				entry.kind = "reuse" // Do not reset the baseline over local edits.
			case prev.sourceHash == row.hash:
				entry.kind, entry.label = "missing", "新版已删除此记录，不会自动恢复"
			case entry.current == row.hash:
				entry.kind = "reuse" // Existing independently-created exact match.
			case prev.target != "" && entry.current != prev.targetHash:
				entry.kind, entry.label = "conflict", "旧版和新版均已修改（或新版已删除），不会覆盖"
			case prev.target != "" || untouchedSeed:
				entry.kind = "update"
			case entry.current != "":
				entry.kind, entry.label = "conflict", "此稳定键在新版已存在，内容不同，不会覆盖"
			default:
				entry.kind = "add"
			}
			if entry.kind == "update" && entry.resetCredential {
				old, next := targets.hosts[row.target], row.value.(Host)
				entry.label = fmt.Sprintf("%s：连接信息变化（%s:%d → %s:%d），需重新核对指纹并录入凭据", next.Name, old.Address, old.Port, next.Address, next.Port)
				entry.needsCredential = true
			}
			if entry.kind == "missing" || entry.kind == "conflict" {
				entry.needsCredential = false
			}
			writePlanHash(all, entry)
			p.rows[table] = append(p.rows[table], entry)
		}
		if t.usable {
			for _, key := range sortedKeys(targets.lineages[table]) {
				if seen[key] {
					continue
				}
				p.missing = append(p.missing, migrationChange{Table: table, Key: key, Kind: "missing", Label: "旧版已删除；新版保留，不会自动停用或删除"})
				_, _ = fmt.Fprintln(all, "source-absent", key)
			}
		}
		if table == "hosts" {
			p.hostsHash = hex.EncodeToString(all.Sum(nil))
		}
	}
	p.fullHash = hex.EncodeToString(all.Sum(nil))
	return p, nil
}

func writePlanHash(h hash.Hash, r plannedRow) {
	_, _ = fmt.Fprintf(h, "%q %q %q %q %q %q %q %q\n", r.row.key, r.row.target, r.kind, r.current, r.binding, r.prev.target, r.prev.sourceHash, r.prev.targetHash)
}

func (p legacyPlan) presentation(id string, snap *legacySnapshot) migrationPreview {
	out := migrationPreview{PreviewID: id, Totals: make(map[string]int), Changes: []migrationChange{}, Warnings: append([]string{}, snap.warnings...)}
	for _, table := range legacyTables {
		out.Totals[table] = snap.tables[table].total
	}
	for _, r := range p.rows["hosts"] {
		if r.needsCredential {
			out.NeedsCredentials++
		}
	}
	shownTotal, conflicts := 0, 0
	appendChange := func(c migrationChange) {
		shownTotal++
		if len(out.Changes) < maxPreviewChanges {
			out.Changes = append(out.Changes, c)
		}
	}
	for _, kind := range []string{"conflict", "update", "missing", "add", "reuse"} {
		if kind == "missing" {
			for _, change := range p.missing {
				appendChange(change)
			}
		}
		for _, table := range legacyTables {
			for _, r := range p.rows[table] {
				if r.kind != kind {
					continue
				}
				if kind == "conflict" {
					conflicts++
				}
				appendChange(migrationChange{Table: table, Key: r.row.key, Kind: r.kind, Label: r.label})
			}
		}
	}
	if conflicts != 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("存在 %d 处未决冲突；相应导入范围不会写入，需先人工处理并重新预览。", conflicts))
	}
	if len(p.missing) != 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("旧源缺少 %d 条以往已导入记录；新版保留这些记录。", len(p.missing)))
	}
	if shownTotal > len(out.Changes) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("预览只展示首 %d 条变化（共 %d 条）；导入和对账使用完整快照。", len(out.Changes), shownTotal))
	}
	out.Warnings = append(out.Warnings, "应用使用预览时的快照。若旧库或新版记录有改动，请重新预览。")
	return out
}

func (s *Store) applyLegacy(ctx context.Context, input applyParams) (applyResult, error) {
	if !validRef(input.PreviewID) {
		return applyResult{}, rpc.Error("BAD_PARAMS", "迁移预览 ID 无效")
	}
	if err := acquire(ctx, s.migration); err != nil {
		return applyResult{}, err
	}
	defer func() { <-s.migration }()
	if result, ok, err := s.replayBatch(ctx, input); ok || err != nil {
		return result, err
	}
	pending := s.preview
	if pending == nil || pending.id != input.PreviewID || time.Since(pending.created) > previewLifetime {
		return applyResult{}, rpc.Error("MIGRATION_PREVIEW_REQUIRED", "迁移预览已失效，请重新选择旧库并预览")
	}
	if err := acquire(ctx, s.secretGate); err != nil {
		return applyResult{}, err
	}
	defer func() { <-s.secretGate }()
	defer s.collectSecrets(ctx)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return applyResult{}, err
	}
	defer tx.Rollback()
	// Acquire the SQLite write lock before classifying rows. The marker,
	// selected rows, mappings and final result all commit or all roll back.
	_, err = tx.ExecContext(ctx, `INSERT INTO legacy_batches(preview_id,source_id,snapshot_hash,hosts_only,accept_updates,applied_at,result) VALUES(?,?,?,?,?,?, '')`,
		input.PreviewID, pending.snapshot.id, pending.snapshot.digest, input.HostsOnly, input.AcceptUpdates, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return applyResult{}, err
	}
	p, err := s.planLegacy(ctx, tx, pending.snapshot)
	if err != nil {
		return applyResult{}, err
	}
	if input.HostsOnly && pending.hostPlan != p.hostsHash || !input.HostsOnly && pending.fullPlan != p.fullHash {
		return applyResult{}, rpc.Error("MIGRATION_STALE", "新版数据已变化；没有写入，请重新预览并复核")
	}
	tables := legacyTables
	if input.HostsOnly {
		tables = tables[:1]
	}
	for _, table := range tables {
		for _, row := range p.rows[table] {
			if row.kind == "conflict" {
				return applyResult{}, rpc.Error("MIGRATION_CONFLICT", "所选范围内有未决冲突；未写入数据。请处理冲突并重新预览")
			}
		}
	}
	result, reset, err := writeMigration(ctx, tx, p, pending.snapshot, input, tables)
	if err != nil {
		return applyResult{}, err
	}
	if err := recordOriginAlias(ctx, tx, pending.snapshot); err != nil {
		return applyResult{}, err
	}
	if err := verifyMigration(ctx, tx, input.PreviewID, result); err != nil {
		return applyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return applyResult{}, err
	}
	for _, id := range reset {
		delete(s.session, id)
	}
	s.preview = nil
	return result, nil
}

func (s *Store) replayBatch(ctx context.Context, p applyParams) (applyResult, bool, error) {
	var hostsOnly, acceptUpdates bool
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT hosts_only,accept_updates,result FROM legacy_batches WHERE preview_id=?`, p.PreviewID).Scan(&hostsOnly, &acceptUpdates, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return applyResult{}, false, nil
	}
	if err != nil {
		return applyResult{}, false, err
	}
	if p.HostsOnly != hostsOnly || p.AcceptUpdates != acceptUpdates {
		return applyResult{}, true, rpc.Error("MIGRATION_PREVIEW_REQUIRED", "该预览已用其他选项应用，请重新预览后选择导入范围")
	}
	var result applyResult
	err = json.Unmarshal([]byte(raw), &result)
	return result, true, err
}

func writeMigration(ctx context.Context, tx *sql.Tx, p legacyPlan, snap *legacySnapshot, input applyParams, tables []string) (applyResult, []string, error) {
	result := applyResult{Imported: make(map[string]int), Skipped: make(map[string]int)}
	mappings, err := tx.PrepareContext(ctx, `INSERT INTO legacy_map(source_id,table_name,source_key,target_id,source_hash,target_hash) VALUES(?,?,?,?,?,?)
		ON CONFLICT(source_id,table_name,source_key) DO UPDATE SET target_id=excluded.target_id,source_hash=excluded.source_hash,target_hash=excluded.target_hash`)
	if err != nil {
		return result, nil, err
	}
	defer mappings.Close()
	history, err := tx.PrepareContext(ctx, historyUpsert)
	if err != nil {
		return result, nil, err
	}
	defer history.Close()
	var resets []string
	var imported, skipped, delayed, reused int
	for _, table := range tables {
		result.Imported[table], result.Skipped[table] = 0, snap.tables[table].rejected
		var lastWritten *plannedRow
		for _, r := range p.rows[table] {
			var persistMapping bool
			switch r.kind {
			case "reuse":
				reused++
				result.Skipped[table]++
				// A previously imported row with local edits must not have its
				// target baseline replaced by a reuse from an unchanged source.
				persistMapping = r.prev.target == "" || (r.prev.sourceHash != r.row.hash && r.current == r.row.hash)
			case "missing":
				result.Skipped[table]++
			case "add", "update":
				if r.kind == "update" && !input.AcceptUpdates {
					delayed++
					result.Skipped[table]++
					continue
				}
				switch v := r.row.value.(type) {
				case Host:
					h := p.targets.hosts[v.ID]
					if r.resetCredential {
						if err := swapRefs(ctx, tx, h.ref, ""); err != nil {
							return result, nil, err
						}
						h = hostRow{}
						resets = append(resets, v.ID)
					}
					v.Fingerprint, v.LastSeen = h.Fingerprint, h.LastSeen
					if err := writeHost(ctx, tx, hostRow{Host: v, ref: h.ref}); err != nil {
						return result, nil, err
					}
				case customRow:
					if err := writeCustom(ctx, tx, v); err != nil {
						return result, nil, err
					}
				case string:
					if err := writePreference(ctx, tx, r.row.target, v); err != nil {
						return result, nil, err
					}
				case legacyHistory:
					if err := writeHistory(ctx, history, snap.id, table, r.row, v); err != nil {
						return result, nil, err
					}
				default:
					return result, nil, errors.New("unknown migration row type")
				}
				result.Imported[table]++
				persistMapping = true
			default:
				return result, nil, errors.New("unresolved migration row")
			}
			if persistMapping {
				if err := requireOne(mappings.ExecContext(ctx, snap.id, table, r.row.key, r.row.target, r.row.hash, r.row.hash)); err != nil {
					return result, nil, err
				}
				if r.kind == "add" || r.kind == "update" {
					lastWritten = &r
					if table == "hosts" || result.Imported[table] == 1 || result.Imported[table]%sourcePageSize == 0 {
						if err := verifyLegacyRow(ctx, tx, p.targets, snap.id, table, r); err != nil {
							return result, nil, err
						}
					}
				}
			}
		}
		if lastWritten != nil && table != "hosts" {
			if err := verifyLegacyRow(ctx, tx, p.targets, snap.id, table, *lastWritten); err != nil {
				return result, nil, err
			}
		}
		if result.Imported[table]+result.Skipped[table] != snap.tables[table].total {
			return result, nil, rpc.Error("MIGRATION_VERIFY", "逐表数量对账失败；未导入任何数据")
		}
		imported += result.Imported[table]
		skipped += result.Skipped[table]
	}
	result.Message = fmt.Sprintf("迁移已提交：新增或更新 %d 条，跳过 %d 条（其中原样复用 %d 条、未授权更新 %d 条）。旧凭据不迁移，历史不会重新执行。", imported, skipped, reused, delayed)
	return result, resets, nil
}

func verifyLegacyRow(ctx context.Context, tx *sql.Tx, view targetView, source, table string, r plannedRow) error {
	bad := rpc.Error("MIGRATION_VERIFY", "导入行的引用或字段未通过核验；未写入数据")
	id := r.row.target
	var actual string
	var err error
	switch r.row.value.(type) {
	case Host:
		var host hostRow
		host, err = getHostRow(ctx, tx, id)
		if err == nil {
			actual = hostDigest(host.Host)
			prior := view.hosts[id]
			if r.resetCredential {
				prior = hostRow{}
			}
			if host.ref != prior.ref || host.Fingerprint != prior.Fingerprint || host.LastSeen != prior.LastSeen {
				return bad
			}
		}
	case customRow:
		var app customRow
		app, err = scanCustom(tx.QueryRowContext(ctx, `SELECT `+customColumns+` FROM custom_apps WHERE id=?`, id))
		actual = valueDigest(app)
	case string:
		var value string
		err = tx.QueryRowContext(ctx, `SELECT value FROM preferences WHERE key=?`, id).Scan(&value)
		actual = valueDigest(value)
	case legacyHistory:
		var kind, sourceID, bodyHash string
		var history legacyHistory
		_, sourceID, kind, bodyHash, history, err = scanLegacyHistory(tx.QueryRowContext(ctx, `SELECT `+historyColumns+` FROM legacy_history WHERE id=?`, id))
		if err == nil && (source != sourceID || table != kind || bodyHash != r.row.hash) {
			return bad
		}
		actual = valueDigest(history)
	}
	if errors.Is(err, sql.ErrNoRows) || err == nil && actual != r.row.hash {
		return bad
	}
	if err != nil {
		return err
	}
	var mapping lineage
	err = tx.QueryRowContext(ctx, `SELECT target_id,source_hash,target_hash FROM legacy_map WHERE source_id=? AND table_name=? AND source_key=?`, source, table, r.row.key).
		Scan(&mapping.target, &mapping.sourceHash, &mapping.targetHash)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (mapping.target != id || mapping.sourceHash != actual || mapping.targetHash != actual) {
		return bad
	}
	return err
}

func verifyMigration(ctx context.Context, tx *sql.Tx, previewID string, result applyResult) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	if rows.Next() {
		_ = rows.Close()
		return rpc.Error("MIGRATION_VERIFY", "新版数据库引用检查未通过；未导入任何数据")
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE legacy_batches SET result=? WHERE preview_id=?`, string(raw), previewID)
	return err
}
