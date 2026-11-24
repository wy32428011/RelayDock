package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"remoteinstaller.local/core/internal/state"
)

func fixtureSFTP(t *testing.T, s *Service) *sftp.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sshClient, err := s.Dial(ctx, "local-host")
	if err != nil {
		t.Fatal(err)
	}
	c, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshClient.Close(); _ = c.Close() })
	return c
}

func putFixtureFile(t *testing.T, c *sftp.Client, name, content string) {
	t.Helper()
	w, err := c.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileRequest(path string) map[string]any {
	return map[string]any{"hostId": "local-host", "path": path}
}

func TestSFTPEditDetectsConcurrentAndExternalChanges(t *testing.T) {
	f := startSSH(t, false, false, nil)
	s := startService(t, f.savedStore(), nil)
	fixture := fixtureSFTP(t, s)
	initial := "标题: 测试\r\n# 原注释\r\n"
	putFixtureFile(t, fixture, "/配置.yml", initial)
	p := fileRequest("/配置.yml")
	snapshot := mustCall(t, s, "files.read", p).(textResult)
	if snapshot.Content != initial || snapshot.Digest != digestBytes([]byte(initial)) {
		t.Fatalf("did not preserve original bytes: %+v", snapshot)
	}
	putFixtureFile(t, fixture, "/配置.yml", "external edit\n")
	p["expectedDigest"], p["content"], p["confirmed"] = snapshot.Digest, "should not overwrite", true
	_, err := call(s, "files.write", p)
	wantCode(t, err, "FILE_CONFLICT")
	current := mustCall(t, s, "files.read", fileRequest("/配置.yml")).(textResult)
	if current.Content != "external edit\n" {
		t.Fatal("stale write overwrote original")
	}
	p["expectedDigest"] = current.Digest
	p["confirmed"] = false
	_, err = call(s, "files.write", p)
	wantCode(t, err, "CONFIRM_REQUIRED")
	errors := make(chan error, 2)
	for _, body := range []string{"edit A", "edit B"} {
		go func() {
			params := fileRequest("/配置.yml")
			params["content"], params["expectedDigest"], params["confirmed"] = body, current.Digest, true
			result, err := call(s, "files.write", params)
			if err == nil && result.(map[string]string)["digest"] != digestBytes([]byte(body)) {
				t.Error("write did not return new digest")
			}
			errors <- err
		}()
	}
	first, second := <-errors, <-errors
	if first != nil {
		first, second = second, first
	}
	if first != nil {
		t.Fatalf("neither concurrent edit succeeded: %v / %v", first, second)
	}
	wantCode(t, second, "FILE_CONFLICT")
	listed := mustCall(t, s, "files.list", fileRequest(".")).(directoryResult)
	if listed.Path != "/" || len(listed.Entries) != 1 || listed.Entries[0].Path != "/配置.yml" {
		t.Fatalf("unexpected listing or staging leftovers: %+v", listed)
	}
}

func TestSFTPWithoutAtomicReplacementLeavesOriginal(t *testing.T) {
	if err := sftp.SetSFTPExtensions(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sftp.SetSFTPExtensions("hardlink@openssh.com", posixRename, "statvfs@openssh.com"); err != nil {
			t.Fatal(err)
		}
	})
	f := startSSH(t, false, false, nil)
	s := startService(t, f.savedStore(), nil)
	fixture := fixtureSFTP(t, s)
	putFixtureFile(t, fixture, "/original.txt", "original\n")
	p := fileRequest("/original.txt")
	snapshot := mustCall(t, s, "files.read", p).(textResult)
	p["expectedDigest"], p["content"], p["confirmed"] = snapshot.Digest, "replacement\n", true
	_, err := call(s, "files.write", p)
	wantCode(t, err, "ATOMIC_REPLACE_UNAVAILABLE")
	current := mustCall(t, s, "files.read", fileRequest("/original.txt")).(textResult)
	if current != snapshot {
		t.Fatal("server without atomic replace lost the original file")
	}
}

func TestSFTPDirectoryUploadDownloadRenameAndSafeDelete(t *testing.T) {
	f := startSSH(t, false, false, nil)
	s := startService(t, f.savedStore(), nil)
	fixture := fixtureSFTP(t, s)
	mustCall(t, s, "files.mkdir", fileRequest("/work"))
	source := filepath.Join(t.TempDir(), "合成目录")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 1, 0xff, 0x1b}, 52*1024)
	if err := os.WriteFile(filepath.Join(source, "sub", "opaque.bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	upload := fileRequest("/work/project")
	upload["token"], upload["confirmed"] = source, true
	mustCall(t, s, "files.upload", upload)
	_, err := call(s, "files.upload", upload)
	wantCode(t, err, "FILE_EXISTS")
	upload["overwrite"] = true
	mustCall(t, s, "files.upload", upload)
	_, err = call(s, "files.read", fileRequest("/work/project/sub/opaque.bin"))
	wantCode(t, err, "FILE_TEXT_LIMIT")
	download := fileRequest("/work/project/sub/opaque.bin")
	target := filepath.Join(t.TempDir(), "下载.bin")
	download["token"] = target
	mustCall(t, s, "files.download", download)
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(data, got) {
		t.Fatal("download did not preserve binary bytes")
	}
	rename := fileRequest("/work/project/sub/opaque.bin")
	rename["confirmed"], rename["to"] = true, "/work/project/sub/renamed.bin"
	mustCall(t, s, "files.rename", rename)
	putFixtureFile(t, fixture, "/outside.txt", "do not delete\n")
	if err := fixture.Symlink("/", "/work/project/jump"); err != nil {
		t.Fatal(err)
	}
	listed := mustCall(t, s, "files.list", fileRequest("/work/project")).(directoryResult)
	if len(listed.Entries) != 2 || !listed.Entries[1].Link || listed.Entries[1].Directory {
		t.Fatalf("directory link not identified: %+v", listed)
	}
	_, err = call(s, "files.read", fileRequest("/work/project/jump/outside.txt"))
	wantCode(t, err, "FILE_LINK")
	_, err = call(s, "files.list", fileRequest("/work/project/jump"))
	wantCode(t, err, "FILE_LINK")
	_, err = call(s, "files.list", fileRequest("/work/../"))
	wantCode(t, err, "REMOTE_PATH")
	remove := fileRequest("/")
	remove["confirmed"], remove["recursive"] = true, true
	_, err = call(s, "files.delete", remove)
	wantCode(t, err, "PROTECTED_PATH")
	remove["path"] = "/work/project"
	mustCall(t, s, "files.delete", remove)
	if text := mustCall(t, s, "files.read", fileRequest("/outside.txt")).(textResult).Content; text != "do not delete\n" {
		t.Fatal("recursive deletion traversed a link")
	}
	_, err = call(s, "files.list", fileRequest("/work/project"))
	wantCode(t, err, "FILE_NOT_FOUND")
}

func TestWindowsPathVolumeAndAliasValidation(t *testing.T) {
	w := &remoteFS{windows: true, home: "/C:/Users/operator", nativeDrivePath: true}
	for input, want := range map[string]string{
		".": "/C:/Users/operator", "/": "/C:/", `d:\deploy\微服务`: "/D:/deploy/微服务", "C:": "/C:/",
		"/D:/": "/D:/", "./sub/file.txt": "/C:/Users/operator/sub/file.txt", "/server/log": "/C:/server/log",
	} {
		got, err := w.resolve(input)
		if err != nil || got != want {
			t.Fatalf("resolve %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{`\\server\share`, `\\?\C:\Windows`, `C:relative`, `C:\..\Windows`, `C:\. \Windows`, `C:\LPT1.log`, `C:\file:stream`, `C:\a\b.`} {
		_, err := w.resolve(input)
		wantCode(t, err, "REMOTE_PATH")
	}
	if !w.protected("/D:/") || !w.protected("/C:/users") || w.protected("/C:/Users/operator/projects") || w.wire("/D:/") != "D:/" || w.parent("/D:/file") != "/D:/" {
		t.Fatal("incorrect Windows path/root rules")
	}
}

func TestExplicitCustomActionsUseStoredCommandsAndSafeOutput(t *testing.T) {
	f := startSSH(t, false, false, nil)
	store := f.savedStore()
	store.app = state.CustomApp{ID: "synthetic-app", Name: "Fixture", Kind: "SUPPORT", Enabled: true,
		StartCommand: "test-start", StopCommand: "test-stop", StatusCommand: "test-status", Directory: "/deploy/backend", FrontendDirectory: "/deploy/frontend"}
	s := startService(t, store, nil)
	p := map[string]any{"hostId": "local-host", "appId": "synthetic-app", "action": "restart", "confirmed": false}
	_, err := call(s, "custom.run", p)
	wantCode(t, err, "CONFIRM_REQUIRED")
	p["confirmed"], p["command"] = true, "not a saved command"
	_, err = call(s, "custom.run", p)
	wantCode(t, err, "BAD_PARAMS")
	delete(p, "command")
	if len(f.commands()) != 0 {
		t.Fatal("executed an unconfirmed or arbitrary command")
	}
	mustCall(t, s, "custom.run", p)
	if !slices.Equal(f.commands(), []string{"test-stop", "test-start"}) {
		t.Fatal("restart did not use stored commands in order")
	}
	p["action"] = "status"
	out := mustCall(t, s, "custom.run", p).(map[string]string)["output"]
	if strings.Contains(out, syntheticSecret) || !strings.Contains(out, "fixture output") {
		t.Fatalf("unexpected status output: %q", out)
	}
	source := filepath.Join(t.TempDir(), "front.js")
	if err := os.WriteFile(source, []byte("// synthetic frontend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p["action"], p["token"] = "uploadFrontend", source
	mustCall(t, s, "custom.run", p)
	text := mustCall(t, s, "files.read", fileRequest("/deploy/frontend/front.js")).(textResult)
	if text.Content != "// synthetic frontend\n" {
		t.Fatal("frontend deployment did not use the stored target")
	}
	p["action"] = "uploadAndStart"
	mustCall(t, s, "custom.run", p)
	commands := f.commands()
	if commands[len(commands)-1] != "test-start" {
		t.Fatal("did not start once upload completed")
	}
	absoluteMissing := filepath.Join(t.TempDir(), "not-present")
	p["token"] = absoluteMissing
	_, err = call(s, "custom.run", p)
	wantCode(t, err, "LOCAL_SOURCE")
	if !slices.Equal(f.commands(), commands) {
		t.Fatal("started application after failed upload")
	}
	_, err = s.Handle(context.Background(), "exec", json.RawMessage(`{}`))
	wantCode(t, err, "UNKNOWN_METHOD")
}
