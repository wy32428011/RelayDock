package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSharedProtocolFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/protocol.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var envelope struct {
			V     int    `json:"v"`
			Type  string `json:"type"`
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil || envelope.V != Version {
			t.Fatal(string(scanner.Bytes()), err)
		}
		switch envelope.Type {
		case "request":
			var r request
			if err := json.Unmarshal(scanner.Bytes(), &r); err != nil || r.ID == "" || r.Method == "" {
				t.Fatal(r, err)
			}
		case "response":
			var r response
			if err := json.Unmarshal(scanner.Bytes(), &r); err != nil || r.OK || r.Error.Code != "BAD_PARAMS" {
				t.Fatal(r, err)
			}
		case "event":
			if envelope.Topic == "" {
				t.Fatal("missing topic")
			}
		default:
			t.Fatal("unknown type")
		}
		count++
	}
	if scanner.Err() != nil || count != 5 {
		t.Fatal(count, scanner.Err())
	}
}

func TestHandshakeAndUnknownParams(t *testing.T) {
	in, out := io.Pipe()
	input, output := io.Pipe()
	defer in.Close()
	defer out.Close()
	defer input.Close()
	defer output.Close()
	result := make(chan error, 1)
	s := New(out)
	go func() {
		result <- s.Serve(context.Background(), input, func(_ context.Context, _ string, p json.RawMessage) (any, error) {
			return map[string]int{"selected": 1}, nil
		})
	}()
	reader := bufio.NewReader(in)
	call := func(line string) response {
		t.Helper()
		if _, err := io.WriteString(output, line+"\n"); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var r response
		if err = json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(`{"v":1,"type":"request","id":"a","method":"host.list","params":{}}`); r.Error == nil || r.Error.Code != "HANDSHAKE_REQUIRED" {
		t.Fatal(r)
	}
	if r := call(`{"v":2,"type":"request","id":"b","method":"hello","params":{"supported":[2]}}`); r.Error == nil || r.Error.Code != "VERSION_MISMATCH" {
		t.Fatal(r)
	}
	if r := call(`{"v":1,"type":"request","id":"c","method":"hello","params":{"supported":[1],"unwanted":true}}`); r.Error == nil || r.Error.Code != "BAD_PARAMS" {
		t.Fatal(r)
	}
	if r := call(`{"v":1,"type":"request","id":"d","method":"hello","params":{"supported":[1]}}`); !r.OK || r.ID != "d" {
		t.Fatal(r)
	}
	if r := call(`{"v":1,"type":"request","id":"e","method":"host.list","params":{}}`); !r.OK || r.ID != "e" {
		t.Fatal(r)
	}
	_ = output.Close()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsNonObject(t *testing.T) {
	for _, v := range []string{"   ", `[]`, `null`, `true`, `{} {}`, `{"bad":2}`} {
		var d struct{}
		if err := Decode([]byte(v), &d); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
	if err := Decode([]byte(`{}`), new(struct{})); err != nil {
		t.Fatal(err)
	}
}

func TestFrameLimit(t *testing.T) {
	var output bytes.Buffer
	if err := New(&output).Serve(context.Background(), strings.NewReader(strings.Repeat("x", MaxFrame+1)), func(context.Context, string, json.RawMessage) (any, error) {
		t.Error("handler called")
		return nil, nil
	}); err == nil {
		t.Fatal("unbounded frame accepted")
	}
}
