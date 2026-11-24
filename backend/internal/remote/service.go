// Package remote implements user-authorized SSH, PTY and SFTP operations.
package remote

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

const (
	probeLifetime      = 3 * time.Minute
	interactiveTimeout = 110 * time.Second // less than the desktop bridge's 120s response deadline
)

type hostStore interface {
	Host(context.Context, string) (state.Host, error)
	CredentialForHost(context.Context, state.Host) (state.Credential, error)
	CredentialRemembered(context.Context, string) (bool, error)
	SaveHostIfCurrent(context.Context, state.Host, state.Credential, bool, *state.Host) (state.Host, error)
	Settings(context.Context) (state.Settings, error)
	Custom(context.Context, string) (state.CustomApp, error)
}

type Service struct {
	store hostStore
	emit  rpc.Emit
	life  context.Context
	stop  context.CancelFunc

	mu         sync.Mutex
	conns      map[*trackedConn]struct{}
	challenges map[probeKey]challenge
	probes     map[string]testedHost
	terminals  map[string]*terminal
	slots      chan struct{}
	mutations  map[string]*mutationGate
}

func New(store *state.Store, emit rpc.Emit) *Service { return newService(store, emit) }

func newService(store hostStore, emit rpc.Emit) *Service {
	life, stop := context.WithCancel(context.Background())
	if emit == nil {
		emit = func(rpc.Event) {}
	}
	s := &Service{
		store: store, emit: emit, life: life, stop: stop,
		conns: make(map[*trackedConn]struct{}), challenges: make(map[probeKey]challenge),
		probes: make(map[string]testedHost), terminals: make(map[string]*terminal),
		slots: make(chan struct{}, 12), mutations: make(map[string]*mutationGate),
	}
	go s.expireProbes()
	return s
}

func (s *Service) Close() {
	s.stop()
	s.mu.Lock()
	clear(s.probes)
	clear(s.challenges)
	clients := make([]*trackedConn, 0, len(s.conns))
	for c := range s.conns {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}

func (s *Service) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.life, cancel)
	if s.life.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (s *Service) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method == "host.probe" || method == "host.save" || method == "terminal.open" {
		bounded, stop := context.WithTimeout(ctx, interactiveTimeout)
		defer stop()
		ctx = bounded
	}
	switch method {
	case "host.probe":
		return s.probe(ctx, raw)
	case "host.save":
		return s.save(ctx, raw)
	case "terminal.open":
		return s.openTerminal(ctx, raw)
	case "terminal.input", "terminal.resize", "terminal.close":
		return s.terminalAction(ctx, method, raw)
	case "files.list", "files.read", "files.write", "files.mkdir", "files.rename", "files.delete", "files.upload", "files.download":
		return s.fileAction(ctx, method, raw)
	case "custom.run":
		return s.runCustom(ctx, raw)
	default:
		return nil, rpc.Error("UNKNOWN_METHOD", "不受支持的远程操作")
	}
}

type okResult struct {
	OK bool `json:"ok"`
}

type trackedConn struct {
	net.Conn
	owner *Service
	once  sync.Once
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()
	})
	return err
}

type mutationGate struct {
	slot chan struct{}
	refs int
}

// Serialize file mutations against the same trusted SSH identity, including
// aliases. SFTP does not lock out other clients or remote programs.
func (s *Service) lockMutations(ctx context.Context, identity string) (func(), error) {
	s.mu.Lock()
	gate := s.mutations[identity]
	if gate == nil {
		gate = &mutationGate{slot: make(chan struct{}, 1)}
		s.mutations[identity] = gate
	}
	gate.refs++
	s.mu.Unlock()
	unref := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		gate.refs--
		if gate.refs == 0 {
			delete(s.mutations, identity)
		}
	}
	select {
	case gate.slot <- struct{}{}:
		return func() { <-gate.slot; unref() }, nil
	case <-ctx.Done():
		unref()
		return nil, ctx.Err()
	}
}

func (s *Service) expireProbes() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.mu.Lock()
			s.pruneProbes(now)
			s.mu.Unlock()
		case <-s.life.Done():
			return
		}
	}
}

func (s *Service) pruneProbes(now time.Time) {
	for key, pending := range s.challenges {
		if !now.Before(pending.expires) {
			delete(s.challenges, key)
		}
	}
	for id, tested := range s.probes {
		if !now.Before(tested.expires) {
			delete(s.probes, id)
		}
	}
}
