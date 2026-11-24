package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

type sampleHosts struct {
	hosts    map[string]state.Host
	settings state.Settings
}

func (s *sampleHosts) Host(_ context.Context, id string) (state.Host, error) {
	h, ok := s.hosts[id]
	if !ok {
		return state.Host{}, rpc.Error("HOST_NOT_FOUND", "主机不存在")
	}
	return h, nil
}

func (s *sampleHosts) Settings(context.Context) (state.Settings, error) { return s.settings, nil }

type sampleAssets struct {
	root    string
	bundle  assets.Bundle
	resolve func() (assets.Bundle, error)
}

func (s sampleAssets) App(id string) (assets.App, bool) {
	if id != "mosquitto" && id != "mysql" && id != "jdk17" {
		return assets.App{}, false
	}
	return assets.App{ID: id, Label: id, Platforms: []string{"ubuntu", "windows"}, Versions: []string{"2.0.21", reviewedMosquittoVersion}, Params: []assets.Param{
		{Key: "mqtt_port", Label: "MQTT Port", Type: "number", Default: "1883", Required: true},
		{Key: "username", Type: "text"}, {Key: "password", Type: "secret"},
		{Key: "install_dir", Type: "text"},
	}}, true
}

func (s sampleAssets) Script(os, _, _, _ string) (string, string, error) {
	if os == "windows" {
		return s.root, "check.ps1", nil
	}
	return s.root, "check.sh", nil
}

func (s sampleAssets) Resolve(_ context.Context, _, _, _, _, _, _ string) (assets.Bundle, error) {
	if s.resolve != nil {
		return s.resolve()
	}
	return s.bundle, nil
}

func hostsFixture(n int) *sampleHosts {
	h := &sampleHosts{hosts: make(map[string]state.Host), settings: state.Settings{MaxConcurrentHosts: 2}}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("host-%d", i)
		h.hosts[id] = state.Host{ID: id, Address: id + ".test", Port: 22, Username: "fixture", CredentialReady: true,
			Fingerprint: "fixture-" + id, OS: "ubuntu", OSVersion: "24.04", Arch: "amd64"}
	}
	return h
}

func sampleDB(t *testing.T) *sql.DB {
	t.Helper()
	// The SQLite driver is already registered by state. An isolated path means
	// fixtures neither open the application database nor call a credential API.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sample.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func syntheticService(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	s, err := newService(db, hostsFixture(4), sampleAssets{}, func(context.Context, state.Host) (*ssh.Client, error) {
		return nil, errors.New("unexpected network dial in scheduler fixture")
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func createJob(h *sampleHosts, host, kind, key string) (operation, plan) {
	return operation{HostID: host, AppID: "mosquitto", RequestKey: key, Kind: kind, Confirmed: true},
		plan{host: h.hosts[host], appID: "mosquitto", kind: kind}
}

func waitForTask(t *testing.T, s *Service, id, want string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := taskByID(context.Background(), s.db, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.State == want {
			return task
		}
		if !active(task.State) || time.Now().After(deadline) {
			t.Fatalf("task %s was %s, want %s: %s", id, task.State, want, task.Message)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func receiveJob(t *testing.T, started <-chan *job, id string) *job {
	t.Helper()
	select {
	case j := <-started:
		if j.id != id {
			t.Fatalf("started %s (%s %s), want %s", j.id, j.plan.host.ID, j.plan.kind, id)
		}
		return j
	case <-time.After(5 * time.Second):
		t.Fatal("task did not start")
		return nil
	}
}

func TestFIFOAndHostConcurrency(t *testing.T) {
	ctx := context.Background()
	s := syntheticService(t, sampleDB(t))
	h := s.hosts.(*sampleHosts)
	started, releases := make(chan *job, 10), make(map[string]chan struct{})
	for _, host := range []string{"host-1", "host-2", "host-3"} {
		for _, kind := range []string{"detect", "install", "uninstall"} {
			releases[host+kind] = make(chan struct{})
		}
	}
	s.execute = func(j *job) (completion, *ssh.Client) {
		started <- j
		select {
		case <-releases[j.plan.host.ID+j.plan.kind]:
		case <-j.ctx.Done():
		}
		if j.plan.host.ID == "host-1" && j.plan.kind == "install" {
			return completion{failed, "synthetic failure", nil}, nil
		}
		return completion{succeeded, "ok", nil}, nil
	}
	first, firstPlan := createJob(h, "host-1", "detect", "first")
	r1, err := s.enqueue(ctx, []operation{first}, []plan{firstPlan}, h.settings, false)
	if err != nil {
		t.Fatal(err)
	}
	receiveJob(t, started, r1.TaskIDs[0])

	spec := [][2]string{{"host-1", "install"}, {"host-2", "install"}, {"host-3", "detect"}, {"host-1", "uninstall"}}
	var ops []operation
	var plans []plan
	for _, item := range spec {
		op, p := createJob(h, item[0], item[1], item[0]+item[1])
		ops, plans = append(ops, op), append(plans, p)
	}
	r2, err := s.enqueue(ctx, ops, plans, h.settings, true)
	if err != nil {
		t.Fatal(err)
	}
	receiveJob(t, started, r2.TaskIDs[2])
	close(releases["host-3detect"])
	waitForTask(t, s, r2.TaskIDs[2], succeeded)
	select {
	case unexpected := <-started:
		t.Fatalf("install or same-host operation overtook queue: %s", unexpected.id)
	default:
	}
	close(releases["host-1detect"])
	receiveJob(t, started, r2.TaskIDs[0])
	close(releases["host-1install"])
	// Once install 1 actually exits, install 2 and the next host-1 task
	// can start concurrently. Their goroutine start order isn't observable.
	got := make(map[string]bool)
	for range 2 {
		select {
		case j := <-started:
			got[j.id] = true
		case <-time.After(5 * time.Second):
			t.Fatal("queue stopped after a failed task")
		}
	}
	if !got[r2.TaskIDs[1]] || !got[r2.TaskIDs[3]] {
		t.Fatalf("incorrect following tasks: %v", got)
	}
	close(releases["host-2install"])
	close(releases["host-1uninstall"])
	waitForTask(t, s, r2.TaskIDs[0], failed)
	waitForTask(t, s, r2.TaskIDs[1], succeeded)
	waitForTask(t, s, r2.TaskIDs[3], succeeded)
}

func TestBatchDedupSurvivesRestartAndRestoresInterrupted(t *testing.T) {
	ctx := context.Background()
	db := sampleDB(t)
	s := syntheticService(t, db)
	h := s.hosts.(*sampleHosts)
	var ops []operation
	var jobs []*job
	for i, kind := range []string{"install", "detect", "uninstall"} {
		op, p := createJob(h, "host-1", kind, kind+"-request")
		ops = append(ops, op)
		at := now()
		jobs = append(jobs, &job{task: Task{ID: rpc.ID(), BatchID: "synthetic-batch", HostID: op.HostID, AppID: p.appID,
			Kind: kind, Seq: 1, State: []string{running, queued, verifying}[i], Stage: running, CreatedAt: at, UpdatedAt: at}})
	}
	// Seed what would be durable after abrupt process loss (no Close).
	if err := insertTasks(ctx, db, jobs, ops); err != nil {
		t.Fatal(err)
	}
	s2 := syntheticService(t, db)
	// Dedup precedes host/script availability; these assets cannot resolve or execute.
	result, err := s2.submit(ctx, ops, true)
	if err != nil || result.BatchID != "synthetic-batch" || len(result.TaskIDs) != len(jobs) {
		t.Fatalf("dedup result=%+v err=%v", result, err)
	}
	for i, id := range result.TaskIDs {
		task := waitForTask(t, s2, id, interrupted)
		if task.ID != jobs[i].task.ID || task.Seq != 2 {
			t.Fatalf("incorrect recovery: %+v", task)
		}
	}
	if _, err := s2.submit(ctx, []operation{ops[1], ops[0], ops[2]}, true); !hasCode(err, "REQUEST_CONFLICT") {
		t.Fatalf("reordered replay accepted: %v", err)
	}
	if _, err := s2.submit(ctx, ops[:1], true); !hasCode(err, "REQUEST_CONFLICT") {
		t.Fatalf("partial replay accepted: %v", err)
	}
	op := ops[0]
	op.HostID = "host-2"
	if _, err := s2.submit(ctx, []operation{op}, false); !hasCode(err, "REQUEST_CONFLICT") {
		t.Fatalf("request key accepted for a different host: %v", err)
	}
}

func TestCancelQueuedAndRunningWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name        string
		writeMarked bool
		terminal    string
	}{
		{"before write dispatch", false, cancelled},
		{"after write dispatch", true, interrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := syntheticService(t, sampleDB(t))
			h := s.hosts.(*sampleHosts)
			started, finish := make(chan *job, 2), make(chan struct{})
			release := sync.OnceFunc(func() { close(finish) })
			defer release()
			s.execute = func(j *job) (completion, *ssh.Client) {
				if !s.phase(j, running, 20, "test") {
					t.Error("running phase rejected before cancellation")
					return completion{state: failed}, nil
				}
				if tc.writeMarked && !s.beginChange(j) {
					t.Error("initial write rejected before cancellation")
					return completion{state: failed}, nil
				}
				started <- j
				<-j.ctx.Done()
				<-finish // Keep cancelling observable until the test checks it.
				if s.beginChange(j) {
					t.Error("cancellation allowed a new write")
				}
				if s.phase(j, verifying, 94, "late verification") {
					t.Error("cancellation allowed a new phase")
				}
				return completion{succeeded, "late success", nil}, nil
			}
			op1, p1 := createJob(h, "host-1", "install", "write")
			op2, p2 := createJob(h, "host-1", "detect", "read")
			ops := []operation{op1, op2}
			result, err := s.enqueue(ctx, ops, []plan{p1, p2}, h.settings, true)
			if err != nil {
				t.Fatal(err)
			}
			id := result.TaskIDs[0]
			j := receiveJob(t, started, id)
			if err := s.cancel(ctx, result.TaskIDs[1]); err != nil {
				t.Fatal(err)
			}
			waitForTask(t, s, result.TaskIDs[1], cancelled)
			if err := s.cancel(ctx, id); err != nil {
				t.Fatal(err)
			}
			stopping := waitForTask(t, s, id, cancelling)
			release()
			final := waitForTask(t, s, id, tc.terminal)
			if final.Seq != stopping.Seq+1 || final.Percent != stopping.Percent {
				t.Fatalf("cancelling was not followed directly by the terminal state: %+v -> %+v", stopping, final)
			}
			s.progress(j, "late-script-event", 99)
			latest := waitForTask(t, s, id, tc.terminal)
			if !reflect.DeepEqual(final, latest) {
				t.Fatalf("late progress changed final task: %+v", latest)
			}
			replay, err := s.submit(ctx, ops, true)
			if err != nil || !reflect.DeepEqual(result, replay) {
				t.Fatalf("cancelled batch replay did not return original tasks: %+v %v", replay, err)
			}
			select {
			case <-started:
				t.Fatal("cancelled job started")
			default:
			}
		})
	}
}

func TestPersistentRedactedLogsAndEvents(t *testing.T) {
	ctx := context.Background()
	db := sampleDB(t)
	h := hostsFixture(1)
	var events []rpc.Event
	var eventMu sync.Mutex
	s, err := newService(db, h, sampleAssets{}, nil, func(ev rpc.Event) {
		eventMu.Lock()
		defer eventMu.Unlock()
		events = append(events, ev)
		if snapshot, ok := ev.Data.(Task); ok && !active(snapshot.State) {
			actual, err := taskByID(ctx, db, snapshot.ID)
			if err != nil || !reflect.DeepEqual(snapshot, actual) {
				t.Errorf("terminal event preceded the durable state: %+v err=%v", actual, err)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	secret := `s3cret'=sample`
	s.execute = func(j *job) (completion, *ssh.Client) {
		s.phase(j, running, 5, "test")
		_, err := s.collect(j, func(f func(string)) error {
			f("credential=" + secret)
			f("PROGRESS:" + secret + ":10")
			f("PASSWORD: implicit-other-secret")
			for i := range logPage + 3 {
				f(fmt.Sprintf("line %d", i))
			}
			return nil
		})
		if err != nil {
			return completion{failed, "unexpected", nil}, nil
		}
		return completion{warning, "sample warning", &AppStatus{Installed: parseBool("true"), Running: parseBool("false"), Version: secret}}, nil
	}
	op, p := createJob(h, "host-1", "install", "secret-fixture")
	p.redact = newRedactor([]string{secret})
	r, err := s.enqueue(ctx, []operation{op}, []plan{p}, h.settings, false)
	if err != nil {
		t.Fatal(err)
	}
	id := r.TaskIDs[0]
	task := waitForTask(t, s, id, warning)
	if task.Percent != 100 {
		t.Fatalf("warning must be a completed task: %+v", task)
	}
	detail, err := s.get(ctx, taskQuery{TaskID: id})
	if err != nil || !detail.Truncated || len(detail.Logs) != logPage {
		t.Fatalf("wrong bounded log page: %d truncated=%v %v", len(detail.Logs), detail.Truncated, err)
	}
	cursor := detail.Logs[len(detail.Logs)-4].Seq
	partial, err := s.get(ctx, taskQuery{TaskID: id, AfterSeq: cursor})
	if err != nil || partial.Truncated || len(partial.Logs) != 3 {
		t.Fatalf("unexpected cursor result: %d %v", len(partial.Logs), err)
	}
	eventMu.Lock()
	seen := int64(0)
	for _, ev := range events {
		if ev.Seq != seen+1 {
			t.Errorf("unordered event seq %d after %d", ev.Seq, seen)
		}
		seen = ev.Seq
	}
	b, err := json.Marshal(events)
	eventMu.Unlock()
	if err != nil || strings.Contains(string(b), secret) || strings.Contains(string(b), "implicit-other-secret") {
		t.Fatal("event stream contains a secret")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_logs WHERE instr(text, ?) > 0 OR instr(text, ?) > 0`, secret, "implicit-other-secret").Scan(&count); err != nil || count != 0 {
		t.Fatalf("secret in durable logs: %d %v", count, err)
	}
	if err := s.clearLogs(ctx, id); err != nil {
		t.Fatal(err)
	}
	detail, err = s.get(ctx, taskQuery{TaskID: id})
	if err != nil || detail.Truncated || len(detail.Logs) != 0 || detail.Task.State != warning || detail.Task.Seq != task.Seq+1 {
		t.Fatalf("clearing logs rewrote history: %+v %v", detail, err)
	}
}

func hasCode(err error, code string) bool {
	var fault *rpc.Fault
	return errors.As(err, &fault) && fault.Code == code
}
