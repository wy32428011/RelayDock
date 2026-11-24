package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

const syntheticSecret = "synthetic-test-password"

type testStore struct {
	mu       sync.Mutex
	h        state.Host
	creds    state.Credential
	remember bool
	app      state.CustomApp
}

func (s *testStore) Host(_ context.Context, id string) (state.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.h.ID && id != "" {
		return s.h, nil
	}
	return state.Host{}, rpc.Error("HOST_NOT_FOUND", "not found")
}

func (s *testStore) CredentialForHost(_ context.Context, want state.Host) (state.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if want != s.h {
		return state.Credential{}, rpc.Error("HOST_CONFLICT", "changed host")
	}
	return s.creds, nil
}

func (s *testStore) CredentialRemembered(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remember, nil
}

func (s *testStore) SaveHostIfCurrent(_ context.Context, h state.Host, c state.Credential, remember bool, prior *state.Host) (state.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (prior == nil && s.h.ID != "") || (prior != nil && *prior != s.h) {
		return state.Host{}, rpc.Error("HOST_CONFLICT", "changed host")
	}
	s.h, s.creds, s.remember = h, c, remember
	return h, nil
}

func (*testStore) Settings(context.Context) (state.Settings, error) {
	return state.Settings{ConnectionTimeoutSeconds: 4}, nil
}

func (s *testStore) Custom(_ context.Context, id string) (state.CustomApp, error) {
	if id == s.app.ID && id != "" {
		return s.app, nil
	}
	return state.CustomApp{}, rpc.Error("CUSTOM_NOT_FOUND", "not found")
}

type sshFixture struct {
	listener net.Listener
	handlers sftp.Handlers
	noSFTP   bool
	windows  bool
	key      ssh.PublicKey
	logins   atomic.Int64
	shells   atomic.Int64
	sizes    chan [2]uint32

	mu      sync.Mutex
	wg      sync.WaitGroup
	signer  ssh.Signer
	conns   map[net.Conn]bool
	log     []string
	stopped bool
}

func newSigner(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, signer
}

func startSSH(t *testing.T, windows, noSFTP bool, key ssh.PublicKey) *sshFixture {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, signer := newSigner(t)
	f := &sshFixture{listener: listener, handlers: sftp.InMemHandler(), windows: windows, noSFTP: noSFTP,
		key: key, signer: signer, sizes: make(chan [2]uint32, 8), conns: make(map[net.Conn]bool)}
	f.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			if f.stopped {
				f.mu.Unlock()
				_ = conn.Close()
				return
			}
			f.conns[conn] = true
			signer := f.signer
			f.mu.Unlock()
			f.wg.Go(func() { f.serve(conn, signer) })
		}
	})
	t.Cleanup(func() {
		_ = listener.Close()
		f.mu.Lock()
		f.stopped = true
		for conn := range f.conns {
			_ = conn.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *sshFixture) rotate(t *testing.T) string {
	t.Helper()
	_, signer := newSigner(t)
	f.mu.Lock()
	f.signer = signer
	f.mu.Unlock()
	return ssh.FingerprintSHA256(signer.PublicKey())
}

func (f *sshFixture) fingerprint() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ssh.FingerprintSHA256(f.signer.PublicKey())
}

func (f *sshFixture) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func (f *sshFixture) serve(conn net.Conn, signer ssh.Signer) {
	defer func() {
		_ = conn.Close()
		f.mu.Lock()
		delete(f.conns, conn)
		f.mu.Unlock()
	}()
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		f.logins.Add(1)
		if c.User() == "operator" && string(password) == syntheticSecret {
			return nil, nil
		}
		return nil, errors.New("test authentication denied")
	}}
	if f.key != nil {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			f.logins.Add(1)
			if c.User() != "operator" || !bytes.Equal(key.Marshal(), f.key.Marshal()) {
				return nil, errors.New("test key denied")
			}
			return nil, nil
		}
	}
	cfg.AddHostKey(signer)
	_, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		if channel.ChannelType() != "session" {
			_ = channel.Reject(ssh.UnknownChannelType, "test sessions only")
			continue
		}
		ch, reqs, err := channel.Accept()
		if err == nil {
			f.wg.Go(func() { f.session(ch, reqs) })
		}
	}
}

const fixturePrompt = "\x1b[32m本地 SSH 夹具\x1b[0m\r\n"

func (f *sshFixture) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term                      string
				Cols, Rows, Width, Height uint32
				Modes                     string
			}
			if ssh.Unmarshal(req.Payload, &p) == nil && p.Term == "xterm-256color" {
				f.sizes <- [2]uint32{p.Rows, p.Cols}
				_ = req.Reply(true, nil)
			} else {
				_ = req.Reply(false, nil)
			}
		case "window-change":
			var p struct{ Cols, Rows, Width, Height uint32 }
			if ssh.Unmarshal(req.Payload, &p) == nil {
				f.sizes <- [2]uint32{p.Rows, p.Cols}
			}
		case "shell":
			f.shells.Add(1)
			_ = req.Reply(true, nil)
			go func() {
				_, _ = io.WriteString(ch, fixturePrompt)
				_, _ = io.Copy(ch, ch) // exact PTY byte echo, including control characters
			}()
		case "subsystem":
			var p struct{ Name string }
			if ssh.Unmarshal(req.Payload, &p) != nil || p.Name != "sftp" || f.noSFTP {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			server := sftp.NewRequestServer(ch, f.handlers, sftp.WithStartDirectory("/"))
			_ = server.Serve()
			_ = server.Close()
			return
		case "exec":
			var p struct{ Command string }
			if ssh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			if p.Command == "test-wait" { // deliberately no exit status until cancelled
				for range reqs {
				}
				return
			}
			status := f.exec(ch, p.Command)
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (f *sshFixture) exec(ch ssh.Channel, cmd string) uint32 {
	f.mu.Lock()
	f.log = append(f.log, cmd)
	f.mu.Unlock()
	if cmd == linuxProbe {
		if f.windows {
			return 1
		}
		_, _ = io.WriteString(ch, "RI_OS=Linux\nRI_ARCH=aarch64\nID=ubuntu\nVERSION_ID=\"24.04\"\n")
		return 0
	}
	if cmd == powershellProbe() && f.windows {
		_, _ = io.WriteString(ch, "RI_OS=Windows\r\nRI_VERSION=10.0.20348\r\nRI_ARCH=X64\r\n")
		return 0
	}
	switch cmd {
	case "test-dual":
		var writers sync.WaitGroup
		writers.Go(func() { _, _ = io.WriteString(ch, strings.Repeat("x", 2<<20)+"\nstdout\n") })
		writers.Go(func() { _, _ = io.WriteString(ch.Stderr(), strings.Repeat("y", 2<<20)+"\r\nstderr") })
		writers.Wait()
	case "test-status":
		_, _ = io.WriteString(ch, "fixture output "+syntheticSecret+"\n")
	case "test-stop", "test-start":
		_, _ = io.WriteString(ch, cmd+" ok\n")
	default:
		return 1
	}
	return 0
}

func (f *sshFixture) draft() hostDraft {
	return hostDraft{Name: "回环夹具", Address: "127.0.0.1", Port: f.listener.Addr().(*net.TCPAddr).Port,
		Username: "operator", Auth: "password", Secret: syntheticSecret}
}

func (f *sshFixture) savedStore() *testStore {
	d := f.draft()
	return &testStore{h: state.Host{ID: "local-host", Name: d.Name, Address: d.Address, Port: d.Port,
		Username: d.Username, Auth: d.Auth, Fingerprint: f.fingerprint(), OS: "ubuntu", CredentialReady: true},
		creds: state.Credential{Secret: syntheticSecret}, remember: true}
}

func startService(t *testing.T, store *testStore, emit rpc.Emit) *Service {
	t.Helper()
	s := newService(store, emit)
	t.Cleanup(s.Close)
	return s
}

func call(s *Service, method string, params any) (any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Handle(ctx, method, raw)
}

func mustCall(t *testing.T, s *Service, method string, params any) any {
	t.Helper()
	result, err := call(s, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var fault *rpc.Fault
	if !errors.As(err, &fault) || fault.Code != code {
		t.Fatalf("fault = %v, want %s", err, code)
	}
}

func TestHostProbeRequiresLiveTrustAndSingleUseSave(t *testing.T) {
	f := startSSH(t, false, false, nil)
	store := &testStore{}
	s := startService(t, store, nil)
	draft := f.draft()
	fingerprint := f.fingerprint()
	_, err := call(s, "host.probe", map[string]any{"host": draft, "acceptFingerprint": fingerprint})
	wantCode(t, err, "TRUST_REQUIRED")
	if f.logins.Load() != 0 {
		t.Fatal("sent credentials before fingerprint discovery")
	}
	untrusted := mustCall(t, s, "host.probe", map[string]any{"host": draft}).(trustResult)
	if untrusted.State != "trust" || untrusted.Changed || untrusted.Fingerprint != fingerprint || f.logins.Load() != 0 {
		t.Fatalf("invalid first trust result: %+v", untrusted)
	}
	other := draft
	other.Username = "different-account"
	_, err = call(s, "host.probe", map[string]any{"host": other, "acceptFingerprint": fingerprint})
	wantCode(t, err, "TRUST_REQUIRED")
	wrong := draft
	wrong.Secret = "incorrect-synthetic-password"
	_, err = call(s, "host.probe", map[string]any{"host": wrong, "acceptFingerprint": fingerprint})
	wantCode(t, err, "AUTH_FAILED")
	ready := mustCall(t, s, "host.probe", map[string]any{"host": draft, "acceptFingerprint": fingerprint}).(readyResult)
	if !ready.SFTP || ready.State != "ready" || ready.Host.OS != "ubuntu" || ready.Host.OSVersion != "24.04" || ready.Host.Arch != "arm64" || ready.Host.LastSeen == "" {
		t.Fatalf("wrong platform/capability: %+v", ready)
	}
	if _, err := s.Dial(context.Background(), ready.Host.ID); err == nil {
		t.Fatal("saved trust before host.save")
	}
	p := map[string]string{"probeId": ready.ProbeID}
	if got := mustCall(t, s, "host.save", p).(state.Host); got != ready.Host {
		t.Fatalf("persisted wrong host: %+v", got)
	}
	_, err = call(s, "host.save", p)
	wantCode(t, err, "PROBE_EXPIRED")
}

func TestHostKeyChangedDuringConfirmationAndExpiredProbe(t *testing.T) {
	f := startSSH(t, false, false, nil)
	s := startService(t, &testStore{}, nil)
	p := map[string]any{"host": f.draft()}
	first := mustCall(t, s, "host.probe", p).(trustResult)
	nextFingerprint := f.rotate(t)
	p["acceptFingerprint"] = first.Fingerprint
	next := mustCall(t, s, "host.probe", p).(trustResult)
	if next.Fingerprint != nextFingerprint || f.logins.Load() != 0 {
		t.Fatal("authenticated after a key changed during confirmation")
	}
	_, err := call(s, "host.probe", p)
	wantCode(t, err, "TRUST_REQUIRED")
	p["acceptFingerprint"] = next.Fingerprint
	ready := mustCall(t, s, "host.probe", p).(readyResult)
	s.mu.Lock()
	cached := s.probes[ready.ProbeID]
	cached.expires = time.Now().Add(-time.Second)
	s.probes[ready.ProbeID] = cached
	s.mu.Unlock()
	_, err = call(s, "host.save", map[string]string{"probeId": ready.ProbeID})
	wantCode(t, err, "PROBE_EXPIRED")
}

func TestHostRotationBlocksAuthenticationAndSaveConflicts(t *testing.T) {
	f := startSSH(t, false, false, nil)
	store := f.savedStore()
	s := startService(t, store, nil)
	fingerprint := f.rotate(t)
	_, err := s.Dial(context.Background(), "local-host")
	wantCode(t, err, "HOST_KEY_CHANGED")
	if f.logins.Load() != 0 {
		t.Fatal("sent credentials to changed host key")
	}
	p := map[string]any{"hostId": "local-host"}
	if got := mustCall(t, s, "host.probe", p).(trustResult); !got.Changed || got.Fingerprint != fingerprint {
		t.Fatalf("rotation not identified: %+v", got)
	}
	p["acceptFingerprint"] = fingerprint
	_, err = call(s, "host.probe", p)
	wantCode(t, err, "HOST_KEY_CHANGED")
	p["replaceFingerprint"] = true
	ready := mustCall(t, s, "host.probe", p).(readyResult)
	mustCall(t, s, "host.save", map[string]string{"probeId": ready.ProbeID})
	if !store.remember || store.h.Fingerprint != fingerprint {
		t.Fatal("did not preserve persistence or update the key")
	}
	delete(p, "acceptFingerprint")
	delete(p, "replaceFingerprint")
	ready = mustCall(t, s, "host.probe", p).(readyResult)
	store.mu.Lock()
	store.h.Name = "modified since probe"
	store.mu.Unlock()
	_, err = call(s, "host.save", map[string]string{"probeId": ready.ProbeID})
	wantCode(t, err, "HOST_CONFLICT")
}

func TestDialPinnedRejectsChangedSavedTarget(t *testing.T) {
	f := startSSH(t, false, false, nil)
	store := f.savedStore()
	s := startService(t, store, nil)
	prior := store.h
	store.h.Port++
	_, err := s.DialPinned(context.Background(), prior)
	wantCode(t, err, "HOST_CONFLICT")
	if f.logins.Load() != 0 {
		t.Fatal("authenticated a stale host snapshot")
	}
}

func TestWindowsProbeWithEncryptedKeyAndNoSFTP(t *testing.T) {
	private, userSigner := newSigner(t)
	f := startSSH(t, true, true, userSigner.PublicKey())
	s := startService(t, &testStore{}, nil)
	phrase := "synthetic-test-passphrase"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "local fixture", []byte(phrase))
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "测试密钥")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	d := f.draft()
	d.Auth, d.Secret, d.KeyToken = "key", "", keyFile
	trust := mustCall(t, s, "host.probe", map[string]any{"host": d}).(trustResult)
	_, err = call(s, "host.probe", map[string]any{"host": d, "acceptFingerprint": trust.Fingerprint})
	wantCode(t, err, "KEY_PASSPHRASE_REQUIRED")
	if f.logins.Load() != 0 {
		t.Fatal("unlocked key somehow attempted authentication")
	}
	d.Secret = phrase
	ready := mustCall(t, s, "host.probe", map[string]any{"host": d, "acceptFingerprint": trust.Fingerprint}).(readyResult)
	if ready.SFTP || ready.Host.OS != "windows" || ready.Host.OSVersion != "10.0.20348" || ready.Host.Arch != "amd64" {
		t.Fatalf("invalid Windows platform/capabilities: %+v", ready)
	}
	mustCall(t, s, "host.save", map[string]string{"probeId": ready.ProbeID})
	_, err = call(s, "files.list", filePath{HostID: ready.Host.ID, Path: "."})
	wantCode(t, err, "SFTP_UNAVAILABLE")
	opened := mustCall(t, s, "terminal.open", map[string]any{"hostId": ready.Host.ID, "rows": 24, "cols": 80}).(map[string]string)
	mustCall(t, s, "terminal.close", opened)
}

func waitSize(t *testing.T, f *sshFixture, want [2]uint32) {
	t.Helper()
	select {
	case got := <-f.sizes:
		if got != want {
			t.Fatalf("PTY size = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PTY resize not received")
	}
}

func TestPTYByteChunksResizeAndClosure(t *testing.T) {
	f := startSSH(t, false, false, nil)
	events := make(chan rpc.Event, 128)
	s := startService(t, f.savedStore(), func(e rpc.Event) { events <- e })
	opened := mustCall(t, s, "terminal.open", map[string]any{"hostId": "local-host", "rows": 24, "cols": 80}).(map[string]string)
	waitSize(t, f, [2]uint32{24, 80})
	id := opened["sessionId"]
	mustCall(t, s, "terminal.resize", map[string]any{"sessionId": id, "rows": 42, "cols": 100})
	waitSize(t, f, [2]uint32{42, 100})
	pasted := []byte("中文\x03\x1b[0m\x00")
	for i := 0; i < 3; i++ {
		mustCall(t, s, "terminal.input", map[string]string{"sessionId": id, "bytesB64": base64.StdEncoding.EncodeToString(pasted)})
	}
	want := append([]byte(fixturePrompt), bytes.Repeat(pasted, 3)...)
	deadline := time.After(2 * time.Second)
	var output []byte
	var seq int64
	for len(output) < len(want) {
		select {
		case event := <-events:
			if event.Topic != "terminal.output" || event.SessionID != id || event.Seq != seq+1 {
				t.Fatalf("bad output event %+v", event)
			}
			seq = event.Seq
			chunk, err := base64.StdEncoding.DecodeString(event.Data.(map[string]string)["bytesB64"])
			if err != nil || len(chunk) > terminalChunk {
				t.Fatal("invalid terminal framing")
			}
			output = append(output, chunk...)
		case <-deadline:
			t.Fatal("PTY output timed out")
		}
	}
	if !bytes.Equal(output, want) {
		t.Fatalf("PTY output = %q, want %q", output, want)
	}
	mustCall(t, s, "terminal.close", opened)
	select {
	case event := <-events:
		if event.Topic != "terminal.closed" || event.SessionID != id || event.Seq != seq+1 {
			t.Fatalf("invalid closed event %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing terminal close event")
	}
	_, err := call(s, "terminal.input", map[string]string{"sessionId": id, "bytesB64": "Aw=="})
	wantCode(t, err, "TERMINAL_CLOSED")
}

func TestRunConsumesBothPipesAndCancels(t *testing.T) {
	f := startSSH(t, false, false, nil)
	s := startService(t, f.savedStore(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.Dial(ctx, "local-host")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var lines []string
	if err := Run(ctx, c, "test-dual", func(line string) { lines = append(lines, line) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 || !slices.Contains(lines, "stdout") || !slices.Contains(lines, "stderr") {
		t.Fatalf("incorrect pipe or long-line handling (%d lines)", len(lines))
	}
	child, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := Run(child, c, "test-wait", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run cancellation: %v", err)
	}
}

func TestHandshakeHonorsContextBeforeHostKey(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if c, err := listener.Accept(); err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c) // never send an SSH identification string
		}
	}()
	s := startService(t, &testStore{}, nil)
	h := state.Host{Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "operator"}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = s.discoverFingerprint(ctx, h, time.Minute)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SSH banner timeout: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed-out socket not closed")
	}
}
