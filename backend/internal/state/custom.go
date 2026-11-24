package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

type CustomApp struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Kind              string `json:"kind"`
	Description       string `json:"description"`
	Directory         string `json:"directory"`
	FrontendDirectory string `json:"frontendDirectory"`
	ConfigPath        string `json:"configPath"`
	StartCommand      string `json:"startCommand"`
	StopCommand       string `json:"stopCommand"`
	StatusCommand     string `json:"statusCommand"`
	LogsPath          string `json:"logsPath"`
	Enabled           bool   `json:"enabled"`
}

type customRow struct {
	CustomApp
	Sort int
}

const customColumns = `id, name, kind, description, directory, frontend_directory, config_path,
	start_command, stop_command, status_command, logs_path, enabled, sort_order`

func defaultCustomApps() []customRow {
	// Seed stable identities and locations, not implicit remote commands.
	return []customRow{
		{CustomApp: CustomApp{ID: "SUPPORT", Name: "Support", Kind: "SUPPORT", Enabled: true,
			Description: "Support 运维支持平台", Directory: "/opt/zeus-support", FrontendDirectory: "/var/www/zeus-support",
			ConfigPath: "/opt/zeus-support/conf/application-prod.properties", LogsPath: "/opt/zeus-support/log"}, Sort: 10},
		{CustomApp: CustomApp{ID: "XXL_JOB", Name: "XXL-JOB", Kind: "SUPPORT", Enabled: true,
			Description: "XXL-JOB 分布式任务调度中心", Directory: "/opt/xxl-job", FrontendDirectory: "/var/www/xxl-job",
			ConfigPath: "/opt/xxl-job/conf/application-prod.properties", LogsPath: "/opt/xxl-job/log"}, Sort: 20},
	}
}

func nameKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validCustom(a CustomApp) bool {
	if !validID(a.ID) || strings.TrimSpace(a.Name) == "" || !validText(a.Name, 256, false) ||
		(a.Kind != "SUPPORT" && a.Kind != "GENERIC" && a.Kind != "CUSTOM") || !validText(a.Description, 2048, true) {
		return false
	}
	for _, path := range []string{a.Directory, a.FrontendDirectory, a.ConfigPath, a.LogsPath} {
		if !validText(path, 2048, false) {
			return false
		}
	}
	for _, cmd := range []string{a.StartCommand, a.StopCommand, a.StatusCommand} {
		if !validText(cmd, 8192, true) {
			return false
		}
	}
	return true
}

func scanCustom(row scanner) (customRow, error) {
	var a customRow
	err := row.Scan(&a.ID, &a.Name, &a.Kind, &a.Description, &a.Directory, &a.FrontendDirectory, &a.ConfigPath,
		&a.StartCommand, &a.StopCommand, &a.StatusCommand, &a.LogsPath, &a.Enabled, &a.Sort)
	return a, err
}

func (s *Store) Custom(ctx context.Context, id string) (CustomApp, error) {
	a, err := scanCustom(s.DB.QueryRowContext(ctx, `SELECT `+customColumns+` FROM custom_apps WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CustomApp{}, rpc.Error("CUSTOM_NOT_FOUND", "此自定义应用不存在或已删除")
	}
	return a.CustomApp, err
}

func (s *Store) listCustom(ctx context.Context) ([]CustomApp, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+customColumns+` FROM custom_apps ORDER BY sort_order, name_key, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomApp{}
	for rows.Next() {
		a, err := scanCustom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a.CustomApp)
	}
	return out, rows.Err()
}

func writeCustom(ctx context.Context, tx *sql.Tx, a customRow) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO custom_apps (`+customColumns+`,name_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,name_key=excluded.name_key,kind=excluded.kind,
		description=excluded.description,directory=excluded.directory,frontend_directory=excluded.frontend_directory,
		config_path=excluded.config_path,start_command=excluded.start_command,stop_command=excluded.stop_command,
		status_command=excluded.status_command,logs_path=excluded.logs_path,enabled=excluded.enabled,sort_order=excluded.sort_order`,
		a.ID, a.Name, a.Kind, a.Description, a.Directory, a.FrontendDirectory, a.ConfigPath, a.StartCommand,
		a.StopCommand, a.StatusCommand, a.LogsPath, a.Enabled, a.Sort, nameKey(a.Name))
	return requireOne(result, err)
}

func (s *Store) saveCustom(ctx context.Context, a CustomApp) (CustomApp, error) {
	if a.ID == "" {
		a.ID = rpc.ID()
	}
	a.Name = strings.TrimSpace(a.Name)
	if !validCustom(a) {
		return CustomApp{}, rpc.Error("BAD_PARAMS", "名称、应用类型、路径或命令格式无效")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CustomApp{}, err
	}
	defer tx.Rollback()
	var otherID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM custom_apps WHERE name_key=? AND id<>?`, nameKey(a.Name), a.ID).Scan(&otherID)
	if err == nil {
		return CustomApp{}, rpc.Error("CUSTOM_CONFLICT", "已存在同名的其他自定义应用")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CustomApp{}, err
	}
	r := customRow{CustomApp: a, Sort: 100}
	err = tx.QueryRowContext(ctx, `SELECT sort_order FROM custom_apps WHERE id=?`, a.ID).Scan(&r.Sort)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CustomApp{}, err
	}
	if err := writeCustom(ctx, tx, r); err != nil {
		return CustomApp{}, err
	}
	return a, tx.Commit()
}
