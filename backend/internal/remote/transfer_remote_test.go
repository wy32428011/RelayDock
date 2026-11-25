package remote

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

type multiHostStore struct {
	mu    sync.Mutex
	hosts map[string]state.Host
	creds map[string]state.Credential
}

func newMultiHostStore() *multiHostStore {
	return &multiHostStore{
		hosts: make(map[string]state.Host),
		creds: make(map[string]state.Credential),
	}
}

func (s *multiHostStore) add(id string, f *sshFixture) {
	base := f.savedStore()
	h := base.h
	h.ID = id
	s.hosts[id] = h
	s.creds[id] = base.creds
}

func (s *multiHostStore) Host(_ context.Context, id string) (state.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.hosts[id]; ok {
		return h, nil
	}
	return state.Host{}, rpc.Error("HOST_NOT_FOUND", "not found")
}

func (s *multiHostStore) CredentialForHost(_ context.Context, want state.Host) (state.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.hosts[want.ID]; ok && h == want {
		return s.creds[want.ID], nil
	}
	return state.Credential{}, rpc.Error("HOST_CONFLICT", "changed host")
}

func (s *multiHostStore) CredentialRemembered(context.Context, string) (bool, error) {
	return true, nil
}

func (s *multiHostStore) SaveHostIfCurrent(context.Context, state.Host, state.Credential, bool, *state.Host) (state.Host, error) {
	return state.Host{}, rpc.Error("UNSUPPORTED", "not used in test")
}

func (*multiHostStore) Settings(context.Context) (state.Settings, error) {
	return state.Settings{ConnectionTimeoutSeconds: 4}, nil
}

func (*multiHostStore) Custom(context.Context, string) (state.CustomApp, error) {
	return state.CustomApp{}, rpc.Error("CUSTOM_NOT_FOUND", "not found")
}

func dialFixtureSFTP(t *testing.T, s *Service, hostID string) *sftp.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sshClient, err := s.Dial(ctx, hostID)
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

func TestCrossHostTransferCopyAndMove(t *testing.T) {
	fA := startSSH(t, false, false, nil)
	fB := startSSH(t, false, false, nil)
	store := newMultiHostStore()
	store.add("host-a", fA)
	store.add("host-b", fB)
	s := newService(store, nil)
	t.Cleanup(s.Close)

	sftpA := dialFixtureSFTP(t, s, "host-a")
	sftpB := dialFixtureSFTP(t, s, "host-b")

	if err := sftpA.MkdirAll("/src/app/sub/empty"); err != nil {
		t.Fatal(err)
	}
	putFixtureFile(t, sftpA, "/src/app/main.txt", "hello cross-host\n")
	putFixtureFile(t, sftpA, "/src/app/sub/nested.yml", "key: value\n")
	if err := sftpB.MkdirAll("/dst"); err != nil {
		t.Fatal(err)
	}

	// Unconfirmed transfer must be rejected.
	_, err := call(s, "files.transfer", map[string]any{
		"sourceHostId": "host-a", "sourcePath": "/src/app",
		"targetHostId": "host-b", "targetPath": "/dst/app",
		"mode": "copy", "confirmed": false,
	})
	wantCode(t, err, "CONFIRM_REQUIRED")

	// Cross-host recursive directory copy.
	res := mustCall(t, s, "files.transfer", map[string]any{
		"sourceHostId": "host-a", "sourcePath": "/src/app",
		"targetHostId": "host-b", "targetPath": "/dst/app",
		"mode": "copy", "confirmed": true,
	}).(transferResult)
	if !res.OK || res.Files != 2 || res.Dirs < 3 {
		t.Fatalf("unexpected copy result: %+v", res)
	}

	// Verify copied files on host-b.
	gotMain := mustCall(t, s, "files.read", map[string]any{"hostId": "host-b", "path": "/dst/app/main.txt"}).(textResult)
	if gotMain.Content != "hello cross-host\n" {
		t.Fatalf("copied main.txt mismatch: %q", gotMain.Content)
	}
	gotNested := mustCall(t, s, "files.read", map[string]any{"hostId": "host-b", "path": "/dst/app/sub/nested.yml"}).(textResult)
	if gotNested.Content != "key: value\n" {
		t.Fatalf("copied nested.yml mismatch: %q", gotNested.Content)
	}
	// Source still exists on host-a.
	mustCall(t, s, "files.read", map[string]any{"hostId": "host-a", "path": "/src/app/main.txt"})

	// Overwrite=false should fail when target exists.
	_, err = call(s, "files.transfer", map[string]any{
		"sourceHostId": "host-a", "sourcePath": "/src/app/main.txt",
		"targetHostId": "host-b", "targetPath": "/dst/app/main.txt",
		"mode": "copy", "overwrite": false, "confirmed": true,
	})
	wantCode(t, err, "FILE_EXISTS")

	// Overwrite=true should succeed and update file.
	putFixtureFile(t, sftpA, "/src/app/main.txt", "updated content\n")
	mustCall(t, s, "files.transfer", map[string]any{
		"sourceHostId": "host-a", "sourcePath": "/src/app/main.txt",
		"targetHostId": "host-b", "targetPath": "/dst/app/main.txt",
		"mode": "copy", "overwrite": true, "confirmed": true,
	})
	gotUpdated := mustCall(t, s, "files.read", map[string]any{"hostId": "host-b", "path": "/dst/app/main.txt"}).(textResult)
	if gotUpdated.Content != "updated content\n" {
		t.Fatalf("overwritten main.txt mismatch: %q", gotUpdated.Content)
	}

	// Cross-host move removes source after copy.
	mustCall(t, s, "files.transfer", map[string]any{
		"sourceHostId": "host-a", "sourcePath": "/src/app",
		"targetHostId": "host-b", "targetPath": "/dst/moved-app",
		"mode": "move", "confirmed": true,
	})
	mustCall(t, s, "files.read", map[string]any{"hostId": "host-b", "path": "/dst/moved-app/sub/nested.yml"})
	_, err = call(s, "files.list", map[string]any{"hostId": "host-a", "path": "/src/app"})
	wantCode(t, err, "FILE_NOT_FOUND")

	// Copying directory into itself on same host must fail.
	_, err = call(s, "files.transfer", map[string]any{
		"sourceHostId": "host-b", "sourcePath": "/dst/moved-app",
		"targetHostId": "host-b", "targetPath": "/dst/moved-app/inside",
		"mode": "copy", "confirmed": true,
	})
	wantCode(t, err, "INVALID_DESTINATION")
}

func TestArchiveAndExtractRoundtrip(t *testing.T) {
	f := startSSH(t, false, false, nil)
	s := startService(t, f.savedStore(), nil)
	fixture := fixtureSFTP(t, s)

	if err := fixture.MkdirAll("/bundle/conf"); err != nil {
		t.Fatal(err)
	}
	putFixtureFile(t, fixture, "/bundle/readme.txt", "hello archive\n")
	putFixtureFile(t, fixture, "/bundle/conf/app.yml", "port: 8080\n")

	// Tar.gz roundtrip
	ar := mustCall(t, s, "files.archive", map[string]any{
		"hostId": "local-host", "path": "/bundle",
		"destination": "/bundle.tar.gz", "confirmed": true,
	}).(archiveResult)
	if !ar.OK || ar.Files != 2 || ar.Bytes <= 0 {
		t.Fatalf("unexpected tar.gz archive result: %+v", ar)
	}

	ex := mustCall(t, s, "files.extract", map[string]any{
		"hostId": "local-host", "path": "/bundle.tar.gz",
		"destination": "/unpacked-tgz", "confirmed": true,
	}).(extractResult)
	if !ex.OK || ex.Files != 2 {
		t.Fatalf("unexpected tar.gz extract result: %+v", ex)
	}
	got := mustCall(t, s, "files.read", fileRequest("/unpacked-tgz/bundle/conf/app.yml")).(textResult)
	if got.Content != "port: 8080\n" {
		t.Fatalf("extracted tar.gz content mismatch: %q", got.Content)
	}

	// Zip roundtrip
	arZip := mustCall(t, s, "files.archive", map[string]any{
		"hostId": "local-host", "path": "/bundle",
		"destination": "/bundle.zip", "confirmed": true,
	}).(archiveResult)
	if !arZip.OK || arZip.Files != 2 || arZip.Bytes <= 0 {
		t.Fatalf("unexpected zip archive result: %+v", arZip)
	}
	exZip := mustCall(t, s, "files.extract", map[string]any{
		"hostId": "local-host", "path": "/bundle.zip",
		"destination": "/unpacked-zip", "confirmed": true,
	}).(extractResult)
	if !exZip.OK || exZip.Files != 2 {
		t.Fatalf("unexpected zip extract result: %+v", exZip)
	}
	gotZip := mustCall(t, s, "files.read", fileRequest("/unpacked-zip/bundle/readme.txt")).(textResult)
	if gotZip.Content != "hello archive\n" {
		t.Fatalf("extracted zip content mismatch: %q", gotZip.Content)
	}
}
