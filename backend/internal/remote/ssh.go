package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

var errFingerprint = errors.New("SSH fingerprint not accepted")

func (s *Service) discoverFingerprint(ctx context.Context, h state.Host, timeout time.Duration) (string, error) {
	client, fingerprint, err := s.connect(ctx, h, timeout, "", nil)
	if client != nil { // The callback always rejects in discovery mode.
		_ = client.Close()
		return "", rpc.Error("SSH_HOST_KEY", "无法核对 SSH 主机指纹")
	}
	if fingerprint != "" && ctx.Err() == nil {
		return fingerprint, nil
	}
	return "", err
}

func (s *Service) connect(ctx context.Context, h state.Host, timeout time.Duration, expected string, auth ssh.AuthMethod) (*ssh.Client, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	destination := endpoint(h)
	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "tcp", destination)
	if err != nil {
		return nil, "", sshFault(ctx, err)
	}
	conn := &trackedConn{Conn: raw, owner: s}
	s.mu.Lock()
	if s.life.Err() != nil {
		s.mu.Unlock()
		_ = raw.Close()
		return nil, "", context.Canceled
	}
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	// ssh.ClientConfig.Timeout does not cover the SSH handshake. A socket
	// deadline and context hook also bound key exchange and authentication.
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, "", sshFault(ctx, err)
	}
	stop := context.AfterFunc(ctx, closeTransport(conn))
	defer stop()
	var fingerprint string
	var firstKey sync.Once
	cfg := &ssh.ClientConfig{
		User: h.Username,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			observed := ssh.FingerprintSHA256(key)
			firstKey.Do(func() { fingerprint = observed })
			if !equalFingerprint(expected, observed) {
				return errFingerprint
			}
			return nil
		},
	}
	if auth != nil {
		cfg.Auth = []ssh.AuthMethod{auth}
	}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, destination, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if fingerprint != "" && !equalFingerprint(expected, fingerprint) {
			return nil, fingerprint, rpc.Error("HOST_KEY_CHANGED", "SSH 主机指纹不匹配，连接已阻止，请重新探测并核对")
		}
		return nil, fingerprint, sshFault(ctx, err)
	}
	if !stop() || ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, "", sshFault(ctx, err)
	}
	conn = nil // ownership transfers to ssh.Client
	return ssh.NewClient(sshConn, channels, requests), fingerprint, nil
}

func closeTransport(c io.Closer) func() { return func() { _ = c.Close() } }

func sshFault(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return context.DeadlineExceeded
	}
	if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
		return rpc.Error("AUTH_FAILED", "SSH 认证失败，请检查用户名、密码或私钥")
	}
	return rpc.Retry("SSH_CONNECTION", "无法建立 SSH 连接，请检查目标地址、网络及服务器配置")
}

func authMethod(kind string, cred state.Credential) (ssh.AuthMethod, error) {
	if kind == "password" {
		if cred.Secret == "" || len(cred.Secret) > 16*1024 {
			return nil, rpc.Error("AUTH_REQUIRED", "请输入有效的 SSH 密码")
		}
		return ssh.Password(cred.Secret), nil
	}
	if kind != "key" || cred.KeyPath == "" {
		return nil, rpc.Error("AUTH_REQUIRED", "请在本机重新选择 SSH 私钥")
	}
	root, leaf, err := openLocalParent(cred.KeyPath)
	if err != nil {
		return nil, rpc.Error("KEY_UNAVAILABLE", "无法读取选定的私钥，请在本机重新选择")
	}
	defer root.Close()
	info, statErr := root.Lstat(leaf)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, rpc.Error("KEY_UNAVAILABLE", "私钥须为不超过 1 MiB 的常规文件")
	}
	f, err := openRegular(root, leaf, info)
	if err != nil {
		return nil, rpc.Error("KEY_UNAVAILABLE", "无法读取选定的私钥，请在本机重新选择")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	_ = f.Close()
	defer clear(data)
	if err != nil || len(data) > 1<<20 {
		return nil, rpc.Error("KEY_UNAVAILABLE", "无法读取选定的私钥，请检查文件大小及权限")
	}
	signer, err := ssh.ParsePrivateKey(data)
	var encrypted *ssh.PassphraseMissingError
	if errors.As(err, &encrypted) {
		if cred.Secret == "" {
			return nil, rpc.Error("KEY_PASSPHRASE_REQUIRED", "此私钥已加密，请输入私钥口令")
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(cred.Secret))
		if err != nil {
			return nil, rpc.Error("KEY_PASSPHRASE_INVALID", "无法解锁私钥，请检查口令和密钥格式")
		}
	}
	if err != nil {
		return nil, rpc.Error("KEY_UNAVAILABLE", "SSH 私钥格式不受支持或文件已损坏")
	}
	return ssh.PublicKeys(signer), nil
}

// Run streams both remote output pipes, one complete line per callback. The
// caller supplies a trusted command and decides whether/how to store its lines.
// Cancellation closes the session, and the transport too if the peer stalls.
func Run(ctx context.Context, c *ssh.Client, cmd string, line func(string)) error {
	return runStreams(ctx, c, cmd, line, line)
}

func runStreams(ctx context.Context, c *ssh.Client, cmd string, stdout, stderr func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stopOpen := context.AfterFunc(ctx, closeTransport(c))
	session, err := c.NewSession()
	stopOpen()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		go func() { _ = session.Close() }()
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			_ = c.Close()
		}
	})
	defer func() { close(finished); stop() }()
	defer session.Close()
	// ssh.Session uses independent copy workers. Serialize delivery to callers
	// without serializing the two channel readers or buffering an entire stream.
	var outputMu sync.Mutex
	out, logs := boundedLines{line: stdout, mu: &outputMu}, boundedLines{line: stderr, mu: &outputMu}
	session.Stdout, session.Stderr = &out, &logs
	if err = session.Start(cmd); err == nil {
		err = session.Wait()
		out.flush()
		logs.flush()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

const maxCommandLine = 64 * 1024

type boundedLines struct {
	line     func(string)
	mu       *sync.Mutex
	buffer   []byte
	overflow bool
}

func (w *boundedLines) Write(p []byte) (int, error) {
	n := len(p)
	if w.line == nil {
		return n, nil
	}
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end == -1 {
			w.append(p)
			break
		}
		w.append(p[:end])
		w.deliver()
		p = p[end+1:]
	}
	return n, nil
}

func (w *boundedLines) append(p []byte) {
	if !w.overflow {
		if len(p) > maxCommandLine-len(w.buffer) {
			w.overflow = true
			w.buffer = w.buffer[:0]
			return
		}
		w.buffer = append(w.buffer, p...)
	}
}

func (w *boundedLines) deliver() {
	line := string(bytes.TrimSuffix(w.buffer, []byte{'\r'}))
	if w.overflow {
		line = "[远程输出行过长，已省略]"
	}
	w.mu.Lock()
	w.line(line)
	w.mu.Unlock()
	w.buffer = w.buffer[:0]
	w.overflow = false
}

func (w *boundedLines) flush() {
	if len(w.buffer) != 0 || w.overflow {
		w.deliver()
	}
}

// Linux and Windows probes emit only tagged fields and are never assembled
// from renderer input. Linux distribution data is read, not shell-sourced.
const linuxProbe = `if [ "$(uname -s 2>/dev/null)" = Linux ]; then printf 'RI_OS=Linux\nRI_ARCH=%s\n' "$(uname -m 2>/dev/null)"; if [ -r /etc/os-release ]; then cat /etc/os-release; elif [ -r /usr/lib/os-release ]; then cat /usr/lib/os-release; fi; else exit 1; fi`
const windowsProbe = `[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $v = $null; try { $v = (Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop).Version } catch {}; if (-not $v) { $v = [Environment]::OSVersion.Version.ToString() }; try { $a = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { $a = $env:PROCESSOR_ARCHITEW6432; if (-not $a) { $a = $env:PROCESSOR_ARCHITECTURE } }; [Console]::WriteLine('RI_OS=Windows'); [Console]::WriteLine('RI_VERSION=' + $v); [Console]::WriteLine('RI_ARCH=' + $a)`

func powershellProbe() string {
	chars := utf16.Encode([]rune(windowsProbe))
	data := make([]byte, 2*len(chars))
	for i, rn := range chars {
		binary.LittleEndian.PutUint16(data[2*i:], rn)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
}

func detectPlatform(ctx context.Context, c *ssh.Client, timeout time.Duration) (string, string, string, error) {
	for _, attempt := range []struct{ command, marker string }{{linuxProbe, "Linux"}, {powershellProbe(), "Windows"}} {
		fields := make(map[string]string, 5)
		phase, cancel := context.WithTimeout(ctx, min(timeout, 10*time.Second))
		err := runStreams(phase, c, attempt.command, func(line string) {
			name, value, ok := strings.Cut(strings.TrimPrefix(line, string(rune(0xFEFF))), "=")
			if ok && smallText(value, 128) {
				switch name {
				case "RI_OS", "RI_ARCH", "RI_VERSION", "ID", "VERSION_ID":
					fields[name] = strings.Trim(strings.TrimSpace(value), "\"'")
				}
			}
		}, nil)
		cancel()
		if ctx.Err() != nil {
			return "", "", "", ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", "", "", err
		}
		if err != nil || fields["RI_OS"] != attempt.marker {
			continue
		}
		arch := normalizeArch(fields["RI_ARCH"])
		if attempt.marker == "Windows" {
			return "windows", fields["RI_VERSION"], arch, nil
		}
		name := strings.ToLower(fields["ID"])
		if name == "" || name == "windows" || len(name) > 80 || strings.ContainsFunc(name, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_')
		}) {
			name = "linux"
		}
		return name, fields["VERSION_ID"], arch, nil
	}
	return "", "", "", rpc.Error("PLATFORM_UNKNOWN", "SSH 认证成功，但无法识别远程操作系统；请检查账户的命令执行权限")
}

func normalizeArch(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "x64", "x86_64", "amd64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	case "x86", "i386", "i486", "i586", "i686", "386":
		return "386"
	case "arm", "armv6l", "armv7l":
		return "arm"
	case "ppc64", "ppc64le", "riscv64", "s390x", "loongarch64":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func testSFTP(ctx context.Context, c *ssh.Client, timeout time.Duration) bool {
	phase, cancel := context.WithTimeout(ctx, min(timeout, 4*time.Second))
	defer cancel()
	stop := context.AfterFunc(phase, closeTransport(c))
	defer stop()
	client, _, err := openSFTP(c)
	if err != nil {
		return false
	}
	defer closeSFTP(client, c)
	_, err = client.Getwd()
	return err == nil && phase.Err() == nil
}

func openRegular(root *os.Root, leaf string, before os.FileInfo) (*os.File, error) {
	f, err := root.Open(leaf)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(before, actual) {
		_ = f.Close()
		return nil, errors.New("local file changed")
	}
	return f, nil
}
