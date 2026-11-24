// Package operations schedules and records built-in application operations.
package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sync"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/remote"
	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

const (
	queued      = "queued"
	preflight   = "preflight"
	running     = "running"
	verifying   = "verifying"
	cancelling  = "cancelling"
	succeeded   = "succeeded"
	warning     = "succeeded_with_warning"
	failed      = "failed"
	cancelled   = "cancelled"
	interrupted = "interrupted"

	maxBatch   = 128
	maxPending = 1000
)

// Task is the durable protocol snapshot. The request and its parameters are kept in memory.
type Task struct {
	ID        string     `json:"id"`
	HostID    string     `json:"hostId"`
	AppID     string     `json:"appId"`
	Version   string     `json:"version"`
	Kind      string     `json:"kind"`
	State     string     `json:"state"`
	Stage     string     `json:"stage"`
	Percent   float64    `json:"percent"`
	Message   string     `json:"message"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
	Seq       int64      `json:"seq"`
	Result    *AppStatus `json:"result,omitempty"`
	BatchID   string     `json:"batchId,omitempty"`
}

type AppStatus struct {
	Installed *bool  `json:"installed"`
	Running   *bool  `json:"running"`
	Version   string `json:"version,omitempty"`
	Port      int    `json:"port,omitempty"`
	Residue   bool   `json:"residue,omitempty"`
}

type TaskLog struct {
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
	At   string `json:"at"`
}

type operation struct {
	HostID     string            `json:"hostId"`
	AppID      string            `json:"appId"`
	Version    string            `json:"version"`
	Kind       string            `json:"kind"`
	RequestKey string            `json:"requestKey"`
	SourceMode string            `json:"sourceMode"`
	ResourceID string            `json:"resourceId"`
	Parameters map[string]string `json:"parameters"`
	KeepData   *bool             `json:"keepData"`
	Confirmed  bool              `json:"confirmed"`
}

type submission struct {
	BatchID string   `json:"batchId"`
	TaskIDs []string `json:"taskIds"`
}

type taskQuery struct {
	TaskID   string `json:"taskId"`
	AfterSeq int64  `json:"afterSeq"`
}

type listParams struct {
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	HostID string `json:"hostId"`
	AppID  string `json:"appId"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
}

type taskDetail struct {
	Task      Task      `json:"task"`
	Logs      []TaskLog `json:"logs"`
	Truncated bool      `json:"truncated,omitempty"`
}

type plan struct {
	host       state.Host
	appID      string
	version    string
	kind       string
	detect     string
	bundle     assets.Bundle
	parameters map[string]string
	redact     redactor
	summary    string
}

type job struct {
	plan     plan
	settings state.Settings
	ctx      context.Context
	cancel   context.CancelFunc
	client   *ssh.Client
	id       string
	hostKey  string

	// These fields are owned by Service.mu.
	task       Task
	changeSent bool
	storeError bool
}

type completion struct {
	state   string
	message string
	status  *AppStatus
}

type hostStore interface {
	Host(context.Context, string) (state.Host, error)
	Settings(context.Context) (state.Settings, error)
}

type applicationStore interface {
	App(string) (assets.App, bool)
	Script(hostOS, appID, version, kind string) (dir, entry string, err error)
	Resolve(ctx context.Context, appID, version, os, osVersion, arch, resourceID string) (assets.Bundle, error)
}

type Service struct {
	db          *sql.DB
	hosts       hostStore
	assets      applicationStore
	dial        func(context.Context, state.Host) (*ssh.Client, error)
	emit        rpc.Emit
	execute     func(*job) (completion, *ssh.Client)
	pickPayload func(assets.Bundle, string) ([]payloadFile, error)

	ctx           context.Context
	stop          context.CancelFunc
	mu            sync.Mutex
	wg            sync.WaitGroup
	closed        bool
	limit         int
	queue         []*job
	jobs          map[string]*job
	busy          map[string]*job
	installActive bool
}

func New(store *state.Store, a *assets.Store, r *remote.Service, emit rpc.Emit) (*Service, error) {
	return newService(store.DB, store, a, r.DialPinned, emit)
}

func newService(db *sql.DB, hosts hostStore, assets applicationStore, dial func(context.Context, state.Host) (*ssh.Client, error), emit rpc.Emit) (*Service, error) {
	if err := initStorage(db); err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Service{
		db: db, hosts: hosts, assets: assets, dial: dial, emit: emit, ctx: ctx, stop: stop,
		jobs: make(map[string]*job), busy: make(map[string]*job), limit: 1,
	}
	s.execute, s.pickPayload = s.executeOperation, mosquittoPayload
	return s, nil
}

func (s *Service) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "operation.validate", "operation.start":
		var p operation
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		if method == "operation.validate" {
			if err := validateShape(p); err != nil {
				return nil, err
			}
			p, err := s.prepare(ctx, p)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "summary": p.summary}, nil
		}
		r, err := s.submit(ctx, []operation{p}, false)
		if err != nil {
			return nil, err
		}
		return map[string]string{"taskId": r.TaskIDs[0]}, nil
	case "operation.batch":
		var p struct {
			Items []operation `json:"items"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.submit(ctx, p.Items, true)
	case "operation.list":
		var p listParams
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.list(ctx, p)
	case "operation.get":
		var p taskQuery
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.get(ctx, p)
	case "operation.cancel", "operation.clearLogs":
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		if !validID(p.TaskID) {
			return nil, rpc.Error("BAD_PARAMS", "任务标识无效")
		}
		var err error
		if method == "operation.cancel" {
			err = s.cancel(ctx, p.TaskID)
		} else {
			err = s.clearLogs(ctx, p.TaskID)
		}
		return map[string]bool{"ok": err == nil}, err
	default:
		return nil, rpc.Error("UNKNOWN_METHOD", "不受支持的任务操作")
	}
}

func (s *Service) submit(ctx context.Context, ops []operation, batch bool) (submission, error) {
	if len(ops) == 0 || len(ops) > maxBatch {
		return submission{}, rpc.Error("BAD_PARAMS", "一次提交须包含 1 到 128 项任务")
	}
	keys := make(map[string]bool, len(ops))
	for _, op := range ops {
		if err := validateShape(op); err != nil {
			return submission{}, err
		}
		if keys[op.RequestKey] {
			return submission{}, rpc.Error("REQUEST_CONFLICT", "批次中的请求标识必须唯一")
		}
		if batch && op.Kind == "install" && (op.AppID == "jdk8" || op.AppID == "jdk11" || op.AppID == "jdk17") {
			return submission{}, rpc.Error("BATCH_UNSUPPORTED", "JDK 安装须逐台提交")
		}
		keys[op.RequestKey] = true
	}

	// An acknowledged or interrupted request keeps its original task even after
	// the host or the offline resources become unavailable.
	s.mu.Lock()
	r, err := s.previousSubmission(ctx, ops, batch)
	s.mu.Unlock()
	if err != nil || len(r.TaskIDs) != 0 {
		return r, err
	}
	plans := make([]plan, len(ops))
	for i, op := range ops {
		p, err := s.prepare(ctx, op)
		if err != nil {
			return submission{}, err
		}
		plans[i] = p
	}
	settings, err := s.hosts.Settings(ctx)
	if err != nil {
		return submission{}, err
	}
	return s.enqueue(ctx, ops, plans, settings, batch)
}

func (s *Service) enqueue(ctx context.Context, ops []operation, plans []plan, settings state.Settings, batch bool) (submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.previousSubmission(ctx, ops, batch)
	if err != nil || len(r.TaskIDs) != 0 {
		return r, err
	}
	if s.closed {
		return submission{}, rpc.Error("CORE_UNAVAILABLE", "任务服务已关闭")
	}
	if len(s.queue)+len(ops) > maxPending {
		return submission{}, rpc.Retry("BUSY", "待执行任务较多，请稍后重试")
	}
	if batch {
		r.BatchID = rpc.ID()
	}
	timestamp := now()
	newJobs := make([]*job, 0, len(ops))
	for _, p := range plans {
		id := rpc.ID()
		t := Task{ID: id, HostID: p.host.ID, AppID: p.appID, Version: p.version, Kind: p.kind, State: queued,
			Stage: queued, Message: "等待执行", Seq: 1, CreatedAt: timestamp, UpdatedAt: timestamp, BatchID: r.BatchID}
		// Separate records pointing at the same trusted host key must not race.
		newJobs = append(newJobs, &job{id: id, hostKey: p.host.Fingerprint, plan: p, settings: settings, task: t})
		r.TaskIDs = append(r.TaskIDs, id)
	}
	if err := insertTasks(ctx, s.db, newJobs, ops); err != nil {
		return submission{}, err
	}
	s.limit = max(1, settings.MaxConcurrentHosts)
	for _, j := range newJobs {
		j.ctx, j.cancel = context.WithCancel(s.ctx)
		s.jobs[j.id] = j
		s.queue = append(s.queue, j)
		s.event("task.state", j.task, j.task)
	}
	s.dispatch()
	return r, nil
}

// dispatch is called with mu held. A blocked install reserves the next install
// slot, and the first queued job for a host reserves that host's next slot.
func (s *Service) dispatch() {
	if s.closed || len(s.busy) >= s.limit {
		return
	}
	waiting := s.queue[:0]
	blockedIDs, blockedKeys := make(map[string]bool), make(map[string]bool)
	for id, j := range s.busy {
		blockedIDs[id], blockedKeys[j.hostKey] = true, true
	}
	installWaiting := s.installActive
	for _, j := range s.queue {
		hostID, hostKey := j.plan.host.ID, j.hostKey
		block := len(s.busy) >= s.limit || blockedIDs[hostID] || blockedKeys[hostKey]
		blockedIDs[hostID], blockedKeys[hostKey] = true, true
		if j.plan.kind == "install" {
			block = block || installWaiting
			installWaiting = true
		}
		if block {
			waiting = append(waiting, j)
			continue
		}
		s.busy[hostID] = j
		if j.plan.kind == "install" {
			s.installActive = true
		}
		s.wg.Add(1)
		go s.work(j)
	}
	clear(s.queue[len(waiting):])
	s.queue = waiting
}

func (s *Service) work(j *job) {
	defer s.wg.Done()
	out := completion{state: failed, message: "任务记录写入失败"}
	var c *ssh.Client
	if s.phase(j, preflight, 2, "正在进行执行前检查") {
		out, c = s.execute(j)
	} else if j.client != nil {
		_ = j.client.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || j.ctx.Err() != nil && j.changeSent {
		out = completion{state: interrupted, message: "任务已中断，远端变更状态未知，请只读复查", status: out.status}
	} else if j.storeError {
		out = completion{state: failed, message: "任务记录写入失败"}
	} else if j.ctx.Err() != nil {
		out = completion{state: cancelled, message: "任务已取消"}
	}
	j.cancel()
	if out.status != nil {
		out.status.Version = j.plan.redact.text(out.status.Version, 96)
	}
	_ = s.transition(j, out.state, out.state, out.message, j.task.Percent, out.status, "task.state")
	delete(s.jobs, j.id)
	delete(s.busy, j.plan.host.ID)
	if j.plan.kind == "install" {
		s.installActive = false
	}
	if c != nil {
		if !s.closed && out.state != cancelled && out.state != interrupted && !j.storeError {
			for _, next := range s.queue {
				if next.hostKey == j.hostKey || next.plan.host.ID == j.plan.host.ID {
					if next.plan.kind == "detect" && sameTarget(next.plan.host, j.plan.host) {
						next.client, c = c, nil
					}
					break
				}
			}
		}
		if c != nil {
			_ = c.Close()
		}
	}
	s.dispatch()
}

func (s *Service) cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		_, err := taskByID(ctx, s.db, id)
		return err
	}
	if s.busy[j.plan.host.ID] == j {
		if j.task.State == cancelling {
			return nil
		}
		err := s.transition(j, cancelling, cancelling, "已请求停止任务", j.task.Percent, nil, "task.state")
		j.cancel()
		return err
	}
	if err := s.transition(j, cancelled, cancelled, "任务已在队列中取消", 0, nil, "task.state"); err != nil {
		return err
	}
	for i, waiting := range s.queue {
		if waiting == j {
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = nil
			s.queue = s.queue[:len(s.queue)-1]
			break
		}
	}
	j.cancel()
	if j.client != nil {
		_ = j.client.Close()
	}
	delete(s.jobs, id)
	s.dispatch()
	return nil
}

func (s *Service) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.stop()
		for _, j := range s.queue {
			if j.client != nil {
				_ = j.client.Close()
			}
			_ = s.transition(j, interrupted, interrupted, "核心已关闭，队列任务需重新提交", 0, nil, "task.state")
			delete(s.jobs, j.id)
		}
		s.queue = nil
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) phase(j *job, state string, percent float64, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return j.ctx.Err() == nil && s.transition(j, state, state, message, percent, nil, "task.state") == nil
}

func (s *Service) beginChange(j *job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.ctx.Err() != nil || j.task.State != running {
		return false
	}
	j.changeSent = true
	return true
}

func (s *Service) transition(j *job, state, stage, message string, percent float64, status *AppStatus, topic string) error {
	if !active(j.task.State) {
		return nil
	}
	t := j.task
	t.State, t.Stage, t.Message, t.Result = state, stage, message, status
	t.Percent = math.Max(t.Percent, percent)
	if state == succeeded || state == warning {
		t.Percent = 100
	}
	t.Seq++
	t.UpdatedAt = now()
	if err := persistTask(s.db, t); err != nil {
		j.storeError = true
		j.cancel()
		return err
	}
	j.task = t
	s.event(topic, t, t)
	return nil
}

func (s *Service) progress(j *job, stage string, percent float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	percent = math.Min(99, percent)
	if j.ctx.Err() == nil && (j.task.State == running || j.task.State == verifying) &&
		(percent > j.task.Percent || percent == j.task.Percent && stage != j.task.Stage) {
		_ = s.transition(j, j.task.State, stage, j.task.Message, percent, nil, "task.progress")
	}
}

func (s *Service) log(j *job, items []TaskLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(items) == 0 || !active(j.task.State) || j.storeError {
		return
	}
	t := j.task
	for i := range items {
		t.Seq++
		items[i].Seq = t.Seq
	}
	t.UpdatedAt = now()
	if err := persistLogs(s.db, t, items); err != nil {
		j.storeError = true
		j.cancel()
		return
	}
	j.task = t
	for _, item := range items {
		s.emitEvent("task.log", j.id, item.Seq, item)
	}
}

func (s *Service) event(topic string, task Task, data any) {
	s.emitEvent(topic, task.ID, task.Seq, data)
}

func (s *Service) emitEvent(topic, id string, seq int64, data any) {
	if s.emit != nil {
		s.emit(rpc.Event{V: rpc.Version, Type: "event", Topic: topic, TaskID: id, Seq: seq, Data: data})
	}
}

func validKind(v string) bool { return v == "detect" || v == "install" || v == "uninstall" }
func active(v string) bool {
	return v == queued || v == preflight || v == running || v == verifying || v == cancelling
}
func validState(v string) bool {
	return active(v) || v == succeeded || v == warning || v == failed || v == cancelled || v == interrupted
}
