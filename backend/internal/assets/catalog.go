// Package assets catalogs built-in scripts and verifies user-reviewed offline bundles.
package assets

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

// Param describes one operation parameter, not an argument to a shell.
type Param struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Default  string   `json:"default,omitempty"`
	Hint     string   `json:"hint,omitempty"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// App is the public catalog entry. Platforms record available script families,
// not approval to use an install/uninstall script on a production host.
type App struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Category       string   `json:"category"`
	Family         string   `json:"family"`
	Description    string   `json:"description"`
	Platforms      []string `json:"platforms"`
	Versions       []string `json:"versions"`
	Params         []Param  `json:"params"`
	SupportsOnline bool     `json:"supportsOnline"`
}

type catalogItem struct {
	App
	ScriptDir string `json:"scriptDir"`
}

var (
	paramKey   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
	versionKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,47}$`)
)

func readCatalog(root *os.Root) ([]catalogItem, error) {
	const failure = "内置应用目录缺失或格式无效，请检查应用安装文件"
	data, err := readSmallFile(root, "catalog.json", maxManifestBytes)
	if err != nil {
		return nil, rpc.Error("CATALOG_INVALID", failure)
	}
	var cfg struct {
		Schema       int           `json:"schema"`
		Applications []catalogItem `json:"applications"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.Schema != 1 || len(cfg.Applications) == 0 || len(cfg.Applications) > 100 {
		return nil, rpc.Error("CATALOG_INVALID", failure)
	}
	ids, dirs := map[string]bool{}, map[string]bool{}
	for _, a := range cfg.Applications {
		dirKey := strings.ToLower(a.ScriptDir)
		if !paramKey.MatchString(a.ID) || ids[a.ID] || !safePath(a.ScriptDir) || strings.Contains(a.ScriptDir, "/") || dirs[dirKey] ||
			a.Label == "" || a.Family == "" || a.Description == "" || a.Category == "" ||
			len(a.Versions) == 0 || len(a.Platforms) == 0 || a.Params == nil {
			return nil, rpc.Error("CATALOG_INVALID", failure)
		}
		ids[a.ID], dirs[dirKey] = true, true
		if !distinct(a.Versions, func(v string) bool { return versionKey.MatchString(v) && safePath(v) }) || !distinct(a.Platforms, knownOS) {
			return nil, rpc.Error("CATALOG_INVALID", failure)
		}
		params := map[string]bool{}
		for _, p := range a.Params {
			if !paramKey.MatchString(p.Key) || params[p.Key] || p.Label == "" {
				return nil, rpc.Error("CATALOG_INVALID", failure)
			}
			params[p.Key] = true
			switch p.Type {
			case "text", "number", "boolean":
			case "secret":
				if p.Default != "" {
					return nil, rpc.Error("CATALOG_INVALID", failure)
				}
			case "select":
				if len(p.Options) == 0 || (p.Default != "" && !slices.Contains(p.Options, p.Default)) {
					return nil, rpc.Error("CATALOG_INVALID", failure)
				}
			default:
				return nil, rpc.Error("CATALOG_INVALID", failure)
			}
		}
	}
	return cfg.Applications, nil
}

func distinct(items []string, valid func(string) bool) bool {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		key := strings.ToLower(item)
		if seen[key] || !valid(item) {
			return false
		}
		seen[key] = true
	}
	return true
}

func knownOS(osName string) bool {
	return osName == "ubuntu" || osName == "centos" || osName == "windows"
}

func normalizedOS(osName string) string { return strings.ToLower(strings.TrimSpace(osName)) }

func (s *Store) App(id string) (App, bool) {
	i, ok := s.byID[id]
	if !ok {
		return App{}, false
	}
	a := s.catalog[i].App
	a.Platforms = slices.Clone(a.Platforms)
	a.Versions = slices.Clone(a.Versions)
	a.Params = slices.Clone(a.Params)
	for i := range a.Params {
		a.Params[i].Options = slices.Clone(a.Params[i].Options)
	}
	return a, true
}

func (s *Store) apps() []App {
	out := make([]App, 0, len(s.catalog))
	for _, item := range s.catalog {
		a, _ := s.App(item.ID)
		out = append(out, a)
	}
	return out
}

// Script selects a built-in entrypoint by the remote OS, never by runtime.GOOS.
// Its result only locates a script; the caller owns authorization and platform
// safety gates for any write operation.
func (s *Store) Script(hostOS, appID, version, kind string) (dir, entry string, err error) {
	i, ok := s.byID[appID]
	if !ok {
		return "", "", rpc.Error("APP_NOT_FOUND", "未找到该应用")
	}
	app := s.catalog[i]
	hostOS = normalizedOS(hostOS)
	if !slices.Contains(app.Platforms, hostOS) {
		return "", "", rpc.Error("UNSUPPORTED_PLATFORM", "该应用没有此远端系统的脚本")
	}
	if version == "" {
		version = app.Versions[0]
	}
	if !slices.Contains(app.Versions, version) {
		return "", "", rpc.Error("VERSION_UNSUPPORTED", "应用目录不包含所选版本")
	}
	prefix := kind
	switch kind {
	case "detect":
		prefix = "check_status"
	case "install", "uninstall":
	default:
		return "", "", rpc.Error("BAD_PARAMS", "不支持的脚本操作")
	}
	names := []string{prefix + "_" + hostOS + ".sh", prefix + "_linux.sh", prefix + ".sh"}
	if hostOS == "windows" {
		names = []string{prefix + "_windows.ps1"}
	}
	root, err := openDirectory(s.root)
	if err != nil {
		return "", "", rpc.Error("SCRIPT_UNAVAILABLE", "内置脚本目录不可用")
	}
	defer root.Close()

	base := path.Join("scripts", app.ScriptDir)
	// Respect the actual casing even when the client's file system will not.
	if err = exactPath(root, base, true); err != nil {
		return "", "", rpc.Error("SCRIPT_UNAVAILABLE", "内置脚本目录缺失或无效")
	}
	alternate := "v" + version
	if strings.HasPrefix(version, "v") && len(version) > 1 {
		alternate = version[1:]
	}
	for _, candidate := range []string{path.Join(base, version), path.Join(base, alternate), base} {
		if err = exactPath(root, candidate, true); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", rpc.Error("SCRIPT_UNAVAILABLE", "内置脚本目录无效")
		}
		for _, name := range names {
			if err = exactPath(root, path.Join(candidate, name), false); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", "", rpc.Error("SCRIPT_UNAVAILABLE", "内置脚本文件无效")
			}
			return filepath.Join(s.root, filepath.FromSlash(candidate)), name, nil
		}
	}
	return "", "", rpc.Error("SCRIPT_UNAVAILABLE", "没有适用于所选系统及操作的内置脚本")
}

func exactPath(root *os.Root, name string, directory bool) error {
	parts := strings.Split(name, "/")
	parent := "."
	for i, part := range parts {
		entries, err := fs.ReadDir(root.FS(), parent)
		if err != nil {
			return err
		}
		found := false
		for _, entry := range entries {
			if entry.Name() != part {
				if strings.EqualFold(entry.Name(), part) {
					return fs.ErrInvalid
				}
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			wantDir := directory || i < len(parts)-1
			if (wantDir && !info.IsDir()) || (!wantDir && !info.Mode().IsRegular()) {
				return fs.ErrInvalid
			}
			found = true
		}
		if !found {
			return fs.ErrNotExist
		}
		parent = path.Join(parent, part)
	}
	return nil
}
