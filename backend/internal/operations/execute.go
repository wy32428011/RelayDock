package operations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/remote"
	"remoteinstaller.local/core/internal/rpc"
)

const (
	detectionTimeout = 45 * time.Second
	writeTimeout     = 20 * time.Minute
	transferTimeout  = 15 * time.Minute
)

func (s *Service) executeOperation(j *job) (completion, *ssh.Client) {
	c := j.client
	keep := false
	defer func() {
		if c != nil && !keep {
			_ = c.Close()
		}
	}()
	if err := s.checkTarget(j); err != nil {
		return operationFailure(err, "主机信息已改变，请重新提交任务"), nil
	}
	var err error
	if c == nil {
		c, err = s.connect(j)
		if err != nil {
			return operationFailure(err, "无法建立 SSH 连接"), nil
		}
	}
	if err := s.checkTarget(j); err != nil {
		return operationFailure(err, "主机信息已改变，请重新提交任务"), nil
	}
	if j.plan.kind == "detect" {
		if !s.phase(j, running, 5, "正在检测应用状态") {
			return completion{state: failed, message: "无法开始检测"}, nil
		}
		var status *AppStatus
		var e evidence
		for attempt := 0; ; attempt++ {
			status, e, err = s.detect(j, c, false)
			if err == nil {
				keep = true
				break
			}
			var fault *rpc.Fault
			if completedCommand(err) || errors.As(err, &fault) && !fault.Retryable || !retry(j, attempt) {
				return completion{failed, "无法确认应用状态，请查看日志", &AppStatus{}}, nil
			}
			_ = c.Close()
			c, err = s.connect(j)
			if err != nil {
				return operationFailure(err, "检测连接已断开，应用状态未知"), nil
			}
			if err := s.checkTarget(j); err != nil {
				return operationFailure(err, "主机信息已改变，请重新提交任务"), nil
			}
		}
		if e.warned || status.Installed == nil || status.Running == nil {
			return completion{warning, "检测已完成，部分状态需要复查", status}, c
		}
		if no(status.Installed) {
			if status.Residue {
				return completion{warning, "应用未安装，检测到保留的数据或配置", status}, c
			}
			return completion{succeeded, "检测完成，应用未安装", status}, c
		}
		return completion{succeeded, "检测完成，应用已安装", status}, c
	}
	return s.mosquittoOperation(j, c), nil
}

func (s *Service) checkTarget(j *job) error {
	h, err := s.hosts.Host(j.ctx, j.plan.host.ID)
	if err != nil {
		return err
	}
	if !sameTarget(h, j.plan.host) {
		return rpc.Error("HOST_CHANGED", "主机地址、指纹或平台已变更，请重新提交任务")
	}
	return trustedHost(h)
}

func (s *Service) connect(j *job) (*ssh.Client, error) {
	for attempt := 0; ; attempt++ {
		c, err := s.dial(j.ctx, j.plan.host)
		if err == nil {
			return c, nil
		}
		var fault *rpc.Fault
		if errors.As(err, &fault) && !fault.Retryable || !retry(j, attempt) {
			return nil, err
		}
		if err := s.checkTarget(j); err != nil {
			return nil, err
		}
	}
}

func retry(j *job, attempt int) bool {
	if j.ctx.Err() != nil || attempt >= min(j.settings.RetryCount, 5) {
		return false
	}
	delay := time.Duration(min(max(0, j.settings.RetryIntervalSeconds), 60)) * time.Second
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return j.ctx.Err() == nil
	case <-j.ctx.Done():
		return false
	}
}

func operationFailure(err error, fallback string) completion {
	var fault *rpc.Fault
	if errors.As(err, &fault) {
		return completion{state: failed, message: safeText(fault.Message, 240)}
	}
	return completion{state: failed, message: fallback}
}

func completedCommand(err error) bool {
	var exit *ssh.ExitError
	return err == nil || errors.As(err, &exit)
}

func (s *Service) detect(j *job, c *ssh.Client, independent bool) (*AppStatus, evidence, error) {
	ctx, cancel := context.WithTimeout(j.ctx, detectionTimeout)
	defer cancel()
	e, err := s.collect(j, func(line func(string)) error {
		if independent {
			return remote.Run(ctx, c, mosquittoCommand(j.plan, "detect"), line)
		}
		cmd, input := detectionCommand(j.plan)
		return runInput(ctx, c, cmd, input, line)
	})
	status := e.status()
	if err != nil {
		var exit *ssh.ExitError
		// The legacy Linux Elasticsearch probe explicitly exits 1 when the
		// package exists but the service is stopped.
		if j.plan.appID != "elasticsearch" || isWindows(j.plan.host.OS) ||
			!errors.As(err, &exit) || exit.ExitStatus() != 1 || !yes(status.Installed) || !no(status.Running) {
			return nil, e, err
		}
	}
	if status.Installed == nil && status.Running == nil || e.outcome == failed {
		return nil, e, rpc.Error("STATUS_UNKNOWN", "检测未报告可信的安装或运行状态")
	}
	return &status, e, nil
}

var powershellParamBlock = regexp.MustCompile(`(?m)^param\([\s\S]*?\n\)[ \t]*(?:\n|$)`)

func detectionCommand(p plan) (cmd, input string) {
	body := p.detect
	if isWindows(p.host.OS) {
		// Some legacy scripts put their param block after the output encoding
		// assignment. PowerShell requires it before the script statements.
		if loc := powershellParamBlock.FindStringIndex(body); loc != nil && loc[0] > 0 {
			body = body[loc[0]:loc[1]] + "\n" + body[:loc[0]] + body[loc[1]:]
		}
		args := ""
		if p.appID == "elasticsearch" && p.parameters["HTTP_PORT"] != "" {
			args = " -HttpPort " + psQuote(p.parameters["HTTP_PORT"])
		}
		if strings.HasPrefix(p.appID, "jdk") && p.parameters["INSTALL_DIR"] != "" {
			args = " -InstallPath " + psQuote(p.parameters["INSTALL_DIR"])
		}
		loader := `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$text = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
& ([ScriptBlock]::Create($text))` + args
		return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(loader), base64.StdEncoding.EncodeToString([]byte(body))
	}
	if p.appID == "mosquitto" {
		// Reading a configuration without an explicit listener returned 1
		// through the legacy pipeline and aborted the whole check under pipefail.
		body = strings.ReplaceAll(body, `| tr -d ';\r\n')"`, `| tr -d ';\r\n' || true)"`)
	}
	cmd = "env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LANG=C "
	for _, key := range []string{"AMQP_PORT", "CONFIG_DIR", "DASHBOARD_PORT", "DATA_DIR", "DNS_PORT", "HTTP_PORT", "HTTPS_PORT", "INSTALL_DIR", "MANAGEMENT_PORT", "MQTT_PORT", "PORT", "REDIS_PORT", "TRANSPORT_PORT"} {
		if value := p.parameters[key]; value != "" {
			cmd += key + "=" + shellQuote(value) + " "
		}
	}
	return cmd + "bash --noprofile --norc -s", body
}

func psQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func encodePowerShell(content string) string {
	units := utf16.Encode([]rune(content))
	b := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(b[2*i:], unit)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// runInput uses a single SSH exec session for Unicode script or secret input.
// Closing the connection also unblocks a server that never acknowledges exec.
func runInput(ctx context.Context, c *ssh.Client, cmd, input string, line func(string)) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	session, err := c.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stdout, stderr := &lineWriter{send: line}, &lineWriter{send: line}
	session.Stdin = strings.NewReader(input)
	session.Stdout, session.Stderr = stdout, stderr
	err = session.Run(cmd)
	stdout.flush()
	stderr.flush()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type lineWriter struct {
	buffer []byte
	long   bool
	send   func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexAny(p, "\r\n")
		if end < 0 {
			end = len(p)
		}
		if !w.long {
			if len(w.buffer)+end > 16*1024 {
				w.long = true
			} else {
				w.buffer = append(w.buffer, p[:end]...)
			}
		}
		if end == len(p) {
			break
		}
		w.flush()
		p = p[end+1:]
	}
	return n, nil
}

func (w *lineWriter) flush() {
	if w.long {
		w.send("[远端单行输出过长，已省略]")
	} else if len(w.buffer) != 0 {
		w.send(string(w.buffer))
	}
	w.buffer = w.buffer[:0]
	w.long = false
}

var _ io.Writer = (*lineWriter)(nil)
