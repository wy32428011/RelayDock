package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type updateResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

const maxVersionPayload = 64 * 1024

func (s *Store) checkUpdate(ctx context.Context) (updateResult, error) {
	out := updateResult{Current: s.Version}
	endpoint, proxy, token, message, err := s.updateConfig(ctx)
	if err != nil {
		return out, err
	}
	if message != "" {
		out.Message = message
		return out, nil
	}
	transport := s.transport
	if transport == nil {
		p := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, MaxResponseHeaderBytes: 64 * 1024}
		if proxy != nil {
			p.Proxy = http.ProxyURL(proxy)
		}
		defer p.CloseIdleConnections()
		transport = p
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // A saved endpoint may not redirect credentials elsewhere.
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		out.Message = "更新检测地址无效，请检查系统设置。"
		return out, nil
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Message = "更新检测失败：请检查网络、代理或服务器证书。"
		return out, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Message = fmt.Sprintf("更新检测失败：服务器返回 HTTP %d。", resp.StatusCode)
		return out, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVersionPayload+1))
	if err != nil || len(body) > maxVersionPayload {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Message = "更新检测失败：版本响应过大或不完整。"
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if dec.Decode(&doc) != nil || dec.Decode(new(any)) != io.EOF {
		out.Message = "更新检测失败：版本响应不是有效的 JSON。"
		return out, nil
	}
	latest := strings.TrimSpace(findVersion(doc, 0))
	lv, valid := parseVersion(latest)
	cv, validCurrent := parseVersion(s.Version)
	if !valid || !validCurrent {
		out.Message = "更新检测失败：无法解析版本号。"
		return out, nil
	}
	out.Latest = latest
	out.Available = compareVersion(cv, lv) < 0
	if out.Available {
		out.Message = "发现新版本。此操作不会下载或安装更新。"
	} else {
		out.Message = "当前没有可用的更新。"
	}
	return out, nil
}

func (s *Store) updateConfig(ctx context.Context) (*url.URL, *url.URL, string, string, error) {
	if err := acquire(ctx, s.secretGate); err != nil {
		return nil, nil, "", "", err
	}
	defer func() { <-s.secretGate }()
	cfg, err := s.Settings(ctx)
	if err != nil {
		return nil, nil, "", "", err
	}
	repo, repoErr := normalizeURL(cfg.RepositoryURL, false)
	endpoint, endpointErr := normalizeURL(cfg.UpdateCheckURL, false)
	if repoErr != nil || endpointErr != nil {
		return nil, nil, "", "更新检测地址无效，请检查系统设置。", nil
	}
	if endpoint == nil && repo != nil {
		derived := *repo
		escaped := strings.TrimRight(repo.EscapedPath(), "/") + "/api/version"
		derived.Path, _ = url.PathUnescape(escaped)
		derived.RawPath = escaped
		endpoint = &derived
	}
	if endpoint == nil {
		return nil, nil, "", "尚未配置更新检测地址或仓库地址。", nil
	}
	proxy, err := normalizeURL(cfg.ProxyURL, true)
	if err != nil {
		return nil, nil, "", "代理地址无效，请检查系统设置。", nil
	}
	refs, err := loadKV(ctx, s.DB, `SELECT key,ref FROM preference_credentials`)
	if err != nil {
		return nil, nil, "", "", err
	}
	var token string
	if endpoint.Scheme == "https" && sameOrigin(repo, endpoint) && refs[repoSecret] != "" {
		token, err = s.readSecret(refs[repoSecret])
		if err != nil || token == "" || !validText(token, 4096, false) || strings.ContainsAny(token, " \t") {
			return nil, nil, "", "仓库令牌不可用，请解锁系统凭据库或重新录入。", nil
		}
	}
	if refs[proxySecret] != "" {
		if proxy == nil || proxy.Scheme != "https" || proxy.User == nil {
			return nil, nil, "", "已保存的代理密码与当前代理不匹配，请重新录入。", nil
		}
		password, err := s.readSecret(refs[proxySecret])
		if err != nil || password == "" {
			return nil, nil, "", "代理密码不可用，请解锁系统凭据库或重新录入。", nil
		}
		proxy.User = url.UserPassword(proxy.User.Username(), password)
	}
	return endpoint, proxy, token, "", nil
}

func findVersion(node any, depth int) string {
	obj, ok := node.(map[string]any)
	if !ok || depth >= 6 {
		return ""
	}
	names := sortedKeys(obj)
	for _, key := range []string{"latestVersion", "version", "tag_name", "tagName"} {
		for _, name := range names {
			if !strings.EqualFold(name, key) {
				continue
			}
			switch v := obj[name].(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return v
				}
			case json.Number:
				return v.String()
			}
		}
	}
	// Preserve the old service's support for nested objects. Sort for a stable
	// answer even if response objects contain more than one candidate.
	for _, key := range names {
		if v := findVersion(obj[key], depth+1); v != "" {
			return v
		}
	}
	return ""
}

type versionParts struct {
	nums [4]uint32
	pre  []string
}

func parseVersion(raw string) (versionParts, bool) {
	var out versionParts
	if len(raw) > 80 || raw == "" {
		return out, false
	}
	if raw[0] == 'v' || raw[0] == 'V' {
		raw = raw[1:]
	}
	core, build, hasBuild := strings.Cut(raw, "+")
	if hasBuild && !validVersionIdentifiers(build) {
		return out, false
	}
	core, pre, hasPre := strings.Cut(core, "-")
	if hasPre {
		if !validVersionIdentifiers(pre) {
			return out, false
		}
		out.pre = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) > len(out.nums) {
		return out, false
	}
	for i, p := range parts {
		if !digits(p) {
			return out, false
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return out, false
		}
		out.nums[i] = uint32(n)
	}
	return out, true
}

func validVersionIdentifiers(s string) bool {
	for part := range strings.SplitSeq(s, ".") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func compareVersion(a, b versionParts) int {
	for i, n := range a.nums {
		if n < b.nums[i] {
			return -1
		}
		if n > b.nums[i] {
			return 1
		}
	}
	if len(a.pre) == 0 || len(b.pre) == 0 {
		return len(b.pre) - len(a.pre)
	}
	for i := 0; i < min(len(a.pre), len(b.pre)); i++ {
		x, y := a.pre[i], b.pre[i]
		switch nx, ny := digits(x), digits(y); {
		case nx && ny:
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				return len(x) - len(y)
			}
		case nx:
			return -1
		case ny:
			return 1
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	return len(a.pre) - len(b.pre)
}
