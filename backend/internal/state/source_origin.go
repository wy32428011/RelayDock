package state

import (
	"context"
	"database/sql"
	"errors"

	"remoteinstaller.local/core/internal/rpc"
)

const pathAliasTable, pathAliasKey = "@origin", "@path"

// The legacy schema has no database UUID. A path (hashed, not exposed) is an
// origin, unless it has been explicitly used as an alias or is a unique exact
// match to a prior sanitized snapshot. Do not infer continuity from one or
// several shared row IDs when the snapshot differs.
func selectOrigin(ctx context.Context, tx *sql.Tx, snap *legacySnapshot) (string, error) {
	var origin string
	err := tx.QueryRowContext(ctx, `SELECT target_id FROM legacy_map WHERE source_id=? AND table_name=? AND source_key=?`,
		snap.pathID, pathAliasTable, pathAliasKey).Scan(&origin)
	if err == nil {
		return origin, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var known bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM legacy_batches WHERE source_id=?)`, snap.pathID).Scan(&known); err != nil {
		return "", err
	}
	if known {
		return snap.pathID, nil
	}
	var count int
	var candidate sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT count(DISTINCT source_id), min(source_id) FROM legacy_batches WHERE snapshot_hash=?`, snap.digest).Scan(&count, &candidate)
	if err != nil {
		return "", err
	}
	if count == 1 && candidate.Valid {
		return candidate.String, nil
	}
	return snap.pathID, nil
}

func historyID(origin, table, key string) string {
	return textDigest(origin + "|" + table + "|" + key)[:32]
}

func (s *legacySnapshot) setOrigin(origin string) {
	if origin == s.id {
		return
	}
	s.id = origin
	for _, table := range []string{"tasks", "logs", "install_history"} {
		rows := s.tables[table].rows
		for i := range rows {
			if rows[i].target != "" {
				rows[i].target = historyID(origin, table, rows[i].key)
			}
		}
	}
	s.warnings = append(s.warnings, "该路径沿用已知旧库来源，相同历史不会重复导入。")
}

func recordOriginAlias(ctx context.Context, tx *sql.Tx, snap *legacySnapshot) error {
	if snap.id == snap.pathID {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO legacy_map(source_id,table_name,source_key,target_id,source_hash,target_hash) VALUES(?,?,?,?, '', '')`,
		snap.pathID, pathAliasTable, pathAliasKey, snap.id)
	if err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT target_id FROM legacy_map WHERE source_id=? AND table_name=? AND source_key=?`,
		snap.pathID, pathAliasTable, pathAliasKey).Scan(&stored); err != nil {
		return err
	}
	if stored != snap.id {
		return rpc.Error("MIGRATION_STALE", "源库关联已变化，请重新预览")
	}
	return nil
}
