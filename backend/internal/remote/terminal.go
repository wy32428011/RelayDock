package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
)

const terminalChunk = 16 * 1024

type terminal struct {
	id      string
	client  *ssh.Client
	session *ssh.Session
	input   io.Writer
	closing atomic.Bool
	done    chan struct{}
	actions chan struct{}
	output  *terminalOutput
}

type terminalOutput struct {
	emit rpc.Emit
	id   string
	mu   sync.Mutex
	seq  int64
}

func (o *terminalOutput) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	size := len(b)
	for len(b) > 0 {
		n := min(terminalChunk, len(b))
		o.seq++
		o.emit(rpc.Event{Topic: "terminal.output", SessionID: o.id, Seq: o.seq,
			Data: map[string]string{"bytesB64": base64.StdEncoding.EncodeToString(b[:n])}})
		b = b[n:]
	}
	return size, nil
}

func (o *terminalOutput) closed(reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seq++
	o.emit(rpc.Event{Topic: "terminal.closed", SessionID: o.id, Seq: o.seq, Data: map[string]string{"reason": reason}})
}

func validDimensions(rows, cols int) bool {
	return rows > 0 && rows <= 1000 && cols > 0 && cols <= 1000
}

func (s *Service) openTerminal(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		HostID string `json:"hostId"`
		Rows   int    `json:"rows"`
		Cols   int    `json:"cols"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if !validDimensions(p.Rows, p.Cols) {
		return nil, rpc.Error("BAD_PARAMS", "终端行列数须为 1–1000")
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, rpc.Retry("TERMINAL_LIMIT", "终端会话已达上限，请先关闭不使用的终端")
	}
	var client *ssh.Client
	transferred := false
	defer func() {
		if !transferred {
			if client != nil {
				_ = client.Close()
			}
			<-s.slots
		}
	}()
	var err error
	client, err = s.Dial(ctx, p.HostID)
	if err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	stop := context.AfterFunc(setup, closeTransport(client))
	defer stop()
	t := &terminal{id: rpc.ID(), client: client, done: make(chan struct{}), actions: make(chan struct{}, 1)}
	t.output = &terminalOutput{id: t.id, emit: s.emit}
	t.session, err = client.NewSession()
	if err == nil {
		t.input, err = t.session.StdinPipe()
	}
	if err == nil {
		t.session.Stdout, t.session.Stderr = t.output, t.output
		err = t.session.RequestPty("xterm-256color", p.Rows, p.Cols, ssh.TerminalModes{})
	}
	if err == nil {
		err = t.session.Shell()
	}
	if !stop() || setup.Err() != nil {
		return nil, setup.Err()
	}
	if err != nil {
		return nil, rpc.Error("TERMINAL_UNAVAILABLE", "远程 SSH 服务未开启 PTY 或无法启动登录 Shell")
	}
	s.mu.Lock()
	if s.life.Err() != nil {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	s.terminals[t.id] = t
	s.mu.Unlock()
	transferred = true
	go s.waitTerminal(t)
	return map[string]string{"sessionId": t.id}, nil
}

func (s *Service) waitTerminal(t *terminal) {
	err := t.session.Wait()
	_ = t.client.Close()
	close(t.done)
	s.mu.Lock()
	delete(s.terminals, t.id)
	s.mu.Unlock()
	<-s.slots
	reason := "远程终端连接已中断。"
	var exit *ssh.ExitError
	if t.closing.Load() || s.life.Err() != nil {
		reason = "终端已关闭。"
	} else if err == nil || errors.As(err, &exit) {
		reason = "远程 Shell 已退出。"
	}
	t.output.closed(reason)
}

func (s *Service) terminalAction(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var id string
	var apply func(*terminal) error
	switch method {
	case "terminal.input":
		var p struct {
			SessionID string `json:"sessionId"`
			BytesB64  string `json:"bytesB64"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		if len(p.BytesB64) == 0 || len(p.BytesB64) > base64.StdEncoding.EncodedLen(terminalChunk) {
			return nil, rpc.Error("BAD_PARAMS", "终端输入块应为不超过 16 KiB 的字节")
		}
		block, err := base64.StdEncoding.Strict().DecodeString(p.BytesB64)
		if err != nil || len(block) == 0 || len(block) > terminalChunk || base64.StdEncoding.EncodeToString(block) != p.BytesB64 {
			return nil, rpc.Error("BAD_PARAMS", "终端输入不是有效的 Base64 数据块")
		}
		id = p.SessionID
		apply = func(t *terminal) error {
			n, err := t.input.Write(block)
			if err == nil && n != len(block) {
				return io.ErrShortWrite
			}
			return err
		}
	case "terminal.resize":
		var p struct {
			SessionID string `json:"sessionId"`
			Rows      int    `json:"rows"`
			Cols      int    `json:"cols"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		if !validDimensions(p.Rows, p.Cols) {
			return nil, rpc.Error("BAD_PARAMS", "终端行列数须为 1–1000")
		}
		id = p.SessionID
		apply = func(t *terminal) error { return t.session.WindowChange(p.Rows, p.Cols) }
	case "terminal.close":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		id = p.SessionID
	}
	if !validID(id) {
		return nil, rpc.Error("BAD_PARAMS", "缺少有效的终端会话 ID")
	}
	s.mu.Lock()
	t := s.terminals[id]
	s.mu.Unlock()
	if method == "terminal.close" {
		if t != nil {
			t.closing.Store(true)
			_ = t.client.Close()
		}
		return okResult{OK: true}, nil
	}
	if t == nil {
		return nil, terminalClosed()
	}
	phase, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-t.done:
		return nil, terminalClosed()
	case <-phase.Done():
		return nil, phase.Err()
	case t.actions <- struct{}{}:
		defer func() { <-t.actions }()
	}
	if err := phase.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(phase, closeTransport(t.client))
	err := apply(t)
	stop()
	if phase.Err() != nil {
		return nil, phase.Err()
	}
	if err != nil {
		return nil, terminalClosed()
	}
	return okResult{OK: true}, nil
}

func terminalClosed() error {
	return rpc.Error("TERMINAL_CLOSED", "终端会话已结束，请重新连接")
}
