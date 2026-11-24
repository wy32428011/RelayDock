package operations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"remoteinstaller.local/core/internal/rpc"
)

const taskColumns = `id, host_id, app_id, version, kind, state, stage, percent, message, created_at, updated_at, seq, result, batch_id`

const (
	maxLogRows = 5000
	logPage    = 160
)

func initStorage(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS operation_tasks (
			id TEXT PRIMARY KEY, request_hash TEXT NOT NULL UNIQUE,
			host_id TEXT NOT NULL, app_id TEXT NOT NULL, version TEXT NOT NULL, kind TEXT NOT NULL,
			state TEXT NOT NULL, stage TEXT NOT NULL, percent REAL NOT NULL, message TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL, seq INTEGER NOT NULL,
			result TEXT NOT NULL DEFAULT '', batch_id TEXT NOT NULL DEFAULT '', log_floor INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS operation_tasks_host ON operation_tasks(host_id, app_id);
		CREATE INDEX IF NOT EXISTS operation_tasks_batch ON operation_tasks(batch_id);
		CREATE TABLE IF NOT EXISTS operation_logs (
			task_id TEXT NOT NULL REFERENCES operation_tasks(id) ON DELETE CASCADE,
			seq INTEGER NOT NULL, text TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY (task_id, seq)
		);`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE operation_tasks SET state = 'interrupted', stage = 'interrupted',
		message = ?, updated_at = ?, seq = seq + 1
		WHERE state IN ('queued', 'preflight', 'running', 'verifying', 'cancelling')`,
		"核心已重启，任务已中断，请只读复查远端状态", now())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func requestHash(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func insertTasks(ctx context.Context, db *sql.DB, tasks []*job, ops []operation) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, j := range tasks {
		t := j.task
		_, err := tx.ExecContext(ctx, `INSERT INTO operation_tasks
			(id, request_hash, host_id, app_id, version, kind, state, stage, percent, message, created_at, updated_at, seq, batch_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, requestHash(ops[i].RequestKey), t.HostID, t.AppID, t.Version, t.Kind, t.State, t.Stage,
			t.Percent, t.Message, t.CreatedAt, t.UpdatedAt, t.Seq, t.BatchID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var result string
	err := row.Scan(&t.ID, &t.HostID, &t.AppID, &t.Version, &t.Kind, &t.State, &t.Stage, &t.Percent, &t.Message,
		&t.CreatedAt, &t.UpdatedAt, &t.Seq, &result, &t.BatchID)
	if err == nil && result != "" {
		err = json.Unmarshal([]byte(result), &t.Result)
	}
	return t, err
}

func taskByID(ctx context.Context, db *sql.DB, id string) (Task, error) {
	t, err := scanTask(db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM operation_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, rpc.Error("TASK_NOT_FOUND", "没有找到该任务")
	}
	return t, err
}

func (s *Service) previousSubmission(ctx context.Context, ops []operation, batch bool) (submission, error) {
	r := submission{TaskIDs: make([]string, 0, len(ops))}
	for _, op := range ops {
		var id, hostID, appID, kind, version, batchID string
		err := s.db.QueryRowContext(ctx, `SELECT id, host_id, app_id, kind, version, batch_id FROM operation_tasks WHERE request_hash = ?`,
			requestHash(op.RequestKey)).Scan(&id, &hostID, &appID, &kind, &version, &batchID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return submission{}, err
		}
		if hostID != op.HostID || appID != op.AppID || kind != op.Kind || op.Version != "" && version != op.Version {
			return submission{}, rpc.Error("REQUEST_CONFLICT", "请求标识已用于另一项操作")
		}
		if batch && (batchID == "" || r.BatchID != "" && r.BatchID != batchID) {
			return submission{}, rpc.Error("REQUEST_CONFLICT", "请求标识属于其他提交")
		}
		r.BatchID = batchID
		r.TaskIDs = append(r.TaskIDs, id)
	}
	if len(r.TaskIDs) == 0 || !batch {
		return r, nil
	}
	if len(r.TaskIDs) != len(ops) {
		return submission{}, rpc.Error("REQUEST_CONFLICT", "批次包含已提交的任务，请以新请求标识提交新的批次")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operation_tasks WHERE batch_id = ? ORDER BY rowid`, r.BatchID)
	if err != nil {
		return submission{}, err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return submission{}, err
		}
		if i >= len(r.TaskIDs) || r.TaskIDs[i] != id {
			return submission{}, rpc.Error("REQUEST_CONFLICT", "批次顺序或任务范围已改变")
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return submission{}, err
	}
	if i != len(ops) {
		return submission{}, rpc.Error("REQUEST_CONFLICT", "批次任务范围已改变")
	}
	return r, nil
}

func (s *Service) list(ctx context.Context, p listParams) ([]Task, error) {
	if p.Limit == 0 {
		p.Limit = 50
	}
	if p.Limit < 0 || p.Limit > 200 || p.Offset < 0 || len(p.HostID) > maxID || len(p.AppID) > maxID ||
		p.Kind != "" && !validKind(p.Kind) || p.State != "" && !validState(p.State) {
		return nil, rpc.Error("BAD_PARAMS", "任务筛选或分页参数无效")
	}
	filters := []struct{ column, value string }{
		{"host_id", p.HostID}, {"app_id", p.AppID}, {"kind", p.Kind}, {"state", p.State},
	}
	var query strings.Builder
	query.WriteString(`SELECT ` + taskColumns + ` FROM operation_tasks WHERE 1=1`)
	args := make([]any, 0, len(filters)+2)
	for _, f := range filters {
		if f.value != "" {
			query.WriteString(` AND ` + f.column + ` = ?`)
			args = append(args, f.value)
		}
	}
	query.WriteString(` ORDER BY rowid DESC LIMIT ? OFFSET ?`)
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) get(ctx context.Context, p taskQuery) (taskDetail, error) {
	if !validID(p.TaskID) || p.AfterSeq < 0 {
		return taskDetail{}, rpc.Error("BAD_PARAMS", "任务标识或日志游标无效")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return taskDetail{}, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM operation_tasks WHERE id = ?`, p.TaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return taskDetail{}, rpc.Error("TASK_NOT_FOUND", "没有找到该任务")
	}
	if err != nil {
		return taskDetail{}, err
	}
	var floor int64
	if err := tx.QueryRowContext(ctx, `SELECT log_floor FROM operation_tasks WHERE id = ?`, p.TaskID).Scan(&floor); err != nil {
		return taskDetail{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq, text, at FROM operation_logs
		WHERE task_id = ? AND seq > ? ORDER BY seq DESC LIMIT ?`, p.TaskID, p.AfterSeq, logPage+1)
	if err != nil {
		return taskDetail{}, err
	}
	defer rows.Close()
	detail := taskDetail{Task: t, Logs: make([]TaskLog, 0), Truncated: p.AfterSeq < floor}
	for rows.Next() {
		var log TaskLog
		if err := rows.Scan(&log.Seq, &log.Text, &log.At); err != nil {
			return taskDetail{}, err
		}
		if len(detail.Logs) == logPage {
			detail.Truncated = true
			break
		}
		detail.Logs = append(detail.Logs, log)
	}
	for i, j := 0, len(detail.Logs)-1; i < j; i, j = i+1, j-1 {
		detail.Logs[i], detail.Logs[j] = detail.Logs[j], detail.Logs[i]
	}
	return detail, rows.Err()
}

func persistTask(db *sql.DB, t Task) error {
	var result string
	if t.Result != nil {
		b, err := json.Marshal(t.Result)
		if err != nil {
			return err
		}
		result = string(b)
	}
	_, err := db.Exec(`UPDATE operation_tasks SET state = ?, stage = ?, percent = ?, message = ?, updated_at = ?, seq = ?, result = ? WHERE id = ?`,
		t.State, t.Stage, t.Percent, t.Message, t.UpdatedAt, t.Seq, result, t.ID)
	return err
}

func persistLogs(db *sql.DB, task Task, logs []TaskLog) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO operation_logs(task_id, seq, text, at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, log := range logs {
		if _, err := stmt.Exec(task.ID, log.Seq, log.Text, log.At); err != nil {
			return err
		}
	}
	var floor int64
	err = tx.QueryRow(`SELECT seq FROM operation_logs WHERE task_id = ? ORDER BY seq DESC LIMIT 1 OFFSET ?`, task.ID, maxLogRows).Scan(&floor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if floor > 0 {
		if _, err := tx.Exec(`DELETE FROM operation_logs WHERE task_id = ? AND seq <= ?`, task.ID, floor); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE operation_tasks SET seq = ?, updated_at = ?, log_floor = MAX(log_floor, ?) WHERE id = ?`,
		task.Seq, task.UpdatedAt, floor, task.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) clearLogs(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := taskByID(ctx, s.db, id)
	if err != nil {
		return err
	}
	if active(t.State) {
		return rpc.Error("TASK_ACTIVE", "请等任务结束后清理日志")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM operation_logs WHERE task_id = ?`, id); err != nil {
		return err
	}
	t.Seq++
	t.UpdatedAt = now()
	if _, err := tx.ExecContext(ctx, `UPDATE operation_tasks SET log_floor = 0, seq = ?, updated_at = ? WHERE id = ?`, t.Seq, t.UpdatedAt, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.event("task.state", t, t)
	return nil
}
