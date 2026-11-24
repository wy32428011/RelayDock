package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"remoteinstaller.local/core/internal/rpc"
)

func builtinRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../resources")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(builtinRoot(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func faultIs(t *testing.T, err error, want string) {
	t.Helper()
	var fault *rpc.Fault
	if !errors.As(err, &fault) || fault.Code != want {
		t.Fatalf("wanted %s, got %#v", want, err)
	}
}

func TestCatalogAndBuiltinScriptPlatforms(t *testing.T) {
	s := newTestStore(t)
	result, err := s.Handle(context.Background(), "catalog.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	apps := result.([]App)
	want := map[string][]string{
		"mysql": {"8.0.35", "5.7.44"}, "mariadb": {"11.4.7"}, "redis": {"7.2.3", "6.2.13"},
		"nginx": {"1.25.3", "1.24.0"}, "elasticsearch": {"8.5.3"}, "rabbitmq": {"3.12.10", "3.11.27"},
		"mosquitto": {"2.0.21", "2.0.22", "2.1.2", "1.6.10"}, "consul": {"1.22.6"}, "traefik": {"3.6.13"},
		"jdk8": {"8u452b09"}, "jdk11": {"11.0.27_6"}, "jdk17": {"17.0.15_6"},
	}
	families := map[string]bool{}
	if len(apps) != len(want) {
		t.Fatalf("got %d catalog entries", len(apps))
	}
	for _, a := range apps {
		families[a.Family] = true
		if !slices.Equal(a.Versions, want[a.ID]) || len(a.Params) == 0 || a.SupportsOnline {
			t.Fatalf("incorrect or unverified app metadata: %+v", a)
		}
		for _, osName := range a.Platforms {
			dir, entry, err := s.Script(osName, a.ID, a.Versions[0], "detect")
			if err != nil {
				t.Fatalf("%s/%s: %v", osName, a.ID, err)
			}
			body, err := os.ReadFile(filepath.Join(dir, entry))
			if err != nil || !filepath.IsAbs(dir) || filepath.Base(entry) != entry || bytes.ContainsRune(body, '\r') {
				t.Fatalf("invalid script file %s/%s: %v", osName, a.ID, err)
			}
			if osName == "windows" {
				if entry != "check_status_windows.ps1" || !bytes.HasPrefix(body, []byte{0xef, 0xbb, 0xbf}) {
					t.Fatalf("incorrect Windows detection script for %s", a.ID)
				}
			} else if entry != "check_status_linux.sh" || !bytes.HasPrefix(body, []byte("#!")) {
				t.Fatalf("incorrect Linux detection script for %s", a.ID)
			}
		}
	}
	if len(families) != 10 || !families["JDK"] || bytes.Contains(jsonBytes(t, apps), []byte("scriptDir")) {
		t.Fatalf("invalid public catalog: %v", families)
	}
	apps[0].Versions[0] = "bogus"
	apps[0].Params[0].Key = "bogus"
	if a, _ := s.App("mysql"); a.Versions[0] != "8.0.35" || a.Params[0].Key != "port" {
		t.Fatal("callers can mutate the catalog")
	}

	for _, target := range []struct{ os, id, kind, folder, want string }{
		{"Ubuntu", "mosquitto", "install", "Mosquitto", "install_ubuntu.sh"},
		{"centos", "mysql", "install", "MySQL", "install_centos.sh"},
		{"ubuntu", "mariadb", "uninstall", "MariaDB", "uninstall_linux.sh"},
		{"centos", "traefik", "install", "Traefik", "install_linux.sh"},
		{"windows", "jdk17", "install", "JDK17", "install_windows.ps1"},
	} {
		dir, entry, err := s.Script(target.os, target.id, "", target.kind)
		if err != nil || entry != target.want || filepath.Base(dir) != target.folder {
			t.Fatalf("%+v resolved %s/%s (%v)", target, dir, entry, err)
		}
		if strings.HasSuffix(entry, "_ubuntu.sh") || strings.HasSuffix(entry, "_centos.sh") {
			if _, err := os.Stat(filepath.Join(dir, "install_common.sh")); err != nil {
				t.Fatalf("legacy script dependency missing: %v", err)
			}
		}
	}
	_, _, err = s.Script("windows", "mysql", "", "install")
	faultIs(t, err, "SCRIPT_UNAVAILABLE")
	_, _, err = s.Script("windows", "mariadb", "", "detect")
	faultIs(t, err, "UNSUPPORTED_PLATFORM")
	_, _, err = s.Script("ubuntu", "redis", "../fake", "detect")
	faultIs(t, err, "VERSION_UNSUPPORTED")
}

func TestScriptVersionDirectoryTakesPriority(t *testing.T) {
	root := t.TempDir()
	data, err := os.ReadFile(filepath.Join(builtinRoot(t), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, "catalog.json", string(data))
	put(t, root, "scripts/Redis/install_ubuntu.sh", "# base")
	put(t, root, "scripts/Redis/v7.2.3/install_linux.sh", "# historical version directory")
	put(t, root, "scripts/Redis/7.2.3/install_ubuntu.sh", "# native version directory")
	s, err := New(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ os, directory, name string }{
		{"ubuntu", "7.2.3", "install_ubuntu.sh"}, {"centos", "v7.2.3", "install_linux.sh"},
	} {
		dir, entry, err := s.Script(target.os, "redis", "7.2.3", "install")
		if err != nil || filepath.Base(dir) != target.directory || entry != target.name {
			t.Fatalf("%+v resolved %s/%s (%v)", target, dir, entry, err)
		}
	}
	put(t, root, "scripts/Redis/v7.2.3/check_status_Windows.ps1", "# incorrect casing")
	_, _, err = s.Script("windows", "redis", "7.2.3", "detect")
	faultIs(t, err, "SCRIPT_UNAVAILABLE")
}

func record(path, body string, dependencies ...string) fileRecord {
	n := int64(len(body))
	if dependencies == nil {
		dependencies = []string{}
	}
	return fileRecord{path, &n, sha256Hex([]byte(body)), dependencies}
}

func mockManifest() manifest {
	return manifest{Schema: 1, AppID: "jdk17", Version: "17.0.15_6", OS: "ubuntu", OSVersion: "24.04", Arch: "arm64", Source: "合成资源夹具"}
}

func saveManifest(t *testing.T, dir string, m manifest) string {
	b := jsonBytes(t, m)
	put(t, dir, manifestName, string(b))
	return sha256Hex(b)
}

func mockBundle(t *testing.T, source string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	put(t, dir, "package.tgz", "abc")
	put(t, dir, "deps/settings.conf", "let x = 1")
	m := mockManifest()
	m.Source = source
	m.Entrypoint = "package.tgz"
	m.Files = []fileRecord{record("package.tgz", "abc", "deps/settings.conf"), record("deps/settings.conf", "let x = 1")}
	return dir, saveManifest(t, dir, m)
}

func TestImportResolveExactAndPersisted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	dir, digest := mockBundle(t, "经人工审阅的微型测试包")
	v, err := s.Handle(ctx, "resource.list", nil)
	if err != nil || string(jsonBytes(t, v)) != "[]" {
		t.Fatalf("new resource list: %v %v", v, err)
	}
	request := func(token, digest string) []byte {
		return jsonBytes(t, map[string]string{"token": token, "digest": digest})
	}
	_, err = s.Handle(ctx, "resource.import", request(dir, digest))
	faultIs(t, err, "RESOURCE_CONFIRM_REQUIRED")
	v, err = s.Handle(ctx, "resource.inspect", jsonBytes(t, map[string]string{"token": dir}))
	if err != nil {
		t.Fatal(err)
	}
	preview := v.(Bundle)
	if preview.Digest != digest || preview.Trust != "reviewed" || preview.Dir != "" || preview.Entrypoint != "" {
		t.Fatalf("invalid resource preview: %+v", preview)
	}
	otherDir, _ := mockBundle(t, preview.Source)
	_, err = s.Handle(ctx, "resource.import", request(otherDir, preview.Digest))
	faultIs(t, err, "RESOURCE_CONFIRM_REQUIRED")
	_, err = s.Handle(ctx, "resource.import", request(dir, strings.Repeat("a", 64)))
	faultIs(t, err, "RESOURCE_CONFIRM_REQUIRED")
	v, err = s.Handle(ctx, "resource.import", request(dir, preview.Digest))
	if err != nil || v.(Bundle) != preview {
		t.Fatalf("resource import did not commit preview: %v, %v", v, err)
	}
	v, err = s.Handle(ctx, "resource.list", nil)
	if err != nil || !slices.Equal(v.([]Bundle), []Bundle{preview}) {
		t.Fatalf("incorrect public resource list: %+v, %v", v, err)
	}
	// Source directory is no longer needed; reconstruct the store to check persistence.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	s, err = New(s.root, filepath.Dir(s.cache))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Resolve(ctx, "jdk17", "17.0.15_6", "Ubuntu", "24.04", "aarch64", "")
	if err != nil || b.ID != preview.ID || b.Entrypoint != "package.tgz" || !filepath.IsAbs(b.Dir) {
		t.Fatalf("incorrect resolution: %+v, %v", b, err)
	}
	if raw := jsonBytes(t, b); bytes.Contains(raw, []byte("package.tgz")) || bytes.Contains(raw, []byte("\"dir\"")) {
		t.Fatalf("resource response leaked local paths: %s", raw)
	}
	for _, target := range []struct{ version, os, osVersion, arch string }{
		{"17.0.15_6", "ubuntu", "24", "arm64"}, {"17.0.15_6", "ubuntu", "22.04", "arm64"},
		{"17.0.15_6", "ubuntu", "24.04", "amd64"}, {"17.0.15_6", "ubuntu", "24.04", ""},
		{"17.0.15_6", "centos", "24.04", "arm64"}, {"17.0.15_7", "ubuntu", "24.04", "arm64"},
	} {
		for _, id := range []string{"", preview.ID} {
			_, err := s.Resolve(ctx, "jdk17", target.version, target.os, target.osVersion, target.arch, id)
			faultIs(t, err, "RESOURCE_MISMATCH")
		}
	}
	// Same-length corruption must fail even with a valid cached manifest ID.
	put(t, b.Dir, "deps/settings.conf", "let x = 2")
	_, err = s.Resolve(ctx, "jdk17", "17.0.15_6", "ubuntu", "24.04", "arm64", preview.ID)
	faultIs(t, err, "RESOURCE_INVALID")
}

func TestImportAfterSourceChangeDoesNotCommit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	dir, digest := mockBundle(t, "确认后变化的资源")
	if _, err := s.inspect(ctx, dir); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "package.tgz", "xyz")
	_, err := s.importResource(ctx, dir, digest)
	faultIs(t, err, "RESOURCE_INVALID")
	items, err := os.ReadDir(s.cache)
	if err != nil || len(items) != 0 {
		t.Fatalf("partial or final commit after failure: %v %v", items, err)
	}
	put(t, dir, "package.tgz", "abc")
	if _, err := s.inspect(ctx, dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, manifestName, string(before)+"\n")
	_, err = s.importResource(ctx, dir, digest)
	faultIs(t, err, "RESOURCE_CHANGED")
	if _, err := s.inspect(ctx, dir); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.previews[dir] = preview{digest: sha256Hex(append(before, '\n')), expires: time.Now().Add(-time.Minute)}
	s.mu.Unlock()
	_, err = s.importResource(ctx, dir, sha256Hex(append(before, '\n')))
	faultIs(t, err, "RESOURCE_CONFIRM_REQUIRED")
}

func TestResolveWithMultipleMatchesRequiresID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, source := range []string{"合成资源 A", "合成资源 B", "合成资源 A"} {
		dir, digest := mockBundle(t, source)
		if _, err := s.inspect(ctx, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := s.importResource(ctx, dir, digest); err != nil {
			t.Fatal(err)
		}
		if b, err := s.Resolve(ctx, "jdk17", "17.0.15_6", "ubuntu", "24.04", "arm64", digest); err != nil || b.Source != source {
			t.Fatalf("exact resource ID did not resolve: %+v %v", b, err)
		}
	}
	_, err := s.Resolve(ctx, "jdk17", "17.0.15_6", "ubuntu", "24.04", "arm64", "")
	faultIs(t, err, "RESOURCE_AMBIGUOUS")
	list, err := s.list(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("resource re-import not idempotent: %v %v", list, err)
	}
}

func TestCacheQuotaPreservesExistingResources(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	quota := 1
	s.SetCacheQuotaProvider(func(context.Context) (int, error) { return quota, nil })
	fixture := func(label string) (string, string) {
		dir := t.TempDir()
		m := mockManifest()
		m.Source = label
		body := strings.Repeat("a", 600<<10)
		put(t, dir, "synthetic.zip", body)
		m.Files = []fileRecord{record("synthetic.zip", body)}
		digest := saveManifest(t, dir, m)
		if _, err := s.inspect(ctx, dir); err != nil {
			t.Fatal(err)
		}
		return dir, digest
	}
	firstDir, firstDigest := fixture("第一份合成资源")
	if _, err := s.importResource(ctx, firstDir, firstDigest); err != nil {
		t.Fatal(err)
	}
	secondDir, secondDigest := fixture("第二份合成资源")
	_, err := s.importResource(ctx, secondDir, secondDigest)
	faultIs(t, err, "RESOURCE_LIMIT")
	res, err := s.list(ctx)
	if err != nil || len(res) != 1 || res[0].ID != firstDigest {
		t.Fatalf("quota failure affected previous resources: %v %v", res, err)
	}
	quota = 2
	if _, err := s.inspect(ctx, secondDir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.importResource(ctx, secondDir, secondDigest); err != nil {
		t.Fatal(err)
	}
	quota = 1
	if _, err := s.Resolve(ctx, "jdk17", "17.0.15_6", "ubuntu", "24.04", "arm64", secondDigest); err != nil {
		t.Fatalf("reducing the quota must not invalidate existing resources: %v", err)
	}
}

func TestInvalidManifestAndTree(t *testing.T) {
	s := newTestStore(t)
	big := int64(maxFileBytes + 1)
	bundleLimit := int64(maxFileBytes)
	for _, tc := range []struct {
		name  string
		files []fileRecord
	}{
		{"outside", []fileRecord{record("../outside.tgz", "a")}},
		{"backslash", []fileRecord{record(`..\outside.tgz`, "a")}},
		{"absolute", []fileRecord{record("/bundle.zip", "a")}},
		{"device", []fileRecord{record("top/NUL.tgz", "a")}},
		{"ads", []fileRecord{record("bundle.tgz:unlisted", "a")}},
		{"casing", []fileRecord{record("Files/a.tgz", "a"), record("files/b.tgz", "b")}},
		{"prefix", []fileRecord{record("files", "a"), record("files/a.tgz", "a")}},
		{"missingDep", []fileRecord{record("bundle.tgz", "a", "missing")}},
		{"duplicateDep", []fileRecord{record("bundle.tgz", "a", "dep", "dep"), record("dep", "a")}},
		{"missingFile", []fileRecord{record("missing", "a")}},
		{"oversize", []fileRecord{{"big.zip", &big, sha256Hex(nil), []string{}}}},
		{"totalSize", []fileRecord{
			{"1.tgz", &bundleLimit, sha256Hex(nil), []string{}}, {"2.tgz", &bundleLimit, sha256Hex(nil), []string{}},
			{"3.tgz", &bundleLimit, sha256Hex(nil), []string{}}, {"4.tgz", &bundleLimit, sha256Hex(nil), []string{}},
			{"5.tgz", &bundleLimit, sha256Hex(nil), []string{}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mockManifest()
			m.Files = tc.files
			dir := t.TempDir()
			saveManifest(t, dir, m)
			_, err := s.inspect(context.Background(), dir)
			faultIs(t, err, "RESOURCE_INVALID")
		})
	}
	for _, extra := range []string{`"schema":1,`, `"trust":"signed",`} {
		dir := t.TempDir()
		m := mockManifest()
		m.Files = []fileRecord{record("payload.tgz", "fixture")}
		put(t, dir, "payload.tgz", "fixture")
		b := jsonBytes(t, m)
		put(t, dir, manifestName, "{"+extra+string(b[1:]))
		_, err := s.inspect(context.Background(), dir)
		faultIs(t, err, "RESOURCE_INVALID")
	}
	dir, _ := mockBundle(t, "多余文件")
	put(t, dir, "unknown.txt", "unlisted")
	_, err := s.inspect(context.Background(), dir)
	faultIs(t, err, "RESOURCE_INVALID")
}

func TestImportRejectsSymlinks(t *testing.T) {
	s := newTestStore(t)
	dir, _ := mockBundle(t, "符号链接测试")
	outside := t.TempDir()
	put(t, outside, "settings.conf", "let x = 1")
	if err := os.RemoveAll(filepath.Join(dir, "deps")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "deps")); err != nil {
		t.Skipf("cannot create local test symlink: %v", err)
	}
	_, err := s.inspect(context.Background(), dir)
	faultIs(t, err, "RESOURCE_INVALID")
	if err := os.Symlink(dir, filepath.Join(outside, "bundle")); err != nil {
		t.Fatal(err)
	}
	_, err = s.inspect(context.Background(), filepath.Join(outside, "bundle"))
	faultIs(t, err, "RESOURCE_INVALID")
}

func TestUbuntuArmPackageIndex(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const file = "mosquitto_2.1.2-1_arm64.deb"
	const body = "synthetic package bytes"
	index := func(version, arch, filename, hash string) string {
		return fmt.Sprintf("Package: mosquitto\r\nVersion: %s\r\nArchitecture: %s\r\nFilename: %s\r\nSize: %d\r\nSHA256: %s\r\nDescription: tiny fixture\r\n continuation line\r\n", version, arch, filename, len(body), hash)
	}
	hash := sha256Hex([]byte(body))
	for _, test := range []struct{ name, index, extraFile, code string }{
		{"success", index("2:2.1.2-1ubuntu1", "arm64", "./"+file, hash), "", ""},
		{"missing index", "", "", "RESOURCE_INVALID"},
		{"wrong index hash", index("2.1.2", "arm64", file, strings.Repeat("0", 64)), "", "RESOURCE_INVALID"},
		{"wrong index arch", index("2.1.2", "amd64", file, hash), "", "RESOURCE_MISMATCH"},
		{"wrong version", index("2.1.20-1", "arm64", file, hash), "", "RESOURCE_MISMATCH"},
		{"escape", index("2.1.2", "arm64", "../"+file, hash), "", "RESOURCE_INVALID"},
		{"different DEB arch", index("2.1.2", "arm64", file, hash), "libexample_1_amd64.deb", "RESOURCE_MISMATCH"},
		{"unindexed DEB", index("2.1.2", "arm64", file, hash), "libexample_1_arm64.deb", "RESOURCE_INVALID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			m := mockManifest()
			m.AppID, m.Version = "mosquitto", "2.1.2"
			put(t, dir, "repo/"+file, body)
			m.Files = append(m.Files, record("repo/"+file, body))
			if test.index != "" {
				put(t, dir, "repo/Packages", test.index)
				m.Files = append(m.Files, record("repo/Packages", test.index, "repo/"+file))
			}
			if test.extraFile != "" {
				put(t, dir, "repo/"+test.extraFile, body)
				m.Files = append(m.Files, record("repo/"+test.extraFile, body))
			}
			saveManifest(t, dir, m)
			_, err := s.inspect(ctx, dir)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				faultIs(t, err, test.code)
			}
		})
	}
}
