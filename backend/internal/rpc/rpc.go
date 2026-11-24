// Package rpc defines the local, bounded stdio protocol shared with Electron.
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
)

const Version = 1
const MaxFrame = 2 * 1024 * 1024

type Fault struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Fault) Error() string         { return e.Message }
func Error(code, message string) error { return &Fault{Code: code, Message: message} }
func Retry(code, message string) error { return &Fault{Code: code, Message: message, Retryable: true} }
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Decode(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' {
		return Error("BAD_PARAMS", "参数必须是对象")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return Error("BAD_PARAMS", "参数格式不符")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Error("BAD_PARAMS", "参数尾部内容无效")
	}
	return nil
}

type Event struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Topic     string `json:"topic"`
	TaskID    string `json:"taskId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	Data      any    `json:"data"`
}
type Emit func(Event)
type Handler func(context.Context, string, json.RawMessage) (any, error)
type request struct {
	V      int             `json:"v"`
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type response struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  *Fault `json:"error,omitempty"`
}

type Server struct {
	out io.Writer
	mu  sync.Mutex
}

func New(out io.Writer) *Server { return &Server{out: out} }
func (s *Server) write(frame any) {
	b, e := json.Marshal(frame)
	if e != nil {
		return
	}
	if len(b) >= MaxFrame {
		if r, ok := frame.(response); ok {
			b, _ = json.Marshal(response{V: Version, Type: "response", ID: r.ID, Error: &Fault{Code: "FRAME_LIMIT", Message: "结果过大，请缩小查询范围"}})
		} else {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}
func (s *Server) Emit(ev Event) { ev.V = Version; ev.Type = "event"; s.write(ev) }
func (s *Server) respond(id string, result any, err error) {
	r := response{V: Version, Type: "response", ID: id, OK: err == nil, Result: result}
	if err != nil {
		r.Result = nil
		if !errors.As(err, &r.Error) {
			if errors.Is(err, context.Canceled) {
				r.Error = &Fault{Code: "CANCELLED", Message: "请求已取消"}
			} else if errors.Is(err, context.DeadlineExceeded) {
				r.Error = &Fault{Code: "TIMEOUT", Message: "操作超时", Retryable: true}
			} else {
				r.Error = &Fault{Code: "INTERNAL", Message: "操作失败；请检查参数、连接和本机权限"}
			}
		}
	}
	s.write(r)
}
func (s *Server) Serve(parent context.Context, input io.Reader, h Handler) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 65536), MaxFrame)
	var wg sync.WaitGroup
	slots := make(chan struct{}, 32)
	ids := map[string]bool{}
	var idMu sync.Mutex
	handshaken := false
	for scan.Scan() {
		var r request
		if json.Unmarshal(scan.Bytes(), &r) != nil || r.Type != "request" || len(r.ID) == 0 || len(r.ID) > 100 || strings.TrimSpace(r.Method) == "" {
			s.respond("", nil, Error("BAD_FRAME", "无效协议消息"))
			continue
		}
		if r.V != Version {
			s.respond(r.ID, nil, Error("VERSION_MISMATCH", "桌面壳与 Go 协议版本不兼容"))
			continue
		}
		if r.Method == "hello" {
			var p struct {
				Supported []int `json:"supported"`
			}
			err := Decode(r.Params, &p)
			ok := false
			for _, v := range p.Supported {
				if v == Version {
					ok = true
				}
			}
			if err == nil && !ok {
				err = Error("VERSION_MISMATCH", "没有共同协议版本")
			}
			if err != nil {
				s.respond(r.ID, nil, err)
				continue
			}
			handshaken = true
			result, e := h(ctx, r.Method, r.Params)
			s.respond(r.ID, result, e)
			continue
		}
		if !handshaken {
			s.respond(r.ID, nil, Error("HANDSHAKE_REQUIRED", "请先完成协议握手"))
			continue
		}
		idMu.Lock()
		exists := ids[r.ID]
		if !exists {
			ids[r.ID] = true
		}
		idMu.Unlock()
		if exists { // Do not send a second response with the same correlation ID.
			continue
		}
		select {
		case slots <- struct{}{}:
			wg.Add(1)
			go func(r request) {
				defer wg.Done()
				defer func() { <-slots; idMu.Lock(); delete(ids, r.ID); idMu.Unlock() }()
				res, e := h(ctx, r.Method, r.Params)
				s.respond(r.ID, res, e)
			}(r)
		default:
			idMu.Lock()
			delete(ids, r.ID)
			idMu.Unlock()
			s.respond(r.ID, nil, Retry("BUSY", "本地服务繁忙，请稍后重试"))
		}
	}
	cancel()
	wg.Wait()
	return scan.Err()
}
