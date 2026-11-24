package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func parseEvidence(text string) evidence {
	var e evidence
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' }) {
		e.consume(line)
	}
	return e
}

func TestInstallDecision(t *testing.T) { testDecisions(t, "install") }

func TestUninstallDecision(t *testing.T) { testDecisions(t, "uninstall") }

func testDecisions(t *testing.T, kind string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "decision-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Kind, Script, Checked, Expected string
		ScriptFailed, Residue                 bool
		Installed, Running                    *bool
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if f.Kind != kind {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			var checked *AppStatus
			if f.Checked != "" {
				status := parseEvidence(f.Checked).status()
				checked = &status
			}
			policy := installDecision
			if kind == "uninstall" {
				policy = uninstallDecision
			}
			out := policy(parseEvidence(f.Script), f.ScriptFailed, checked)
			if out.state != f.Expected {
				t.Fatalf("state = %q, want %q (%+v)", out.state, f.Expected, out)
			}
			status := out.status
			if status == nil {
				status = &AppStatus{}
			}
			if !reflect.DeepEqual(status.Installed, f.Installed) || !reflect.DeepEqual(status.Running, f.Running) || status.Residue != f.Residue {
				t.Fatalf("unexpected result status %+v", status)
			}
		})
	}
}

func TestScriptProtocols(t *testing.T) {
	var e evidence
	for _, line := range []string{"PROGRESS:Installing:40", `{"type":"progress","stage":"Installing","percent":40}`} {
		if got := e.consume(line); got == nil || got.stage != "Installing" || got.percent != 40 {
			t.Fatalf("event %+v for %s", got, line)
		}
	}
	for _, line := range []string{"arbitrary log", `{"type":false}`, `{"type":"status"`, `PROGRESS:Bad:NaN`, `PROGRESS:Bad:Inf`} {
		if got := e.consume(line); got != nil || len(e.fields) != 0 || e.outcome != "" {
			t.Fatalf("ordinary or malformed line parsed as evidence: %s", line)
		}
	}
	if got := e.consume(`{"type":"progress","stage":"Clamped","percent":"999"}`); got == nil || got.percent != 100 {
		t.Fatalf("unclamped percent: %+v", got)
	}
	if got := e.consume("PROGRESS:Clamped:-50"); got == nil || got.percent != 0 {
		t.Fatalf("unclamped percent: %+v", got)
	}
	e.consume(`{"type":"status","installed":false,"running":false,"service_only_stale":true,"port":443}`)
	e.consume("MANAGEMENT_HTTP_READY:true")
	e.consume("REMOTE_ACCESS_AVAILABLE:true")
	st := e.status()
	if !no(st.Installed) || !no(st.Running) || !st.Residue || st.Port != 443 {
		t.Fatalf("configured port or RabbitMQ meta used as runtime evidence: %+v", st)
	}
	e.consume(`{"type":"status","key":"port_listening","value":true}`)
	st = e.status()
	if !yes(st.Running) || !yes(st.Installed) {
		t.Fatalf("runtime not normalized: %+v", st)
	}
	unknown := parseEvidence("INSTALLED:unknown\nPORT:1883").status()
	if unknown.Running != nil || unknown.Installed != nil {
		t.Fatalf("unknown reported as absent: %+v", unknown)
	}
	partial := parseEvidence("INSTALLED:false\nRUNNING:unknown").status()
	if partial.Running != nil || !no(partial.Installed) {
		t.Fatalf("negative installation evidence masked unknown runtime: %+v", partial)
	}
}

func TestLineFraming(t *testing.T) {
	var lines []string
	w := lineWriter{send: func(s string) { lines = append(lines, s) }}
	for _, chunk := range []string{"first\r", "\nINST", "ALLED:false\rRUNNING", ":false\n", strings.Repeat("x", 17<<10), "\nlast"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("unexpected write n=%d err=%v", n, err)
		}
	}
	w.flush()
	want := []string{"first", "INSTALLED:false", "RUNNING:false", "[远端单行输出过长，已省略]", "last"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %v, want %v", lines, want)
	}
}
