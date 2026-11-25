package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"remoteinstaller.local/core/internal/rpc"
)

const (
	maxTextBytes = 256 * 1024 // even worst-case JSON escaping fits a protocol frame
	fileTimeout  = 45 * time.Second
	transferTime = 25 * time.Minute
)

type filePath struct {
	HostID string `json:"hostId"`
	Path   string `json:"path"`
}

type fileEntry struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Size       int64   `json:"size"`
	Directory  bool    `json:"directory"`
	ModifiedAt string  `json:"modifiedAt,omitempty"`
	Link       bool    `json:"link,omitempty"`
	Mode       *uint32 `json:"mode,omitempty"`
}

type directoryResult struct {
	Path    string      `json:"path"`
	Entries []fileEntry `json:"entries"`
}

type textResult struct {
	Content string `json:"content"`
	Digest  string `json:"digest"`
}

func (s *Service) fileAction(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var target filePath
	var apply func(context.Context, *remoteFS, string) (any, error)
	mutating := true
	timeout := fileTimeout
	confirmed := true
	switch method {
	case "files.list", "files.read", "files.mkdir":
		if err := rpc.Decode(raw, &target); err != nil {
			return nil, err
		}
		mutating = method == "files.mkdir"
		apply = func(ctx context.Context, f *remoteFS, p string) (any, error) {
			switch method {
			case "files.list":
				return f.list(ctx, p)
			case "files.read":
				text, _, err := f.readText(p)
				return text, err
			default:
				return okResult{OK: true}, f.mkdir(p)
			}
		}
	case "files.write":
		var p struct {
			filePath
			Content        string `json:"content"`
			ExpectedDigest string `json:"expectedDigest"`
			Confirmed      bool   `json:"confirmed"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, confirmed = p.filePath, p.Confirmed
		if len(p.Content) > maxTextBytes || !utf8.ValidString(p.Content) || strings.IndexByte(p.Content, 0) >= 0 {
			return nil, rpc.Error("FILE_TEXT_LIMIT", "编辑器仅支持不超过 256 KiB 的 UTF-8 文本文件")
		}
		if len(p.ExpectedDigest) != sha256.Size*2 {
			return nil, rpc.Error("BAD_PARAMS", "请读取远程文件并提供有效的 SHA-256 摘要")
		}
		if _, err := hex.DecodeString(p.ExpectedDigest); err != nil {
			return nil, rpc.Error("BAD_PARAMS", "请读取远程文件并提供有效的 SHA-256 摘要")
		}
		apply = func(ctx context.Context, f *remoteFS, file string) (any, error) {
			return f.writeText(ctx, file, p.Content, strings.ToLower(p.ExpectedDigest))
		}
	case "files.rename":
		var p struct {
			filePath
			To        string `json:"to"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, confirmed = p.filePath, p.Confirmed
		if p.To == "" || !smallText(p.To, maxRemotePath) {
			return nil, remotePathError()
		}
		apply = func(_ context.Context, f *remoteFS, from string) (any, error) {
			to, err := f.resolve(p.To)
			if err != nil {
				return nil, err
			}
			return okResult{OK: true}, f.rename(from, to)
		}
	case "files.delete":
		var p struct {
			filePath
			Recursive bool `json:"recursive"`
			Confirmed bool `json:"confirmed"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, confirmed = p.filePath, p.Confirmed
		apply = func(ctx context.Context, f *remoteFS, path string) (any, error) {
			return okResult{OK: true}, f.remove(ctx, path, p.Recursive)
		}
	case "files.upload":
		var p struct {
			filePath
			Token     string `json:"token"`
			Overwrite bool   `json:"overwrite"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, confirmed, timeout = p.filePath, p.Confirmed, transferTime
		if !validLocalPath(p.Token) {
			return nil, localSourceError()
		}
		apply = func(ctx context.Context, f *remoteFS, dst string) (any, error) {
			return okResult{OK: true}, f.upload(ctx, p.Token, dst, false, p.Overwrite)
		}
	case "files.download":
		var p struct {
			filePath
			Token string `json:"token"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, mutating, timeout = p.filePath, false, transferTime
		if !validLocalPath(p.Token) {
			return nil, localDestinationError()
		}
		apply = func(ctx context.Context, f *remoteFS, src string) (any, error) {
			return okResult{OK: true}, f.download(ctx, src, p.Token)
		}
	case "files.archive":
		var p struct {
			filePath
			Destination string `json:"destination"`
			Overwrite   bool   `json:"overwrite"`
			Confirmed   bool   `json:"confirmed"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, confirmed, timeout = p.filePath, p.Confirmed, transferTime
		if p.Destination == "" || !smallText(p.Destination, maxRemotePath) {
			return nil, remotePathError()
		}
		apply = func(ctx context.Context, f *remoteFS, src string) (any, error) {
			dst, err := f.resolve(p.Destination)
			if err != nil {
				return nil, err
			}
			return f.archive(ctx, src, dst, p.Overwrite)
		}
	case "files.extract":
		var p struct {
			filePath
			Destination string `json:"destination"`
			Overwrite   bool   `json:"overwrite"`
			Confirmed   bool   `json:"confirmed"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		target, confirmed, timeout = p.filePath, p.Confirmed, transferTime
		if p.Destination == "" || !smallText(p.Destination, maxRemotePath) {
			return nil, remotePathError()
		}
		apply = func(ctx context.Context, f *remoteFS, src string) (any, error) {
			dst, err := f.resolve(p.Destination)
			if err != nil {
				return nil, err
			}
			return f.extract(ctx, src, dst, p.Overwrite)
		}
	default:
		return nil, rpc.Error("UNKNOWN_METHOD", "不受支持的文件操作")
	}
	if !confirmed {
		return nil, confirmationRequired()
	}
	if (target.Path == "" && method != "files.list") || !smallText(target.Path, maxRemotePath) {
		return nil, remotePathError()
	}
	phase, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host, err := s.findHost(phase, target.HostID)
	if err != nil {
		return nil, err
	}
	checked := host
	if err := validateHost(&checked); err != nil {
		return nil, err
	}
	if mutating {
		unlock, err := s.lockMutations(phase, mutationKey(checked))
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	client, err := s.dialHost(phase, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return withRemoteFS(phase, client, host.OS == "windows", func(f *remoteFS) (any, error) {
		p, err := f.resolve(target.Path)
		if err != nil {
			return nil, err
		}
		return apply(phase, f, p)
	})
}

func confirmationRequired() error {
	return rpc.Error("CONFIRM_REQUIRED", "请核对目标与影响后显式确认此操作")
}

func (f *remoteFS) list(ctx context.Context, p string) (directoryResult, error) {
	if err := f.checkDir(p); err != nil {
		return directoryResult{}, err
	}
	items, err := f.readDir(ctx, p)
	if err != nil {
		return directoryResult{}, err
	}
	result := directoryResult{Path: p, Entries: make([]fileEntry, 0, min(len(items), 2000))}
	budget := rpc.MaxFrame - 32768
	for _, info := range items {
		target, err := f.child(p, info.Name())
		if err != nil {
			return directoryResult{}, err
		}
		budget -= 6*(len(target)+len(info.Name())) + 160
		if budget < 0 || len(result.Entries) >= 2000 {
			return directoryResult{}, rpc.Error("DIRECTORY_LIMIT", "远程目录条目过多，请直接打开更小的子目录")
		}
		entry := fileEntry{Name: info.Name(), Path: target, Size: max(info.Size(), 0), Directory: info.IsDir() && !isLink(info), Link: isLink(info)}
		if !f.windows {
			bits := posixPermBits(info.Mode())
			entry.Mode = &bits
		}
		if !info.ModTime().IsZero() {
			entry.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		result.Entries = append(result.Entries, entry)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		return a.Name < b.Name
	})
	return result, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (f *remoteFS) readText(p string) (textResult, fs.FileInfo, error) {
	info, err := f.regular(p)
	if err != nil {
		return textResult{}, nil, err
	}
	invalid := rpc.Error("FILE_TEXT_LIMIT", "此文件不是不超过 256 KiB 的 UTF-8 文本，请使用文件下载")
	if info.Size() > maxTextBytes {
		return textResult{}, nil, invalid
	}
	file, err := f.client.Open(f.wire(p))
	if err != nil {
		return textResult{}, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTextBytes+1))
	if err != nil {
		return textResult{}, nil, err
	}
	if len(data) > maxTextBytes || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return textResult{}, nil, invalid
	}
	return textResult{Content: string(data), Digest: digestBytes(data)}, info, nil
}

func (f *remoteFS) writeText(ctx context.Context, p, content, expected string) (any, error) {
	before, info, err := f.readText(p)
	if err != nil {
		return nil, err
	}
	if before.Digest != expected {
		return nil, fileConflict()
	}
	digest := digestBytes([]byte(content))
	if before.Digest == digest {
		return map[string]string{"digest": digest}, nil
	}
	if err := f.atomicReplacement(); err != nil {
		return nil, err
	}
	err = f.stageFile(ctx, p, strings.NewReader(content), info.Mode().Perm(), func() (bool, error) {
		current, latestInfo, err := f.readText(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				err = fileConflict()
			}
			return false, err
		}
		if current.Digest != expected || latestInfo.Mode().Perm() != info.Mode().Perm() {
			return false, fileConflict()
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{"digest": digest}, nil
}

func fileConflict() error {
	return rpc.Error("FILE_CONFLICT", "远程文件已变化，未覆盖；请重新读取并核对差异")
}

func fileExists() error {
	return rpc.Error("FILE_EXISTS", "目标条目已存在，未覆盖；请改名或明确允许覆盖")
}

func protectedPath() error {
	return rpc.Error("PROTECTED_PATH", "不能修改根目录或删除、重命名远端登录目录及其上级")
}

// Save to a unique sibling, then commit with a rename. No operation removes an
// existing file to emulate atomic replacement on servers lacking the extension.
func (f *remoteFS) stageFile(ctx context.Context, p string, src io.Reader, perm fs.FileMode, recheck func() (bool, error)) error {
	parent := f.parent(p)
	if err := f.checkDir(parent); err != nil {
		return err
	}
	tmp, err := f.child(parent, ".remoteinstaller-"+rpc.ID()+".tmp")
	if err != nil {
		return err
	}
	file, err := f.client.OpenFile(f.wire(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed && ctx.Err() == nil && f.checkDir(parent) == nil {
			_ = f.client.Remove(f.wire(tmp))
		}
	}()
	if !f.windows {
		err = file.Chmod(0o600)
	}
	if err == nil {
		_, err = io.Copy(file, contextReader{ctx: ctx, r: src})
	}
	if err == nil && !f.windows && perm != 0o600 {
		err = file.Chmod(perm)
	}
	if _, enabled := f.client.HasExtension("fsync@openssh.com"); err == nil && enabled {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.checkDir(parent); err != nil {
		return err
	}
	replace, err := recheck()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if replace {
		err = f.client.PosixRename(f.wire(tmp), f.wire(p))
	} else {
		err = f.client.Rename(f.wire(tmp), f.wire(p))
	}
	committed = err == nil
	return err
}

func (f *remoteFS) mkdir(p string) error {
	if rootPath(p, f.windows) {
		return protectedPath()
	}
	if err := f.checkDir(f.parent(p)); err != nil {
		return err
	}
	if info, err := f.optionallyStat(p); err != nil {
		return err
	} else if info != nil {
		return fileExists()
	}
	return f.client.Mkdir(f.wire(p))
}

func (f *remoteFS) rename(from, to string) error {
	if f.protected(from) || f.protected(to) {
		return protectedPath()
	}
	if err := f.checkDir(f.parent(from)); err != nil {
		return err
	}
	if _, err := f.stat(from); err != nil {
		return err
	}
	if err := f.checkDir(f.parent(to)); err != nil {
		return err
	}
	if info, err := f.optionallyStat(to); err != nil {
		return err
	} else if info != nil {
		return fileExists()
	}
	return f.client.Rename(f.wire(from), f.wire(to))
}

type deleteEntry struct {
	path string
	kind fs.FileMode
}

func (f *remoteFS) remove(ctx context.Context, p string, recursive bool) error {
	var plan []deleteEntry
	remaining := maxTreeEntries
	var visit func(string) error
	visit = func(current string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.protected(current) {
			return protectedPath()
		}
		remaining--
		if remaining < 0 {
			return rpc.Error("DIRECTORY_LIMIT", "递归删除目标条目过多，未删除任何条目，请分批处理")
		}
		if err := f.checkDir(f.parent(current)); err != nil {
			return err
		}
		info, err := f.stat(current)
		if err != nil {
			return err
		}
		if recursive && info.IsDir() && !isLink(info) {
			children, err := f.readDir(ctx, current)
			if err != nil {
				return err
			}
			for _, child := range children {
				name, err := f.child(current, child.Name())
				if err != nil {
					return err
				}
				if err := visit(name); err != nil {
					return err
				}
			}
		}
		plan = append(plan, deleteEntry{path: current, kind: info.Mode().Type()})
		return nil
	}
	if err := visit(p); err != nil {
		return err
	}
	for i, entry := range plan {
		err := f.removeOne(ctx, entry)
		if err == nil {
			continue
		}
		if i > 0 {
			return rpc.Error("DELETE_PARTIAL", "远端仅删除了部分条目，请刷新并核对剩余内容；不会自动重试")
		}
		return err
	}
	return nil
}

func (f *remoteFS) removeOne(ctx context.Context, entry deleteEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.checkDir(f.parent(entry.path)); err != nil {
		return err
	}
	info, err := f.stat(entry.path)
	if err != nil {
		return err
	}
	if info.Mode().Type() != entry.kind {
		return fileConflict()
	}
	if info.IsDir() && !isLink(info) {
		return f.client.RemoveDirectory(f.wire(entry.path))
	}
	return f.client.Remove(f.wire(entry.path))
}
