package state

import (
	"context"
	"database/sql"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

type historyParams struct {
	Limit  *int `json:"limit"`
	Offset int  `json:"offset"`
}

type legacyRecord struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Summary string `json:"summary"`
	At      string `json:"at"`
	Detail  string `json:"detail,omitempty"`
}

const historyColumns = `id, source_id, kind, host_key, task_key, app_id, app_version, operation,
	state, stage, progress, severity, started_at, ended_at, at, orphan, omitted, body_hash`

const historyUpsert = `INSERT INTO legacy_history (` + historyColumns + `) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET host_key=excluded.host_key,task_key=excluded.task_key,app_id=excluded.app_id,
	app_version=excluded.app_version,operation=excluded.operation,state=excluded.state,stage=excluded.stage,
	progress=excluded.progress,severity=excluded.severity,started_at=excluded.started_at,ended_at=excluded.ended_at,
	at=excluded.at,orphan=excluded.orphan,omitted=excluded.omitted,body_hash=excluded.body_hash
	WHERE legacy_history.source_id=excluded.source_id AND legacy_history.kind=excluded.kind`

func writeHistory(ctx context.Context, stmt *sql.Stmt, source, table string, row legacyRow, v legacyHistory) error {
	result, err := stmt.ExecContext(ctx, row.target, source, table, v.Host, v.Task, v.App, v.Version, v.Operation,
		v.State, v.Stage, v.Progress, v.Severity, v.Start, v.End, v.At, v.Orphan, v.Omitted, row.hash)
	return requireOne(result, err)
}

func scanLegacyHistory(row scanner) (id, sourceID, kind, hash string, v legacyHistory, err error) {
	err = row.Scan(&id, &sourceID, &kind, &v.Host, &v.Task, &v.App, &v.Version, &v.Operation, &v.State,
		&v.Stage, &v.Progress, &v.Severity, &v.Start, &v.End, &v.At, &v.Orphan, &v.Omitted, &hash)
	return
}

func (s *Store) readHistory(ctx context.Context, p historyParams) ([]legacyRecord, error) {
	limit := 100
	if p.Limit != nil {
		limit = *p.Limit
	}
	if limit <= 0 || p.Offset < 0 {
		return nil, rpc.Error("BAD_PARAMS", "分页范围无效")
	}
	limit = min(limit, 250)
	rows, err := s.DB.QueryContext(ctx, `SELECT `+historyColumns+` FROM legacy_history ORDER BY at DESC,kind,id LIMIT ? OFFSET ?`, limit, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]legacyRecord, 0, limit)
	for rows.Next() {
		id, _, kind, _, value, err := scanLegacyHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value.presentation(kind, id))
	}
	return out, rows.Err()
}

func (v legacyHistory) presentation(table, id string) legacyRecord {
	p := legacyRecord{Kind: table, ID: id, At: v.At}
	if table == "logs" {
		p.Summary = "旧日志 · " + translated(v.Severity)
	} else {
		label := v.App
		if label == "" {
			label = "未知应用"
		}
		if v.Version != "" {
			label += " " + v.Version
		}
		p.Summary = label + " · " + translated(v.Operation) + " · " + translated(v.State)
	}
	var detail []string
	if v.Host != "" {
		detail = append(detail, "主机 ID："+v.Host)
	}
	if v.Task != "" {
		detail = append(detail, "旧任务 ID："+v.Task)
	}
	if v.Stage != "" {
		detail = append(detail, "阶段："+translated(v.Stage))
	}
	if v.Progress != "" {
		detail = append(detail, "进度："+v.Progress+"%")
	}
	if v.End != "" {
		detail = append(detail, "结束："+v.End)
	}
	if v.Orphan {
		detail = append(detail, "关联缺失（孤儿记录）")
	}
	if v.Omitted {
		detail = append(detail, "原正文或错误详情已省略，可能包含秘密")
	}
	p.Detail = strings.Join(detail, "；")
	return p
}

func translated(s string) string {
	if label, ok := historyLabels[s]; ok {
		return label
	}
	return "未知"
}

var historyLabels = map[string]string{
	"install": "安装", "uninstall": "卸载", "interrupted": "中断／待复查", "succeeded": "成功", "failed": "失败", "cancelled": "已取消",
	"info": "信息", "success": "成功", "warning": "警告", "error": "错误", "preparing": "准备", "connecting": "连接", "uploading": "上传",
	"extracting": "解压", "configuring": "写入配置", "installing": "安装", "starting": "启动", "verifying": "复查", "completed": "完成",
}
