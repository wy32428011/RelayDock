package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/state"
)

const sampleDetect = "#!/bin/bash\nprintf 'INSTALLED:false\\nRUNNING:false\\n'\n"

func loopback(t *testing.T, exec func(connection *ssh.ServerConn, ch ssh.Channel, cmd string, files sftp.Handlers)) func(context.Context, state.Host) (*ssh.Client, error) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				srv, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				handlers := sftp.InMemHandler()
				for ch := range channels {
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "not a fixture session")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						continue
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer channel.Close()
						for request := range requests {
							var value struct{ Text string }
							if ssh.Unmarshal(request.Payload, &value) != nil {
								_ = request.Reply(false, nil)
								continue
							}
							switch request.Type {
							case "exec":
								_ = request.Reply(true, nil)
								exec(srv, channel, value.Text, handlers)
								return
							case "subsystem":
								if value.Text == "sftp" {
									_ = request.Reply(true, nil)
									server := sftp.NewRequestServer(channel, handlers)
									_ = server.Serve()
									_ = server.Close()
									return
								}
								fallthrough
							default:
								_ = request.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		wg.Wait()
	})
	return func(ctx context.Context, _ state.Host) (*ssh.Client, error) {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp4", listener.Addr().String())
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		c, channels, requests, err := ssh.NewClientConn(conn, listener.Addr().String(), &ssh.ClientConfig{
			User: "fixture", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
		})
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
		return ssh.NewClient(c, channels, requests), nil
	}
}

func exitStatus(ch ssh.Channel, code uint32) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
}

func writeText(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDetectUsesDedicatedLocalSSHAndReusesConnection(t *testing.T) {
	entered := make(chan struct{}, 1)
	continueFirst := make(chan struct{})
	var allow sync.Once
	defer allow.Do(func() { close(continueFirst) })
	var commands, dials atomic.Int32
	dialFn := loopback(t, func(_ *ssh.ServerConn, ch ssh.Channel, cmd string, _ sftp.Handlers) {
		input, err := io.ReadAll(ch)
		if err != nil || !strings.HasSuffix(cmd, "bash --noprofile --norc -s") || string(input) != sampleDetect {
			t.Errorf("unexpected fixture command/input %q, %q, %v", cmd, input, err)
			exitStatus(ch, 1)
			return
		}
		if commands.Add(1) == 1 {
			entered <- struct{}{}
			<-continueFirst
		}
		_, _ = io.WriteString(ch, "INSTALLED:f")
		_, _ = io.WriteString(ch.Stderr(), "stdout/stderr delivered separately\r\n")
		_, _ = io.WriteString(ch, "alse\r\n{\"type\":\"status\",\"running\":false}\n")
		exitStatus(ch, 0)
	})
	db := sampleDB(t)
	hosts := hostsFixture(1)
	root := t.TempDir()
	writeText(t, root, "check.sh", sampleDetect)
	approvedHost := hosts.hosts["host-1"]
	s, err := newService(db, hosts, sampleAssets{root: root}, func(ctx context.Context, host state.Host) (*ssh.Client, error) {
		if host != approvedHost {
			t.Errorf("dial received %+v, want approved host snapshot %+v", host, approvedHost)
		}
		dials.Add(1)
		return dialFn(ctx, host)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	first, _ := createJob(hosts, "host-1", "detect", "test-first")
	second, _ := createJob(hosts, "host-1", "detect", "test-second")
	r, err := s.submit(ctx, []operation{first, second}, true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no fixture command")
	}
	allow.Do(func() { close(continueFirst) })
	for _, id := range r.TaskIDs {
		task := waitForTask(t, s, id, succeeded)
		if task.Result == nil || !no(task.Result.Installed) || !no(task.Result.Running) {
			t.Fatalf("unexpected probe result: %+v", task)
		}
		detail, err := s.get(ctx, taskQuery{TaskID: id})
		if err != nil || len(detail.Logs) < 3 {
			t.Fatalf("missing duplex logs: %+v %v", detail, err)
		}
	}
	if commands.Load() != 2 || dials.Load() != 1 {
		t.Fatalf("expected one connection and two reads (got dials=%d commands=%d)", dials.Load(), commands.Load())
	}
}

func TestWriteConnectionLossDoesNotReplay(t *testing.T) {
	var change, checks atomic.Int32
	dial := loopback(t, func(connection *ssh.ServerConn, ch ssh.Channel, cmd string, _ sftp.Handlers) {
		switch {
		case strings.HasSuffix(cmd, " -- 'check-uninstall' 'amd64' ''"):
			checks.Add(1)
			exitStatus(ch, 0)
		case strings.HasSuffix(cmd, " -- 'uninstall' 'amd64' ''"):
			change.Add(1)
			_, _ = io.WriteString(ch, "PROGRESS:Removing:80\n")
			_ = connection.Close() // no exit status after transmitting a write
		default:
			t.Errorf("unexpected fixture command suffix %q", cmd[max(0, len(cmd)-80):])
			exitStatus(ch, 1)
		}
	})
	hosts := hostsFixture(1)
	hosts.settings.RetryCount = 3
	s, err := newService(sampleDB(t), hosts, sampleAssets{}, dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	op, _ := createJob(hosts, "host-1", "uninstall", "synthetic-uninstall")
	first, err := s.submit(ctx, []operation{op}, false)
	if err != nil {
		t.Fatal(err)
	}
	waitForTask(t, s, first.TaskIDs[0], interrupted)
	retried, err := s.submit(ctx, []operation{op}, false)
	if err != nil || retried.TaskIDs[0] != first.TaskIDs[0] || change.Load() != 1 || checks.Load() != 1 {
		t.Fatalf("replay created a new write: %+v %v, change=%d preflight=%d", retried, err, change.Load(), checks.Load())
	}
}

func TestOfflineInstallWarningOverLoopback(t *testing.T) {
	const (
		name   = "mosquitto_2.1.2-0mosquitto1~noble1_amd64.deb"
		body   = "synthetic package bytes, never interpreted by the SSH fixture"
		secret = "fixture-only-passphrase"
	)
	digest := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(digest[:])
	bundle := payloadFixture(t, []payloadFile{{Path: name, Size: int64(len(body)), SHA256: hash}})
	writeText(t, bundle.Dir, name, body)
	var checks, installs, verifies, cleans atomic.Int32
	dial := loopback(t, func(_ *ssh.ServerConn, ch ssh.Channel, cmd string, files sftp.Handlers) {
		if strings.Contains(cmd, secret) {
			t.Error("secret was sent in an SSH exec argument")
		}
		var code uint32
		switch {
		case strings.HasSuffix(cmd, " -- 'check-install' 'amd64' '2.1.2'"):
			checks.Add(1)
		case strings.HasPrefix(cmd, "mkdir -m 0700 -- '"):
			work := strings.Trim(cmd[len("mkdir -m 0700 -- "):], "'")
			if !stagingPath.MatchString(work) || files.FileCmd.Filecmd(sftp.NewRequest("Mkdir", "/tmp")) != nil ||
				files.FileCmd.Filecmd(sftp.NewRequest("Mkdir", work)) != nil {
				t.Error("staging directory isolated by task ID was not created")
				code = 2
			}
		case strings.Contains(cmd, " -- 'install' 'amd64' '2.1.2' "):
			installs.Add(1)
			suffix := cmd[strings.LastIndex(cmd, " -- 'install' "):]
			args := strings.Fields(suffix)
			if len(args) != 7 || args[5] != "'1883'" || args[6] != "'sample-user'" {
				t.Errorf("unexpected install arguments %v", args)
				code = 2
				break
			}
			input, err := io.ReadAll(ch)
			if err != nil || string(input) != secret+"\n" {
				t.Error("password was not supplied exclusively on SSH stdin")
				code = 2
				break
			}
			work := strings.Trim(args[4], "'")
			if !stagingPath.MatchString(work) || !checkFixtureUpload(files, path.Join(work, "packages", name), body) {
				t.Error("verified bytes were not sent before the write")
				code = 2
				break
			}
			_, _ = io.WriteString(ch, "PROGRESS:Installing:50\n")
			_, _ = io.WriteString(ch.Stderr(), "diagnostic "+secret+"\n")
			code = 1 // writes happened; independent detection must determine the result
		case strings.HasSuffix(cmd, " -- 'detect' 'amd64' '2.1.2'"):
			verifies.Add(1)
			_, _ = io.WriteString(ch, "INSTALLED:true\nRUNNING:true\nVERSION:2.1.2\nPORT:1883\n")
		case strings.HasPrefix(cmd, "rm -rf -- '/tmp/remote-installer."):
			cleans.Add(1)
		default:
			t.Error("unrecognized simulated SSH command")
			code = 2
		}
		exitStatus(ch, code)
	})
	hosts := hostsFixture(1)
	s, err := newService(sampleDB(t), hosts, sampleAssets{bundle: bundle}, dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.pickPayload = func(b assets.Bundle, arch string) ([]payloadFile, error) {
		return mosquittoPayloadWithPins(b, arch, map[string]string{"mosquitto": hash})
	}
	ctx := context.Background()
	op, _ := createJob(hosts, "host-1", "install", "synthetic-install")
	op.Parameters = map[string]string{"username": "sample-user", "password": secret}
	r, err := s.submit(ctx, []operation{op}, false)
	if err != nil {
		t.Fatal(err)
	}
	id := r.TaskIDs[0]
	task := waitForTask(t, s, id, warning)
	if task.Result == nil || task.Result.Version != reviewedMosquittoVersion || !yes(task.Result.Running) ||
		checks.Load() != 1 || installs.Load() != 1 || verifies.Load() != 1 || cleans.Load() != 1 {
		t.Fatalf("incomplete simulated install: %+v", task)
	}
	detail, err := s.get(ctx, taskQuery{TaskID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range detail.Logs {
		if strings.Contains(log.Text, secret) {
			t.Fatal("an install diagnostic leaked its synthetic secret")
		}
	}
}

func checkFixtureUpload(files sftp.Handlers, name, body string) bool {
	request := sftp.NewRequest("Get", name)
	request.Flags = 1 // SSH_FXF_READ
	reader, err := files.FileGet.Fileread(request)
	if err != nil {
		return false
	}
	content := make([]byte, len(body)+1)
	n, err := reader.ReadAt(content, 0)
	return n == len(body) && (err == nil || err == io.EOF) && string(content[:n]) == body
}

func TestTransferRejectsTamperedBytes(t *testing.T) {
	dial := loopback(t, func(_ *ssh.ServerConn, ch ssh.Channel, _ string, _ sftp.Handlers) {
		t.Error("the SFTP fixture must not execute a shell")
		exitStatus(ch, 1)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, state.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	root := t.TempDir()
	writeText(t, root, "bundle.deb", "abc")
	err = uploadPayload(ctx, conn, assets.Bundle{Dir: root}, "/", []payloadFile{{Path: "bundle.deb", Size: 3, SHA256: strings.Repeat("f", 64)}})
	if !hasCode(err, "RESOURCE_INVALID") {
		t.Fatalf("tampered payload not detected: %v", err)
	}
}

func TestWindowsLoaderAndBashParameterQuoting(t *testing.T) {
	var sent string
	body := "$OutputEncoding = [Text.Encoding]::UTF8\nparam(\n [int]$HttpPort = 9200\n)\nWrite-Output $HttpPort"
	p := plan{appID: "elasticsearch", detect: body, parameters: map[string]string{"HTTP_PORT": "10920", "PASSWORD": "never-marshal"}}
	p.host.OS = "windows"
	cmd, input := detectionCommand(p)
	payload, err := base64.StdEncoding.DecodeString(input)
	if err != nil || !strings.HasPrefix(string(payload), "param(") || strings.Contains(cmd+input, "never-marshal") {
		t.Fatalf("Windows script load or parameter separation failed: %q %v", payload, err)
	}
	p.host.OS = "ubuntu"
	p.parameters["DATA_DIR"] = `/example/quoted ' server $ value`
	cmd, input = detectionCommand(p)
	w := lineWriter{send: func(s string) { sent = s }}
	_, _ = w.Write([]byte(cmd + "\n"))
	if !strings.Contains(sent, `DATA_DIR='/example/quoted '"'"' server $ value'`) || input != body || strings.Contains(sent, "PASSWORD") {
		t.Fatalf("bad Bash command or input: %q", sent)
	}
}
