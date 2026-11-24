package assets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"remoteinstaller.local/core/internal/rpc"
)

const (
	cacheDirName = "offline-resources"
	maxResources = 512
	maxPreviews  = 128
	previewTTL   = 10 * time.Minute
)

type preview struct {
	digest  string
	expires time.Time
}

// Store owns the built-in catalog and a private content-addressed import cache.
type Store struct {
	root    string
	cache   string
	catalog []catalogItem
	byID    map[string]int

	mu         sync.Mutex
	previews   map[string]preview
	cacheQuota func(context.Context) (int, error)
	imports    chan struct{}
}

func New(root, userDir string) (*Store, error) {
	if root == "" || userDir == "" {
		return nil, rpc.Error("BAD_PARAMS", "缺少内置资源目录或应用数据目录")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, rpc.Error("CATALOG_INVALID", "无法定位内置资源目录")
	}
	assets, err := openDirectory(absRoot)
	if err != nil {
		return nil, rpc.Error("CATALOG_INVALID", "无法打开内置资源目录")
	}
	defer assets.Close()
	c, err := readCatalog(assets)
	if err != nil {
		return nil, err
	}
	storageErr := rpc.Error("RESOURCE_STORAGE", "无法初始化应用的离线资源缓存")
	data, err := filepath.Abs(userDir)
	if err != nil || os.MkdirAll(data, 0700) != nil {
		return nil, storageErr
	}
	dataRoot, err := openDirectory(data)
	if err != nil {
		return nil, storageErr
	}
	defer dataRoot.Close()
	if err := dataRoot.Mkdir(cacheDirName, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, storageErr
	}
	cache, err := openChildDirectory(dataRoot, cacheDirName)
	if err != nil {
		return nil, storageErr
	}
	cache.Close()
	s := &Store{root: absRoot, cache: filepath.Join(data, cacheDirName), catalog: c, byID: make(map[string]int, len(c)), previews: map[string]preview{}, imports: make(chan struct{}, 1)}
	for i, entry := range c {
		s.byID[entry.ID] = i
	}
	return s, nil
}

// SetCacheQuotaProvider attaches the application setting (in MiB) without
// making the resource package depend on preferences storage. Limits apply to
// new imports; reducing them never deletes resources or affects reads.
func (s *Store) SetCacheQuotaProvider(provider func(context.Context) (int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheQuota = provider
}

func (s *Store) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "catalog.list", "resource.list":
		if err := rpc.Decode(raw, &struct{}{}); err != nil {
			return nil, err
		}
		if method == "catalog.list" {
			return s.apps(), nil
		}
		return s.list(ctx)
	case "resource.inspect":
		var p struct {
			Token string `json:"token"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.inspect(ctx, p.Token)
	case "resource.import":
		var p struct {
			Token  string `json:"token"`
			Digest string `json:"digest"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		return s.importResource(ctx, p.Token, p.Digest)
	default:
		return nil, rpc.Error("UNKNOWN_METHOD", "不支持的资源操作")
	}
}

// In the private core pipe the shell replaces only a validated resource
// directory grant with its absolute path. The renderer never receives or
// supplies this path; the subsequent digest adds a check on reviewed content.
func grantedDirectory(token string) (string, error) {
	if !localAbsolute(token) {
		return "", rpc.Error("RESOURCE_GRANT_REQUIRED", "请通过本机目录选择器授权资源目录")
	}
	return filepath.Clean(token), nil
}

func (s *Store) inspect(ctx context.Context, token string) (Bundle, error) {
	name, err := grantedDirectory(token)
	if err != nil {
		return Bundle{}, err
	}
	root, err := openDirectory(name)
	if err != nil {
		return Bundle{}, rpc.Error("RESOURCE_INVALID", "资源目录不可读，或所选目录是链接")
	}
	defer root.Close()
	plan, err := s.readManifest(root)
	if err != nil {
		return Bundle{}, err
	}
	if err := plan.verify(ctx, root, nil); err != nil {
		return Bundle{}, err
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for dir, entry := range s.previews {
		if !entry.expires.After(now) {
			delete(s.previews, dir)
		}
	}
	if _, exists := s.previews[name]; !exists && len(s.previews) >= maxPreviews {
		return Bundle{}, rpc.Retry("BUSY", "待确认资源过多，请完成已有导入或稍后再试")
	}
	s.previews[name] = preview{digest: plan.bundle.Digest, expires: now.Add(previewTTL)}
	return publicBundle(plan.bundle), nil
}

func (s *Store) takePreview(name, digest string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.previews[name]
	if !ok || p.digest != digest || !p.expires.After(time.Now()) {
		return false
	}
	delete(s.previews, name)
	return true
}

func (s *Store) openCache() (*os.Root, error) {
	root, err := openDirectory(s.cache)
	if err != nil {
		return nil, rpc.Error("RESOURCE_STORAGE", "离线资源缓存不可用")
	}
	return root, nil
}

func (s *Store) importResource(ctx context.Context, token, digest string) (Bundle, error) {
	name, err := grantedDirectory(token)
	if err != nil {
		return Bundle{}, err
	}
	select {
	case s.imports <- struct{}{}:
		defer func() { <-s.imports }()
	default:
		return Bundle{}, rpc.Retry("BUSY", "已有资源正在导入，请稍后再试")
	}
	if !hashString(digest) || !s.takePreview(name, digest) {
		return Bundle{}, rpc.Error("RESOURCE_CONFIRM_REQUIRED", "请先检查资源，并确认同一目录的清单摘要后导入")
	}
	src, err := openDirectory(name)
	if err != nil {
		return Bundle{}, rpc.Error("RESOURCE_CHANGED", "所选资源目录已变化，请重新检查")
	}
	defer src.Close()
	plan, err := s.readManifest(src)
	if err != nil {
		return Bundle{}, err
	}
	if plan.bundle.Digest != digest {
		return Bundle{}, rpc.Error("RESOURCE_CHANGED", "资源清单在确认后发生变化，请重新检查")
	}

	cache, err := s.openCache()
	if err != nil {
		return Bundle{}, err
	}
	defer cache.Close()
	_, existingErr := cache.Lstat(digest)
	if existingErr == nil {
		// Reimport verifies both copies. A corrupted existing resource is not
		// silently overwritten; the user may need it to diagnose the problem.
		if err := plan.verify(ctx, src, nil); err != nil {
			return Bundle{}, err
		}
		b, err := s.resolveCached(ctx, cache, digest, plan.bundle)
		return publicBundle(b), err
	}
	if !errors.Is(existingErr, fs.ErrNotExist) {
		return Bundle{}, rpc.Error("RESOURCE_STORAGE", "无法查询离线资源缓存")
	}
	ids, err := cacheIDs(ctx, cache)
	if err != nil {
		return Bundle{}, err
	}
	if len(ids) >= maxResources {
		return Bundle{}, rpc.Error("RESOURCE_LIMIT", "离线资源缓存已达到资源数量上限")
	}
	if err := s.checkQuota(ctx, cache, ids, plan.bundle.Bytes); err != nil {
		return Bundle{}, err
	}
	stage := ".staging-" + rpc.ID()
	if err := cache.Mkdir(stage, 0700); err != nil {
		return Bundle{}, rpc.Error("RESOURCE_STORAGE", "无法创建资源暂存目录")
	}
	defer cache.RemoveAll(stage) // Only this request's stage; never delete a committed resource.
	tmp, err := openChildDirectory(cache, stage)
	if err != nil {
		return Bundle{}, rpc.Error("RESOURCE_STORAGE", "无法打开资源暂存目录")
	}
	err = plan.verify(ctx, src, tmp)
	if err == nil && writeManifest(tmp, plan.data) != nil {
		err = rpc.Error("RESOURCE_STORAGE", "无法提交资源清单到暂存目录")
	}
	closeErr := tmp.Close() // Release Windows directory handles before rename.
	if err != nil {
		return Bundle{}, err
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	if closeErr != nil || cache.Rename(stage, digest) != nil {
		// Another core process may have committed the same content first.
		if b, err := s.resolveCached(ctx, cache, digest, plan.bundle); err == nil {
			return publicBundle(b), nil
		}
		return Bundle{}, rpc.Error("RESOURCE_STORAGE", "离线资源提交失败，已保留原缓存")
	}
	return publicBundle(plan.bundle), nil
}

func writeManifest(root *os.Root, contents []byte) error {
	f, err := root.OpenFile(manifestName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(contents)
	return errors.Join(err, f.Sync(), f.Close())
}

func (s *Store) checkQuota(ctx context.Context, cache *os.Root, ids []string, newBytes int64) error {
	s.mu.Lock()
	provider := s.cacheQuota
	s.mu.Unlock()
	if provider == nil {
		return nil
	}
	mib, err := provider(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || mib < 1 || mib > 1024*1024 {
		return rpc.Error("RESOURCE_STORAGE", "无法读取离线资源缓存上限")
	}
	limit := rpc.Error("RESOURCE_LIMIT", "导入将超过离线资源缓存上限，请检查设置或释放空间")
	left := (int64(mib) << 20) - newBytes
	if left < 0 {
		return limit
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, plan, err := s.openCached(cache, id)
		if err != nil {
			return rpc.Error("RESOURCE_STORAGE", "存在损坏的离线缓存，无法核对剩余空间")
		}
		root.Close()
		left -= plan.bundle.Bytes
		if left < 0 {
			return limit
		}
	}
	return nil
}

// In addition to the JSON tags, explicitly clear internal paths in RPC results.
func publicBundle(b Bundle) Bundle {
	b.Dir, b.Entrypoint = "", ""
	return b
}

func cacheIDs(ctx context.Context, root *os.Root) ([]string, error) {
	fd, err := root.Open(".")
	if err != nil {
		return nil, rpc.Error("RESOURCE_STORAGE", "无法读取离线资源索引")
	}
	defer fd.Close()
	ids := make([]string, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, readErr := fd.ReadDir(128)
		for _, entry := range entries {
			if entry.IsDir() && hashString(entry.Name()) {
				ids = append(ids, entry.Name())
				if len(ids) > maxResources {
					return nil, rpc.Error("RESOURCE_LIMIT", "离线资源缓存已超过资源数量上限")
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			slices.Sort(ids)
			return ids, nil
		}
		if readErr != nil {
			return nil, rpc.Error("RESOURCE_STORAGE", "无法读取离线资源索引")
		}
	}
}

func (s *Store) list(ctx context.Context) ([]Bundle, error) {
	cache, err := s.openCache()
	if err != nil {
		return nil, err
	}
	defer cache.Close()
	ids, err := cacheIDs(ctx, cache)
	if err != nil {
		return nil, err
	}
	out := make([]Bundle, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root, plan, err := s.openCached(cache, id)
		if err != nil {
			continue
		}
		root.Close()
		out = append(out, publicBundle(plan.bundle))
	}
	return out, nil
}

func (s *Store) openCached(cache *os.Root, id string) (*os.Root, resourcePlan, error) {
	root, err := openChildDirectory(cache, id)
	if err != nil {
		return nil, resourcePlan{}, rpc.Error("RESOURCE_MISMATCH", "所选离线资源不存在或不可用")
	}
	plan, err := s.readManifest(root)
	if err != nil || plan.bundle.Digest != id {
		root.Close()
		return nil, resourcePlan{}, rpc.Error("RESOURCE_INVALID", "离线缓存清单已损坏，不能使用该资源")
	}
	return root, plan, nil
}

func sameTarget(a, b Bundle) bool {
	return a.AppID == b.AppID && a.Version == b.Version && a.OS == b.OS && a.OSVersion == b.OSVersion && a.Arch == b.Arch
}

func (s *Store) resolveCached(ctx context.Context, cache *os.Root, id string, target Bundle) (Bundle, error) {
	root, plan, err := s.openCached(cache, id)
	if err != nil {
		return Bundle{}, err
	}
	defer root.Close()
	if !sameTarget(plan.bundle, target) {
		return Bundle{}, rpc.Error("RESOURCE_MISMATCH", "所选离线资源与应用版本、远端系统版本或 CPU 架构不匹配")
	}
	if err := plan.verify(ctx, root, nil); err != nil {
		return Bundle{}, err
	}
	return plan.bundle, ctx.Err()
}

// Resolve verifies the content of exactly one matching, already imported
// resource, using the remote host's system version and arch. No search in the
// legacy workspace, remote command execution, network request, or fallback.
func (s *Store) Resolve(ctx context.Context, appID, version, osName, osVersion, arch, resourceID string) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	osName, arch = normalizedOS(osName), architecture(arch)
	if arch == "" || !osVersionValid(osVersion) {
		return Bundle{}, rpc.Error("RESOURCE_MISMATCH", "未识别到远端系统版本或 CPU 架构，请重新测试连接")
	}
	i, ok := s.byID[appID]
	if !ok || !slices.Contains(s.catalog[i].Versions, version) || !slices.Contains(s.catalog[i].Platforms, osName) ||
		!knownOfflineCombination(appID, osName, osVersion, arch) {
		return Bundle{}, rpc.Error("RESOURCE_MISMATCH", "不支持该应用版本与远端系统、CPU 架构的离线组合")
	}
	target := Bundle{AppID: appID, Version: version, OS: osName, OSVersion: osVersion, Arch: arch}
	cache, err := s.openCache()
	if err != nil {
		return Bundle{}, err
	}
	defer cache.Close()
	if resourceID != "" {
		if !hashString(resourceID) {
			return Bundle{}, rpc.Error("RESOURCE_MISMATCH", "无效的离线资源 ID")
		}
		return s.resolveCached(ctx, cache, resourceID, target)
	}
	ids, err := cacheIDs(ctx, cache)
	if err != nil {
		return Bundle{}, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return Bundle{}, err
		}
		root, plan, err := s.openCached(cache, id)
		if err != nil {
			continue
		}
		root.Close()
		if !sameTarget(plan.bundle, target) {
			continue
		}
		if resourceID != "" {
			return Bundle{}, rpc.Error("RESOURCE_AMBIGUOUS", "有多个匹配的离线资源，请指定一个")
		}
		resourceID = id
	}
	if resourceID == "" {
		return Bundle{}, rpc.Error("RESOURCE_MISMATCH", "缺少与应用版本、远端系统版本和 CPU 架构完全匹配的离线资源")
	}
	return s.resolveCached(ctx, cache, resourceID, target)
}
