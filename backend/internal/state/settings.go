package state

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
	"remoteinstaller.local/core/internal/rpc"
)

type Settings struct {
	Theme                    string `json:"theme"`
	ConnectionTimeoutSeconds int    `json:"connectionTimeoutSeconds"`
	RetryCount               int    `json:"retryCount"`
	RetryIntervalSeconds     int    `json:"retryIntervalSeconds"`
	MaxConcurrentHosts       int    `json:"maxConcurrentHosts"`
	RepositoryURL            string `json:"repositoryUrl"`
	UpdateCheckURL           string `json:"updateCheckUrl"`
	ProxyURL                 string `json:"proxyUrl"`
	CacheLimitMiB            int    `json:"cacheLimitMiB"`
	RepositoryTokenSet       bool   `json:"repositoryTokenSet"`
	ProxyPasswordSet         bool   `json:"proxyPasswordSet"`
}

type settingsInput struct {
	Settings
	RepositoryToken *string `json:"repositoryToken"`
	ProxyPassword   *string `json:"proxyPassword"`
}

const repoSecret = "repositoryToken"
const proxySecret = "proxyPassword"

var preferenceKeys = []string{"theme", "connectionTimeoutSeconds", "retryCount", "retryIntervalSeconds",
	"maxConcurrentHosts", "repositoryUrl", "updateCheckUrl", "proxyUrl", "cacheLimitMiB"}

func defaultSettings() Settings {
	return Settings{Theme: "dark", ConnectionTimeoutSeconds: 60, RetryCount: 3, RetryIntervalSeconds: 5, MaxConcurrentHosts: 3, CacheLimitMiB: 5000}
}

func settingsValues(s Settings) map[string]string {
	return map[string]string{
		"theme": s.Theme, "connectionTimeoutSeconds": strconv.Itoa(s.ConnectionTimeoutSeconds),
		"retryCount": strconv.Itoa(s.RetryCount), "retryIntervalSeconds": strconv.Itoa(s.RetryIntervalSeconds),
		"maxConcurrentHosts": strconv.Itoa(s.MaxConcurrentHosts), "repositoryUrl": s.RepositoryURL,
		"updateCheckUrl": s.UpdateCheckURL, "proxyUrl": s.ProxyURL, "cacheLimitMiB": strconv.Itoa(s.CacheLimitMiB),
	}
}

func validPreference(key, value string) bool {
	if key == "theme" {
		return value == "dark" || value == "light" || value == "system"
	}
	bounds := map[string][2]int{"connectionTimeoutSeconds": {1, 300}, "retryCount": {0, 10},
		"retryIntervalSeconds": {0, 300}, "maxConcurrentHosts": {1, 32}, "cacheLimitMiB": {1, 1024 * 1024}}
	b, ok := bounds[key]
	n, err := strconv.Atoi(value)
	return ok && err == nil && n >= b[0] && n <= b[1]
}

func loadKV(ctx context.Context, q queryer, query string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	v, err := loadKV(ctx, s.DB, `SELECT key,value FROM preferences`)
	if err != nil {
		return Settings{}, err
	}
	cfg := defaultSettings()
	if t := v["theme"]; validPreference("theme", t) {
		cfg.Theme = t
	}
	for k, dst := range map[string]*int{
		"connectionTimeoutSeconds": &cfg.ConnectionTimeoutSeconds, "retryCount": &cfg.RetryCount,
		"retryIntervalSeconds": &cfg.RetryIntervalSeconds, "maxConcurrentHosts": &cfg.MaxConcurrentHosts,
		"cacheLimitMiB": &cfg.CacheLimitMiB,
	} {
		if validPreference(k, v[k]) {
			*dst, _ = strconv.Atoi(v[k])
		}
	}
	cfg.RepositoryURL, cfg.UpdateCheckURL, cfg.ProxyURL = v["repositoryUrl"], v["updateCheckUrl"], v["proxyUrl"]
	return cfg, nil
}

func (s *Store) readSecret(ref string) (string, error) {
	if !validRef(ref) {
		return "", nil
	}
	value, err := s.vault.Get(s.service, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return value, err
}

func (s *Store) getSettings(ctx context.Context) (Settings, error) {
	if err := acquire(ctx, s.secretGate); err != nil {
		return Settings{}, err
	}
	defer func() { <-s.secretGate }()
	cfg, err := s.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	refs, err := loadKV(ctx, s.DB, `SELECT key,ref FROM preference_credentials`)
	if err != nil {
		return Settings{}, err
	}
	repo, err := s.readSecret(refs[repoSecret])
	cfg.RepositoryTokenSet = err == nil && repo != ""
	proxy, err := s.readSecret(refs[proxySecret])
	cfg.ProxyPasswordSet = err == nil && proxy != ""
	return cfg, nil
}

// Endpoints cannot carry passwords, query-string tokens, or URL fragments.
// A proxy username can be stored as non-secret metadata; its password is separate.
func normalizeURL(value string, proxy bool) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	bad := rpc.Error("BAD_PARAMS", "地址无效；请使用有效的服务地址，不要在 URL 中填写密码、令牌、查询参数或片段")
	if !validText(value, 2048, false) || strings.ContainsAny(value, "\\") {
		return nil, bad
	}
	if !proxy && !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u == nil || u.Hostname() == "" || u.Opaque != "" || u.ForceQuery || u.RawQuery != "" || strings.Contains(value, "#") {
		return nil, bad
	}
	port := u.Port()
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return nil, bad
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if proxy {
		if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h" {
			return nil, bad
		}
		if u.Path != "" && u.Path != "/" {
			return nil, bad
		}
		if u.User != nil {
			if _, set := u.User.Password(); set || u.User.Username() == "" || !validText(u.User.Username(), 256, false) {
				return nil, bad
			}
		}
		u.Path, u.RawPath = "", ""
	} else if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, bad
	}
	return u, nil
}

func urlString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}

func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && urlPort(a) == urlPort(b)
}

func writePreference(ctx context.Context, tx *sql.Tx, key, value string) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO preferences(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return requireOne(result, err)
}

func (s *Store) saveSettings(ctx context.Context, input settingsInput) (Settings, error) {
	cfg := input.Settings
	for key, value := range settingsValues(cfg) {
		if !strings.HasSuffix(key, "Url") && !validPreference(key, value) {
			return Settings{}, rpc.Error("BAD_PARAMS", "主题或连接、重试、并发、缓存数值超出有效范围")
		}
	}
	repoURL, err := normalizeURL(cfg.RepositoryURL, false)
	if err != nil {
		return Settings{}, err
	}
	updateURL, err := normalizeURL(cfg.UpdateCheckURL, false)
	if err != nil {
		return Settings{}, err
	}
	proxyURL, err := normalizeURL(cfg.ProxyURL, true)
	if err != nil {
		return Settings{}, err
	}
	cfg.RepositoryURL, cfg.UpdateCheckURL, cfg.ProxyURL = urlString(repoURL), urlString(updateURL), urlString(proxyURL)
	if input.RepositoryToken != nil {
		v := strings.TrimSpace(*input.RepositoryToken)
		if v != "" && (repoURL == nil || repoURL.Scheme != "https" || !validText(v, 4096, false) || strings.ContainsAny(v, " \t")) {
			return Settings{}, rpc.Error("BAD_PARAMS", "仓库令牌需要有效的 HTTPS 仓库地址和不含空格的令牌")
		}
		input.RepositoryToken = &v
	}
	if v := input.ProxyPassword; v != nil && *v != "" {
		if proxyURL == nil || proxyURL.Scheme != "https" || proxyURL.User == nil || !validText(*v, 4096, false) {
			return Settings{}, rpc.Error("BAD_PARAMS", "代理密码需要带用户名的 HTTPS 代理地址，密码请单独填写")
		}
	}
	if err := acquire(ctx, s.secretGate); err != nil {
		return Settings{}, err
	}
	defer func() { <-s.secretGate }()
	defer s.collectSecrets(ctx)
	oldValues, err := loadKV(ctx, s.DB, `SELECT key,value FROM preferences`)
	if err != nil {
		return Settings{}, err
	}
	oldRefs, err := loadKV(ctx, s.DB, `SELECT key,ref FROM preference_credentials`)
	if err != nil {
		return Settings{}, err
	}
	oldRepo, _ := normalizeURL(oldValues["repositoryUrl"], false)
	oldProxy, _ := normalizeURL(oldValues["proxyUrl"], true)
	keepProxy := sameOrigin(oldProxy, proxyURL) && oldProxy.User.Username() == proxyURL.User.Username()
	newRefs := make(map[string]string)
	for _, field := range []struct {
		key   string
		value *string
		same  bool
	}{
		{repoSecret, input.RepositoryToken, sameOrigin(oldRepo, repoURL)},
		{proxySecret, input.ProxyPassword, keepProxy},
	} {
		if field.value == nil && field.same {
			newRefs[field.key] = oldRefs[field.key]
		}
		if field.value != nil && *field.value != "" {
			ref, err := s.stageSecret(ctx, *field.value, false)
			if err != nil {
				return Settings{}, err
			}
			newRefs[field.key] = ref
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	newValues := settingsValues(cfg)
	defaults := settingsValues(defaultSettings())
	for _, key := range preferenceKeys {
		value := newValues[key]
		if old, exists := oldValues[key]; value == old && exists || !exists && value == defaults[key] {
			continue
		}
		if err := writePreference(ctx, tx, key, value); err != nil {
			return Settings{}, err
		}
	}
	for _, key := range []string{repoSecret, proxySecret} {
		old, ref := oldRefs[key], newRefs[key]
		if old == ref {
			continue
		}
		if ref == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM preference_credentials WHERE key=?`, key)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO preference_credentials(key,ref) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET ref=excluded.ref`, key, ref)
		}
		if err != nil {
			return Settings{}, err
		}
		if err := swapRefs(ctx, tx, old, ref); err != nil {
			return Settings{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, err
	}
	v, err := s.readSecret(newRefs[repoSecret])
	cfg.RepositoryTokenSet = err == nil && v != ""
	v, err = s.readSecret(newRefs[proxySecret])
	cfg.ProxyPasswordSet = err == nil && v != ""
	return cfg, nil
}
