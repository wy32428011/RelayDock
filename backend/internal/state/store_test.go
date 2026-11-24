package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"remoteinstaller.local/core/internal/rpc"
)

type fakeVault struct {
	mu      sync.Mutex
	entries map[string]string
	blocked bool
}

func (v *fakeVault) Set(service, id, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.blocked {
		return errors.New("synthetic vault unavailable")
	}
	if v.entries == nil {
		v.entries = make(map[string]string)
	}
	v.entries[service+"/"+id] = value
	return nil
}

func (v *fakeVault) Get(service, id string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.blocked {
		return "", errors.New("synthetic vault unavailable")
	}
	if value, ok := v.entries[service+"/"+id]; ok {
		return value, nil
	}
	return "", keyring.ErrNotFound
}

func (v *fakeVault) Delete(service, id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.blocked {
		return errors.New("synthetic vault unavailable")
	}
	if _, ok := v.entries[service+"/"+id]; !ok {
		return keyring.ErrNotFound
	}
	delete(v.entries, service+"/"+id)
	return nil
}

func testStore(t *testing.T) (*Store, *fakeVault) {
	t.Helper()
	v := &fakeVault{}
	s := reopen(t, filepath.Join(t.TempDir(), "升级版 # 专用 数据"), v)
	return s, v
}

func reopen(t *testing.T, path string, v *fakeVault) *Store {
	t.Helper()
	s, err := openWithVault(path, v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func call[T any](t *testing.T, s *Store, method string, p any) T {
	t.Helper()
	v, err := callRaw(t, s, method, p)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	out, ok := v.(T)
	if !ok {
		t.Fatalf("%s response type is %T", method, v)
	}
	return out
}

func callRaw(t *testing.T, s *Store, method string, p any) (any, error) {
	t.Helper()
	if p == nil {
		p = struct{}{}
	}
	buf, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handle(t.Context(), method, buf)
}

func assertFault(t *testing.T, err error, code string) {
	t.Helper()
	var fault *rpc.Fault
	if !errors.As(err, &fault) || fault.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertNoSecrets(t *testing.T, s *Store, needles ...string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := filepath.Join(s.Dir, databaseName+suffix)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if bytes.Contains(data, []byte(needle)) {
				t.Fatalf("synthetic sensitive marker found in database%s", suffix)
			}
		}
	}
}

func testHost(id string) Host {
	return Host{ID: id, Name: "合成主机", Address: "192.0.2.25", Port: 22, Username: "tester", Group: "测试",
		Auth: "password", OS: "ubuntu", OSVersion: "24.04", Arch: "amd64", Fingerprint: "SHA256:synthetic",
		LastSeen: "2026-10-01T01:00:00Z"}
}

func TestProfileCredentialLifecycle(t *testing.T) {
	s, vault := testStore(t)
	var journal string
	var busy, version int
	if err := s.DB.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || busy != 5000 || version != schemaVersion {
		t.Fatalf("profile pragmas: %s %d %d", journal, busy, version)
	}
	ctx := t.Context()
	h := testHost("demo-host")
	remembered := Credential{Secret: "SYNTHETIC-PERSIST-CREDENTIAL-889a"}
	session := Credential{Secret: "SYNTHETIC-SESSION-CREDENTIAL-287a"}
	if _, err := s.SaveHost(ctx, h, remembered, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveHost(ctx, testHost("temporary-host"), session, false); err != nil {
		t.Fatal(err)
	}
	for _, row := range call[[]Host](t, s, "host.list", nil) {
		if !row.CredentialReady {
			t.Fatalf("credentials not ready: %s", row.ID)
		}
		wire, _ := json.Marshal(row)
		if bytes.Contains(wire, []byte("secret")) || bytes.Contains(wire, []byte("keyPath")) {
			t.Fatal("credential leaked in listing")
		}
	}
	assertNoSecrets(t, s, remembered.Secret, session.Secret)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s.Dir, vault)
	if got, err := s.Credential(ctx, h.ID); err != nil || got != remembered {
		t.Fatalf("remembered credential not restored: %v", err)
	}
	if ready, err := s.Host(ctx, "temporary-host"); err != nil || ready.CredentialReady {
		t.Fatal("session credential outlived core")
	}
	if kept, err := s.CredentialRemembered(ctx, h.ID); err != nil || !kept {
		t.Fatal("loss of remember choice")
	}
	old, err := s.Host(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := h
	next.Address = "192.0.2.29"
	if _, err := s.SaveHostIfCurrent(ctx, next, session, false, &old); err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveHostIfCurrent(ctx, h, session, false, &old)
	assertFault(t, err, "HOST_CONFLICT")
	_, err = s.CredentialForHost(ctx, old)
	assertFault(t, err, "HOST_CONFLICT")
	if len(vault.entries) != 0 {
		t.Fatal("remembered secret left behind after session-only replacement")
	}
	call[okResult](t, s, "host.delete", idParams{ID: h.ID})
	_, err = s.Credential(ctx, h.ID)
	assertFault(t, err, "HOST_NOT_FOUND")
}

func TestWALWritersWaitWithoutBlockingReaders(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	writer, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if err := writePreference(ctx, writer, "theme", "light"); err != nil {
		t.Fatal(err)
	}
	pending := make(chan error, 1)
	go func() {
		_, err := s.SaveHost(ctx, testHost("wal-writer"), Credential{Secret: "synthetic"}, false)
		pending <- err
	}()
	select {
	case err := <-pending:
		t.Fatalf("write returned before first transaction released its reservation: %v", err)
	case <-time.After(65 * time.Millisecond):
	}
	if settings, err := s.Settings(ctx); err != nil || settings.Theme != "dark" {
		t.Fatalf("WAL snapshot reader saw a blocked or uncommitted write: %v %v", settings.Theme, err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pending:
		if err != nil {
			t.Fatalf("waiting write did not resume: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("waiting write did not finish")
	}
}

func TestVaultUnavailableAndKeyPath(t *testing.T) {
	s, vault := testStore(t)
	vault.blocked = true
	h := testHost("unavailable-vault")
	secret := Credential{Secret: "SYNTHETIC-OFFLINE-7261"}
	_, err := s.SaveHost(t.Context(), h, secret, true)
	assertFault(t, err, "CREDENTIAL_STORE_UNAVAILABLE")
	if countRows(t, s.DB, "hosts") != 0 {
		t.Fatal("failed remembered save created a host")
	}
	if _, err := s.SaveHost(t.Context(), h, secret, false); err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, s, secret.Secret)
	vault.blocked = false
	keyPath := filepath.Join(t.TempDir(), "test-only-key-reference")
	if err := os.WriteFile(keyPath, []byte("not a private key; placeholder for metadata test"), 0600); err != nil {
		t.Fatal(err)
	}
	h.Auth = "key"
	if _, err := s.SaveHost(t.Context(), h, Credential{KeyPath: keyPath}, true); err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, s, keyPath, filepath.Base(keyPath))
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	_, err = s.Credential(t.Context(), h.ID)
	assertFault(t, err, "AUTH_REQUIRED")
}

func TestInterruptedVaultWriteCleanup(t *testing.T) {
	s, vault := testStore(t)
	ctx := t.Context()
	h := testHost("keep-existing")
	cred := Credential{Secret: "SYNTHETIC-CRASH-KEPT"}
	if _, err := s.SaveHost(ctx, h, cred, true); err != nil {
		t.Fatal(err)
	}
	// Simulate process death after staging a new secret, but before binding it.
	pending, err := s.stageSecret(ctx, "SYNTHETIC-CRASH-UNBOUND", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(vault.entries) != 2 {
		t.Fatal("incorrect crash fixture")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s.Dir, vault)
	if _, err := vault.Get(s.service, pending); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("unbound secret survived: %v", err)
	}
	if got, err := s.Credential(ctx, h.ID); err != nil || got != cred {
		t.Fatalf("bound secret lost: %v", err)
	}
}

func TestSettingsSecretsAreAtomicAndBound(t *testing.T) {
	s, vault := testStore(t)
	before := call[Settings](t, s, "settings.get", nil)
	a := before
	a.Theme, a.RepositoryURL, a.ProxyURL = "light", "https://repo.example.invalid/release", "https://operator@proxy.example.invalid:443"
	repo, proxy := "SYNTHETIC-REPOSITORY-TOKEN-c071", "SYNTHETIC-PROXY-PASSWORD-1809"
	vault.blocked = true
	_, err := callRaw(t, s, "settings.save", settingsInput{Settings: a, RepositoryToken: &repo})
	assertFault(t, err, "CREDENTIAL_STORE_UNAVAILABLE")
	if now := call[Settings](t, s, "settings.get", nil); now != before {
		t.Fatalf("settings partially saved: %#v", now)
	}
	vault.blocked = false
	res := call[Settings](t, s, "settings.save", settingsInput{Settings: a, RepositoryToken: &repo, ProxyPassword: &proxy})
	if res.Theme != "light" || !res.RepositoryTokenSet || !res.ProxyPasswordSet {
		t.Fatalf("wrong settings: %#v", res)
	}
	assertNoSecrets(t, s, repo, proxy)
	bad := a
	bad.RepositoryURL = "https://developer:do-not-store@repo.example.invalid"
	_, err = callRaw(t, s, "settings.save", settingsInput{Settings: bad})
	assertFault(t, err, "BAD_PARAMS")
	a.RepositoryURL = "https://other.example.invalid"
	a.ProxyURL = "https://other-user@proxy.example.invalid:443"
	res = call[Settings](t, s, "settings.save", settingsInput{Settings: a})
	if res.RepositoryTokenSet || res.ProxyPasswordSet || len(vault.entries) != 0 {
		t.Fatal("credentials were not revoked after changing recipients")
	}
}

func TestCustomCRUDAndSeedLifecycle(t *testing.T) {
	s, v := testStore(t)
	seed, err := s.Custom(t.Context(), "SUPPORT")
	if err != nil {
		t.Fatal(err)
	}
	seed.Enabled = false
	call[CustomApp](t, s, "custom.save", seed)
	app := call[CustomApp](t, s, "custom.save", CustomApp{Name: "Example", Kind: "CUSTOM", Enabled: true, Directory: "/srv/example"})
	if app.ID == "" {
		t.Fatal("missing stable identifier")
	}
	_, err = callRaw(t, s, "custom.save", CustomApp{Name: "example", Kind: "GENERIC"})
	assertFault(t, err, "CUSTOM_CONFLICT")
	call[okResult](t, s, "custom.delete", idParams{ID: app.ID})
	_, err = s.Custom(t.Context(), app.ID)
	assertFault(t, err, "CUSTOM_NOT_FOUND")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s.Dir, v)
	a, err := s.Custom(t.Context(), seed.ID)
	if err != nil || a.Enabled {
		t.Fatal("reopening profile restored disabled seed")
	}
}

func TestUpdateCheckEndpointPriorityAndRedirect(t *testing.T) {
	s, _ := testStore(t)
	if out := call[updateResult](t, s, "settings.updateCheck", nil); out.Available || out.Current != DefaultVersion {
		t.Fatal("unconfigured update check changed capability")
	}
	var paths []string
	var calls sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Lock()
		paths = append(paths, r.URL.Path)
		calls.Unlock()
		switch r.URL.Path {
		case "/mirror/api/version":
			fmt.Fprint(w, `{"tag_name":"v0.2.0-rc.2"}`)
		case "/explicit":
			fmt.Fprint(w, `{"data":{"latestVersion":"0.9.1"}}`)
		case "/redirect":
			http.Redirect(w, r, "/explicit", http.StatusFound)
		case "/oversize":
			fmt.Fprint(w, strings.Repeat(" ", maxVersionPayload+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := defaultSettings()
	a.RepositoryURL = server.URL + "/mirror/"
	call[Settings](t, s, "settings.save", settingsInput{Settings: a})
	if out := call[updateResult](t, s, "settings.updateCheck", nil); !out.Available || out.Latest != "v0.2.0-rc.2" {
		t.Fatalf("derived endpoint: %#v", out)
	}
	a.UpdateCheckURL = server.URL + "/explicit"
	call[Settings](t, s, "settings.save", settingsInput{Settings: a})
	if out := call[updateResult](t, s, "settings.updateCheck", nil); out.Latest != "0.9.1" {
		t.Fatalf("explicit endpoint: %#v", out)
	}
	for _, path := range []string{"/redirect", "/oversize"} {
		a.UpdateCheckURL = server.URL + path
		call[Settings](t, s, "settings.save", settingsInput{Settings: a})
		if out := call[updateResult](t, s, "settings.updateCheck", nil); out.Available || out.Latest != "" {
			t.Fatalf("unsafe update result: %#v", out)
		}
	}
	calls.Lock()
	defer calls.Unlock()
	if strings.Join(paths, " ") != "/mirror/api/version /explicit /redirect /oversize" {
		t.Fatalf("unexpected HTTP requests: %v", paths)
	}
}

type wireCheck func(*http.Request) (*http.Response, error)

func (f wireCheck) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateBearerIsScoped(t *testing.T) {
	s, _ := testStore(t)
	cfg := defaultSettings()
	cfg.RepositoryURL = "https://mirror.example.invalid"
	cfg.UpdateCheckURL = "https://update.example.invalid/check"
	token := "SYNTHETIC-HTTPS-TOKEN"
	call[Settings](t, s, "settings.save", settingsInput{Settings: cfg, RepositoryToken: &token})
	got := make(map[string]string)
	s.transport = wireCheck(func(r *http.Request) (*http.Response, error) {
		got[r.URL.Hostname()] = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"version":"0.1.0"}`)), Request: r}, nil
	})
	call[updateResult](t, s, "settings.updateCheck", nil)
	cfg.UpdateCheckURL = ""
	call[Settings](t, s, "settings.save", settingsInput{Settings: cfg})
	call[updateResult](t, s, "settings.updateCheck", nil)
	if got["update.example.invalid"] != "" || got["mirror.example.invalid"] != "Bearer "+token {
		t.Fatalf("incorrect authorization scope: %v", got)
	}
}

func TestOpenDoesNotClaimUnrelatedDatabase(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, databaseName)
	db, err := sql.Open("sqlite", sqliteURI(file, "rwc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE existing (name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := openWithVault(dir, &fakeVault{}); err == nil {
		_ = s.Close()
		t.Fatal("claimed unrelated database")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unrelated database was modified")
	}
}
