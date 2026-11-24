package assets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"remoteinstaller.local/core/internal/rpc"
)

const (
	manifestName     = "manifest.json"
	maxManifestBytes = 2 << 20
	maxIndexBytes    = 16 << 20
	maxBundleBytes   = 8 << 30
	maxFileBytes     = 2 << 30
	maxFiles         = 4096
	maxDependencies  = 4096 * 4
)

// Bundle is offline resource metadata. Dir and Entrypoint are internal file
// locations; an entrypoint is a package file, never a script to execute.
type Bundle struct {
	ID         string `json:"id"`
	AppID      string `json:"appId"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	OSVersion  string `json:"osVersion"`
	Arch       string `json:"arch"`
	Source     string `json:"source"`
	Bytes      int64  `json:"bytes"`
	FileCount  int    `json:"fileCount"`
	Digest     string `json:"digest"`
	Trust      string `json:"trust"`
	Dir        string `json:"-"`
	Entrypoint string `json:"-"`
}

type fileRecord struct {
	Path         string   `json:"path"`
	Size         *int64   `json:"size"`
	SHA256       string   `json:"sha256"`
	Dependencies []string `json:"dependencies"`
}

type manifest struct {
	Schema     int          `json:"schema"`
	AppID      string       `json:"appId"`
	Version    string       `json:"version"`
	OS         string       `json:"os"`
	OSVersion  string       `json:"osVersion"`
	Arch       string       `json:"arch"`
	Source     string       `json:"source"`
	Entrypoint string       `json:"entrypoint,omitempty"`
	Files      []fileRecord `json:"files"`
}

type resourcePlan struct {
	bundle Bundle
	data   []byte
	files  []fileRecord
	byPath map[string]fileRecord
	tree   map[string]bool // true for a directory; only manifest-listed payload is allowed.
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashString(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func architecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x86_64", "x64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
}

var osVersionKey = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,3}$`)

func osVersionValid(value string) bool {
	return len(value) <= 48 && osVersionKey.MatchString(value)
}

func knownOfflineCombination(appID, osName, osVersion, arch string) bool {
	// The legacy EL7 AArch64 system-package sets lack a checked dependency
	// closure. An archive-based app is a different capability.
	if osName == "centos" && arch == "arm64" && (osVersion == "7" || strings.HasPrefix(osVersion, "7.")) {
		return !usesSystemPackages(appID)
	}
	return true
}

func usesSystemPackages(appID string) bool { return len(primaryDebNames[appID]) != 0 }

func (s *Store) readManifest(root *os.Root) (resourcePlan, error) {
	var plan resourcePlan
	invalid := func(msg string) (resourcePlan, error) { return plan, rpc.Error("RESOURCE_INVALID", msg) }
	data, err := readSmallFile(root, manifestName, maxManifestBytes)
	if err != nil {
		return invalid("资源目录缺少可读取的 manifest.json，或清单超过大小限制")
	}
	var m manifest
	if !utf8.Valid(data) || !uniqueJSONKeys(data) || !strictJSON(data, &m) || m.Schema != 1 {
		return invalid("资源清单 JSON 或 schema 不正确")
	}
	i, ok := s.byID[m.AppID]
	if !ok || !slices.Contains(s.catalog[i].Versions, m.Version) || !slices.Contains(s.catalog[i].Platforms, m.OS) || !osVersionValid(m.OSVersion) ||
		(m.Arch != "amd64" && m.Arch != "arm64") || !knownOfflineCombination(m.AppID, m.OS, m.OSVersion, m.Arch) {
		return plan, rpc.Error("RESOURCE_MISMATCH", "资源清单的应用、版本、目标系统或 CPU 架构不受支持")
	}
	if strings.TrimSpace(m.Source) == "" || len(m.Source) > 256 || strings.ContainsFunc(m.Source, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) {
		return invalid("资源清单必须注明可读的来源")
	}
	if len(m.Files) == 0 || len(m.Files) > maxFiles {
		return invalid("资源清单文件数量超出限制或为空")
	}

	plan.data, plan.files = data, m.Files
	plan.byPath = make(map[string]fileRecord, len(m.Files))
	plan.tree = map[string]bool{manifestName: false}
	folded := map[string]string{manifestName: manifestName}
	register := func(name string, directory bool) bool {
		key := strings.ToLower(name)
		if prev, exists := folded[key]; exists && prev != name {
			return false
		}
		if wasDir, exists := plan.tree[name]; exists && (!wasDir || !directory) {
			return false
		}
		folded[key], plan.tree[name] = name, directory
		return true
	}
	var total int64
	for _, file := range m.Files {
		if !safePath(file.Path) || !register(file.Path, false) || file.Size == nil || *file.Size < 0 || *file.Size > maxFileBytes ||
			!hashString(file.SHA256) || file.Dependencies == nil {
			return invalid("资源文件路径、大小、散列或依赖字段无效")
		}
		for dir := path.Dir(file.Path); dir != "."; dir = path.Dir(dir) {
			if !register(dir, true) {
				return invalid("资源文件和目录的名称冲突")
			}
		}
		if !matchesPackageTarget(file.Path, m.OS, m.Arch) {
			return plan, rpc.Error("RESOURCE_MISMATCH", "资源含有不匹配目标平台的 DEB 或 RPM")
		}
		total += *file.Size
		if total > maxBundleBytes {
			return invalid("资源包总大小超过限制")
		}
		plan.byPath[file.Path] = file
	}
	var dependencies int
	for _, file := range m.Files {
		dependencies += len(file.Dependencies)
		if dependencies > maxDependencies || !distinct(file.Dependencies, func(dep string) bool {
			_, exists := plan.byPath[dep]
			return exists && dep != file.Path
		}) {
			return invalid("资源文件依赖不存在、重名、指向自身或超过限制")
		}
	}
	if m.Entrypoint != "" {
		file, ok := plan.byPath[m.Entrypoint]
		if !ok || *file.Size == 0 || !packageEntrypoint(m.Entrypoint) {
			return invalid("资源入口必须是清单内非空的包文件，而不是脚本")
		}
	}
	digest := sha256Hex(data)
	plan.bundle = Bundle{
		ID: digest, AppID: m.AppID, Version: m.Version, OS: m.OS, OSVersion: m.OSVersion,
		Arch: m.Arch, Source: m.Source, Bytes: total, FileCount: len(m.Files), Digest: digest,
		Trust: "reviewed", Dir: root.Name(), Entrypoint: m.Entrypoint,
	}
	return plan, nil
}

func matchesPackageTarget(name, osName, arch string) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".deb") {
		return osName == "ubuntu" && (strings.HasSuffix(name, "_"+arch+".deb") || strings.HasSuffix(name, "_all.deb"))
	}
	if strings.HasSuffix(name, ".rpm") {
		rpmArch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
		return osName == "centos" && (strings.HasSuffix(name, "."+rpmArch+".rpm") || strings.HasSuffix(name, ".noarch.rpm"))
	}
	return true
}

func packageEntrypoint(name string) bool {
	name = strings.ToLower(name)
	for _, suffix := range []string{".zip", ".tar.gz", ".tgz", ".tar.xz", ".tar.bz2", ".7z", ".deb", ".rpm", ".msi", ".exe"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func strictJSON(data []byte, out any) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil && errors.Is(d.Decode(new(any)), io.EOF)
}

// encoding/json accepts duplicate keys (including variant case for struct
// fields), which is ambiguous when a person or another implementation reviews
// the same manifest. Limit JSON nesting as well as aggregate bytes.
func uniqueJSONKeys(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var visit func(int) bool
	visit = func(depth int) bool {
		t, err := d.Token()
		if err != nil || depth > 16 {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				key, ok := k.(string)
				if err != nil || !ok || strings.ContainsFunc(key, func(r rune) bool { return r >= utf8.RuneSelf }) {
					return false
				}
				key = strings.ToLower(key)
				if seen[key] {
					return false
				}
				seen[key] = true
				if !visit(depth + 1) {
					return false
				}
			}
		case json.Delim('['):
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
		default:
			return true
		}
		_, err = d.Token()
		return err == nil
	}
	if !visit(0) {
		return false
	}
	_, err := d.Token()
	return errors.Is(err, io.EOF)
}

func (p resourcePlan) checkTree(ctx context.Context, src *os.Root) error {
	const mismatch = "资源目录与清单不一致，不能包含未声明文件、空目录或链接"
	seen := 0
	var walk func(*os.Root, string) error
	walk = func(dir *os.Root, prefix string) error {
		fd, err := dir.Open(".")
		if err != nil {
			return rpc.Error("RESOURCE_INVALID", mismatch)
		}
		defer fd.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, readErr := fd.ReadDir(128)
			for _, entry := range entries {
				name := entry.Name()
				rel := prefix + name
				directory, exists := p.tree[rel]
				if !exists {
					return rpc.Error("RESOURCE_INVALID", mismatch)
				}
				seen++
				if directory {
					child, err := openChildDirectory(dir, name)
					if err != nil {
						return rpc.Error("RESOURCE_INVALID", mismatch)
					}
					err = walk(child, rel+"/")
					child.Close()
					if err != nil {
						return err
					}
				} else if info, err := dir.Lstat(name); err != nil || !info.Mode().IsRegular() {
					return rpc.Error("RESOURCE_INVALID", mismatch)
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return rpc.Error("RESOURCE_INVALID", mismatch)
			}
		}
	}
	if err := walk(src, ""); err != nil {
		return err
	}
	if seen != len(p.tree) {
		return rpc.Error("RESOURCE_INVALID", mismatch)
	}
	return nil
}

type cancellableReader struct {
	ctx context.Context
	io.Reader
}

func (r cancellableReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(b)
}

// verify performs the same checks on inspect, import and preflight. An import
// copies through the verified stream, not through a second read of the source.
func (p resourcePlan) verify(ctx context.Context, src, dst *os.Root) error {
	if err := p.checkTree(ctx, src); err != nil {
		return err
	}
	indexes := aptValidation{seenFiles: map[string]bool{}}
	buf := make([]byte, 128<<10)
	for _, file := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.verifyFile(ctx, src, dst, file, &indexes, buf); err != nil {
			return err
		}
	}
	return p.checkAptCoverage(indexes)
}

func (p resourcePlan) verifyFile(ctx context.Context, src, dst *os.Root, file fileRecord, indexes *aptValidation, buf []byte) error {
	f, info, err := openRegular(src, file.Path)
	if err != nil {
		return rpc.Error("RESOURCE_INVALID", "无法读取清单中指定的资源文件")
	}
	defer f.Close()
	if info.Size() != *file.Size {
		return rpc.Error("RESOURCE_INVALID", "资源文件大小与清单不一致")
	}
	var index bytes.Buffer
	isIndex := p.bundle.OS == "ubuntu" && path.Base(file.Path) == "Packages"
	if isIndex && *file.Size > maxIndexBytes {
		return rpc.Error("RESOURCE_INVALID", "APT Packages 索引超过大小限制")
	}
	h := sha256.New()
	var out io.Writer = h
	if isIndex {
		out = io.MultiWriter(h, &index)
	}
	var dest *os.File
	if dst != nil {
		dir := path.Dir(file.Path)
		if dir != "." {
			if err := dst.MkdirAll(dir, 0700); err != nil {
				return rpc.Error("RESOURCE_STORAGE", "无法建立资源暂存目录")
			}
		}
		dest, err = dst.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return rpc.Error("RESOURCE_STORAGE", "无法保存资源暂存文件")
		}
		defer dest.Close()
		out = io.MultiWriter(out, dest)
	}
	n, err := io.CopyBuffer(out, io.LimitReader(cancellableReader{ctx, f}, *file.Size+1), buf)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return rpc.Error("RESOURCE_IO", "读取或保存资源失败，请检查权限和磁盘空间")
	}
	if n != *file.Size || hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		return rpc.Error("RESOURCE_INVALID", "资源文件大小或 SHA-256 与清单不一致")
	}
	if isIndex {
		if err := p.checkAptIndex(file.Path, &index, indexes); err != nil {
			return err
		}
	}
	if dest != nil {
		if err := dest.Sync(); err != nil {
			return rpc.Error("RESOURCE_STORAGE", "无法写入资源缓存，请检查磁盘空间")
		}
	}
	return nil
}
