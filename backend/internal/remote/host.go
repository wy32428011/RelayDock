package remote

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

type hostDraft struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Group    string `json:"group"`
	Auth     string `json:"auth"`
	Secret   string `json:"secret"`
	KeyToken string `json:"keyToken"`
	Remember *bool  `json:"remember"`
}

type probeKey struct {
	id, address, username, auth, priorAddress, priorFingerprint string
	port, priorPort                                             int
}

type challenge struct {
	fingerprint string
	changed     bool
	expires     time.Time
}

type testedHost struct {
	host     state.Host
	prior    *state.Host
	cred     state.Credential
	remember bool
	expires  time.Time
}

type trustResult struct {
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint"`
	Changed     bool   `json:"changed"`
}

type readyResult struct {
	State   string     `json:"state"`
	ProbeID string     `json:"probeId"`
	Host    state.Host `json:"host"`
	SFTP    bool       `json:"sftp"`
}

func smallText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func validID(id string) bool { return id != "" && smallText(id, 128) }

func canonicalAddress(address string) (string, bool) {
	a := strings.TrimSpace(address)
	if strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") {
		inner, err := netip.ParseAddr(a[1 : len(a)-1])
		if err != nil || !inner.Is6() {
			return "", false
		}
		a = inner.String()
	}
	if !smallText(a, 253) || a == "" {
		return "", false
	}
	if ip, err := netip.ParseAddr(a); err == nil {
		for _, c := range ip.Zone() {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return "", false
			}
		}
		return ip.String(), true
	}
	a = strings.ToLower(a)
	for _, part := range strings.Split(strings.TrimSuffix(a, "."), ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return "", false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return a, true
}

func validateHost(h *state.Host) error {
	address, ok := canonicalAddress(h.Address)
	if !ok || h.Port < 1 || h.Port > 65535 {
		return rpc.Error("BAD_PARAMS", "请输入有效的主机地址和 SSH 端口（1–65535）")
	}
	h.Address = address
	h.Name = strings.TrimSpace(h.Name)
	h.Username = strings.TrimSpace(h.Username)
	h.Group = strings.TrimSpace(h.Group)
	if h.Name == "" || h.Username == "" || !smallText(h.Name, 240) || !smallText(h.Username, 128) || !smallText(h.Group, 240) || (h.Auth != "password" && h.Auth != "key") || (h.ID != "" && !validID(h.ID)) {
		return rpc.Error("BAD_PARAMS", "请检查主机名称、用户名、分组和认证方式")
	}
	return nil
}

func sameEndpoint(h, prior state.Host) bool {
	address, ok := canonicalAddress(prior.Address)
	return ok && h.Address == address && h.Port == prior.Port
}

func equalFingerprint(left, right string) bool {
	return left != "" && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (s *Service) findHost(ctx context.Context, id string) (state.Host, error) {
	if !validID(id) {
		return state.Host{}, rpc.Error("BAD_PARAMS", "缺少有效的主机 ID")
	}
	host, err := s.store.Host(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		err = rpc.Error("HOST_NOT_FOUND", "主机不存在或已删除")
	}
	return host, err
}

func (s *Service) probe(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Host               *hostDraft `json:"host"`
		HostID             string     `json:"hostId"`
		AcceptFingerprint  string     `json:"acceptFingerprint"`
		ReplaceFingerprint bool       `json:"replaceFingerprint"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if (p.Host == nil) == (p.HostID == "") || len(p.AcceptFingerprint) > 100 || (p.ReplaceFingerprint && p.AcceptFingerprint == "") {
		return nil, rpc.Error("BAD_PARAMS", "请指定一个主机，并使用最近一次探测显示的指纹")
	}

	var h state.Host
	var prior *state.Host
	var input state.Credential
	var remember *bool
	id := p.HostID
	if d := p.Host; d != nil {
		id = d.ID
		h = state.Host{ID: d.ID, Name: d.Name, Address: d.Address, Port: d.Port, Username: d.Username, Group: d.Group, Auth: d.Auth}
		input = state.Credential{Secret: d.Secret, KeyPath: d.KeyToken}
		remember = d.Remember
	}
	if id != "" {
		stored, err := s.findHost(ctx, id)
		if err != nil {
			return nil, err
		}
		prior = &stored
		if p.Host == nil {
			h = stored
		}
	}
	if err := validateHost(&h); err != nil {
		return nil, err
	}
	if len(input.Secret) > 16*1024 || len(input.KeyPath) > 4096 || (h.Auth == "password" && input.KeyPath != "") {
		return nil, rpc.Error("BAD_PARAMS", "SSH 凭据参数无效")
	}
	timeout, err := s.connectionTimeout(ctx)
	if err != nil {
		return nil, err
	}

	key := probeKey{id: id, address: h.Address, port: h.Port, username: h.Username, auth: h.Auth}
	var savedFingerprint string
	if prior != nil {
		key.priorAddress, key.priorPort, key.priorFingerprint = prior.Address, prior.Port, prior.Fingerprint
		if sameEndpoint(h, *prior) {
			savedFingerprint = prior.Fingerprint
		}
	}

	var expected string
	if p.AcceptFingerprint != "" {
		s.mu.Lock()
		pending := s.challenges[key]
		s.mu.Unlock()
		if !time.Now().Before(pending.expires) || !equalFingerprint(pending.fingerprint, p.AcceptFingerprint) {
			return nil, rpc.Error("TRUST_REQUIRED", "此指纹未经过当前主机的有效探测，请重新测试后核对")
		}
		if pending.changed && !p.ReplaceFingerprint {
			return nil, rpc.Error("HOST_KEY_CHANGED", "主机身份已变化，必须单独确认替换指纹")
		}
		expected = pending.fingerprint
	} else {
		// Key discovery stops the SSH handshake before any remote authentication.
		observed, err := s.discoverFingerprint(ctx, h, timeout)
		if err != nil {
			return nil, err
		}
		if !equalFingerprint(savedFingerprint, observed) {
			return s.noteTrust(key, observed), nil
		}
		expected = savedFingerprint
	}

	cred, err := s.credentialsFor(ctx, h, prior, input)
	if err != nil {
		return nil, err
	}
	method, err := authMethod(h.Auth, cred)
	if err != nil {
		return nil, err
	}
	client, observed, err := s.connect(ctx, h, timeout, expected, method)
	if err != nil {
		// A server key that changes between discovery and authentication still
		// never sees credentials. Surface the newly observed key for review.
		if observed != "" && !equalFingerprint(expected, observed) && ctx.Err() == nil {
			return s.noteTrust(key, observed), nil
		}
		return nil, err
	}
	defer client.Close()
	osName, version, arch, err := detectPlatform(ctx, client, timeout)
	if err != nil {
		return nil, err
	}
	canSFTP := testSFTP(ctx, client, timeout)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	keep := false
	if remember != nil {
		keep = *remember
	} else if prior != nil {
		keep, err = s.store.CredentialRemembered(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	if h.ID == "" {
		h.ID = rpc.ID()
	}
	h.Fingerprint, h.OS, h.OSVersion, h.Arch = observed, osName, version, arch
	h.CredentialReady, h.LastSeen = true, time.Now().UTC().Format(time.RFC3339Nano)
	now, probeID := time.Now(), rpc.ID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.pruneProbes(now)
	if len(s.probes) >= 128 {
		return nil, rpc.Retry("PROBE_LIMIT", "等待保存的主机测试过多，请稍后重试")
	}
	delete(s.challenges, key)
	s.probes[probeID] = testedHost{host: h, prior: prior, cred: cred, remember: keep, expires: now.Add(probeLifetime)}
	return readyResult{State: "ready", ProbeID: probeID, Host: h, SFTP: canSFTP}, nil
}

func (s *Service) noteTrust(key probeKey, fingerprint string) trustResult {
	now := time.Now()
	changed := key.priorFingerprint != ""
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneProbes(now)
	if s.life.Err() == nil {
		if len(s.challenges) >= 128 {
			var oldestKey probeKey
			var oldest time.Time
			for k, c := range s.challenges {
				if oldest.IsZero() || c.expires.Before(oldest) {
					oldestKey, oldest = k, c.expires
				}
			}
			delete(s.challenges, oldestKey)
		}
		s.challenges[key] = challenge{fingerprint: fingerprint, changed: changed, expires: now.Add(probeLifetime)}
		// Observing a different identity invalidates uncommitted successful tests
		// for this same target, not only the previous trust prompt.
		for id, tested := range s.probes {
			if tested.host.Address == key.address && tested.host.Port == key.port && !equalFingerprint(tested.host.Fingerprint, fingerprint) {
				delete(s.probes, id)
			}
		}
	}
	return trustResult{State: "trust", Fingerprint: fingerprint, Changed: changed}
}

func (s *Service) credentialsFor(ctx context.Context, h state.Host, prior *state.Host, input state.Credential) (state.Credential, error) {
	hasCredential := input.Secret != ""
	if h.Auth == "key" {
		hasCredential = input.KeyPath != ""
	}
	if hasCredential {
		return input, nil
	}
	if prior != nil && sameEndpoint(h, *prior) && h.Username == prior.Username && h.Auth == prior.Auth {
		stored, err := s.store.CredentialForHost(ctx, *prior)
		if err != nil {
			return state.Credential{}, err
		}
		if h.Auth == "key" && input.Secret != "" {
			stored.Secret = input.Secret
		}
		return stored, nil
	}
	return state.Credential{}, rpc.Error("AUTH_REQUIRED", "请输入当前主机的 SSH 密码或选择私钥")
}

func (s *Service) save(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ProbeID string `json:"probeId"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if !validID(p.ProbeID) {
		return nil, rpc.Error("BAD_PARAMS", "缺少主机测试 ID")
	}
	s.mu.Lock()
	tested, exists := s.probes[p.ProbeID]
	delete(s.probes, p.ProbeID)
	s.mu.Unlock()
	if !exists || !time.Now().Before(tested.expires) {
		return nil, rpc.Error("PROBE_EXPIRED", "测试已过期或已使用，请重新测试后保存")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.store.SaveHostIfCurrent(ctx, tested.host, tested.cred, tested.remember, tested.prior)
}

// Dial opens a dedicated SSH transport. No authentication is sent to a host
// without a persisted and currently matching fingerprint.
func (s *Service) Dial(ctx context.Context, hostID string) (*ssh.Client, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	h, err := s.findHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return s.dialHost(ctx, h)
}

// DialPinned dials a previously approved host snapshot instead of looking up
// its ID again. The store only releases credentials if the endpoint, user,
// auth method and pinned fingerprint still match that snapshot.
func (s *Service) DialPinned(ctx context.Context, h state.Host) (*ssh.Client, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	return s.dialHost(ctx, h)
}

func (s *Service) dialHost(ctx context.Context, h state.Host) (*ssh.Client, error) {
	client, _, err := s.dialHostWithCredential(ctx, h)
	return client, err
}

func (s *Service) dialHostWithCredential(ctx context.Context, h state.Host) (*ssh.Client, string, error) {
	prior := h
	if err := validateHost(&h); err != nil {
		return nil, "", err
	}
	if h.Fingerprint == "" {
		return nil, "", rpc.Error("TRUST_REQUIRED", "请先测试此主机并保存核对过的 SSH 指纹")
	}
	timeout, err := s.connectionTimeout(ctx)
	if err != nil {
		return nil, "", err
	}
	cred, err := s.store.CredentialForHost(ctx, prior)
	if err != nil {
		return nil, "", err
	}
	method, err := authMethod(h.Auth, cred)
	if err != nil {
		return nil, "", err
	}
	client, _, err := s.connect(ctx, h, timeout, h.Fingerprint, method)
	if err != nil {
		return nil, "", err
	}
	return client, cred.Secret, nil
}

func (s *Service) connectionTimeout(ctx context.Context) (time.Duration, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return 0, err
	}
	seconds := settings.ConnectionTimeoutSeconds
	if seconds <= 0 {
		seconds = 15
	}
	return time.Duration(min(seconds, 300)) * time.Second, nil
}

func endpoint(h state.Host) string { return net.JoinHostPort(h.Address, strconv.Itoa(h.Port)) }

func mutationKey(h state.Host) string {
	if h.Fingerprint != "" {
		return h.Fingerprint
	}
	return endpoint(h)
}
