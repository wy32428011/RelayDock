package remote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func validLocalPath(p string) bool {
	if !filepath.IsAbs(p) || !smallText(p, 4096) {
		return false
	}
	if runtime.GOOS == "windows" {
		canonical := filepath.ToSlash(p)
		if strings.HasPrefix(canonical, "//./") || strings.HasPrefix(canonical, "//?/") || strings.Contains(p[len(filepath.VolumeName(p)):], ":") {
			return false
		}
	}
	clean := filepath.Clean(p)
	return filepath.Dir(clean) != clean
}

func openLocalParent(p string) (*os.Root, string, error) {
	if !validLocalPath(p) {
		return nil, "", errors.New("invalid authorized local path")
	}
	p = filepath.Clean(p)
	root, err := os.OpenRoot(filepath.Dir(p))
	return root, filepath.Base(p), err
}

func localSourceError() error {
	return rpc.Error("LOCAL_SOURCE", "本机来源不可读、类型不受支持或已变化，请重新选择")
}
func localDestinationError() error {
	return rpc.Error("LOCAL_DESTINATION", "无法安全写入选定位置；请检查本机文件、空间与权限后重新选择")
}

func sameFileState(before, after fs.FileInfo) bool {
	return before != nil && after != nil && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

type uploadItem struct {
	local string
	info  fs.FileInfo
}

type fileUpload struct {
	uploadItem
	remote string
	before fs.FileInfo
}

type uploadPlan struct {
	dirs  []string
	known map[string]bool // path -> existed when planning, false if planned for creation
	files []fileUpload
}

func (f *remoteFS) pathID(p string) string {
	if f.windows {
		return strings.ToLower(p)
	}
	return p
}

func (f *remoteFS) upload(ctx context.Context, source, target string, intoDir, overwrite bool) error {
	if rootPath(target, f.windows) {
		return protectedPath()
	}
	parent, leaf, err := openLocalParent(source)
	if err != nil {
		return localSourceError()
	}
	defer parent.Close()
	info, err := parent.Lstat(leaf)
	if err != nil || isLink(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
		return localSourceError()
	}
	root := parent
	var items []uploadItem
	if info.IsDir() {
		root, err = parent.OpenRoot(leaf)
		if err != nil {
			return localSourceError()
		}
		defer root.Close()
		actual, err := root.Stat(".")
		if err != nil || !os.SameFile(info, actual) {
			return localSourceError()
		}
		err = fs.WalkDir(root.FS(), ".", func(p string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return localSourceError()
			}
			if len(items) >= maxTreeEntries || len(p) > maxRemotePath || strings.Count(p, "/") > maxTreeDepth {
				return rpc.Error("UPLOAD_LIMIT", "选定目录的条目过多或层级过深，请分批上传")
			}
			st, err := entry.Info()
			if err != nil || isLink(st) || (!st.IsDir() && !st.Mode().IsRegular()) {
				return localSourceError()
			}
			items = append(items, uploadItem{local: p, info: st})
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		items = []uploadItem{{local: leaf, info: info}}
	}

	plan := uploadPlan{known: make(map[string]bool)}
	if intoDir {
		if err := f.planDirectory(&plan, target, true); err != nil {
			return err
		}
		if !info.IsDir() {
			target, err = f.child(target, leaf)
			if err != nil {
				return err
			}
		}
	} else if err := f.planDirectory(&plan, f.parent(target), false); err != nil {
		return err
	}

	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := target
		if info.IsDir() && item.local != "." {
			for component := range strings.SplitSeq(item.local, "/") {
				p, err = f.child(p, component)
				if err != nil {
					return err
				}
			}
		}
		key := f.pathID(p)
		if seen[key] {
			return rpc.Error("FILE_NAME_CONFLICT", "本机来源中的名称在目标系统上重复，未上传任何文件")
		}
		seen[key] = true
		if item.info.IsDir() {
			if err := f.planDirectory(&plan, p, true); err != nil {
				return err
			}
			continue
		}
		var before fs.FileInfo
		if plan.known[f.pathID(f.parent(p))] {
			before, err = f.optionallyStat(p)
			if err != nil {
				return err
			}
		}
		if before != nil {
			if !overwrite || !before.Mode().IsRegular() {
				return fileExists()
			}
			if err := f.atomicReplacement(); err != nil {
				return err
			}
		}
		plan.files = append(plan.files, fileUpload{uploadItem: item, remote: p, before: before})
	}

	changed := false
	return f.applyUpload(ctx, root, plan, &changed)
}

func (f *remoteFS) planDirectory(plan *uploadPlan, dir string, create bool) error {
	key := f.pathID(dir)
	if _, known := plan.known[key]; known {
		return nil
	}
	isRoot := rootPath(dir, f.windows)
	if !isRoot {
		if err := f.planDirectory(plan, f.parent(dir), create); err != nil {
			return err
		}
	}
	var info fs.FileInfo
	if isRoot || plan.known[f.pathID(f.parent(dir))] {
		var err error
		info, err = f.optionallyStat(dir)
		if err != nil {
			return err
		}
	}
	if info != nil {
		if isLink(info) {
			return rpc.Error("FILE_LINK", "不会通过目录链接上传，请选择实际路径")
		}
		if !info.IsDir() {
			return rpc.Error("NOT_DIRECTORY", "上传目标包含非目录条目")
		}
		plan.known[key] = true
		return nil
	}
	if !create || isRoot {
		return fs.ErrNotExist
	}
	plan.known[key] = false
	plan.dirs = append(plan.dirs, dir)
	return nil
}

func (f *remoteFS) applyUpload(ctx context.Context, root *os.Root, plan uploadPlan, changed *bool) (err error) {
	defer func() {
		if err != nil && *changed {
			err = rpc.Error("UPLOAD_PARTIAL", "仅完成部分上传，请核对远端文件后再操作；不会自动回滚或重试")
		}
	}()
	for _, p := range plan.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.mkdir(p); err != nil {
			return err
		}
		*changed = true
	}
	for _, spec := range plan.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.uploadFile(ctx, root, spec); err != nil {
			return err
		}
		*changed = true
	}
	return nil
}

func (f *remoteFS) uploadFile(ctx context.Context, root *os.Root, spec fileUpload) error {
	current, err := root.Lstat(spec.local)
	if err != nil || !os.SameFile(current, spec.info) || !sameFileState(current, spec.info) {
		return localSourceError()
	}
	local, err := openRegular(root, spec.local, current)
	if err != nil {
		return localSourceError()
	}
	defer local.Close()
	// New uploads inherit readable/executable bits from the local creator,
	// without propagating group/world write, setuid, setgid, or sticky bits.
	perm := spec.info.Mode().Perm() & 0o755
	if spec.before != nil {
		perm = spec.before.Mode().Perm()
	}
	return f.stageFile(ctx, spec.remote, local, perm, func() (bool, error) {
		latest, err := local.Stat()
		if err != nil || !sameFileState(latest, spec.info) {
			return false, localSourceError()
		}
		remote, err := f.optionallyStat(spec.remote)
		if err != nil {
			return false, err
		}
		if spec.before == nil {
			if remote != nil {
				return false, fileExists()
			}
			return false, nil
		}
		if !sameFileState(spec.before, remote) {
			return false, fileConflict()
		}
		return true, nil
	})
}

func (f *remoteFS) download(ctx context.Context, source, target string) error {
	before, err := f.regular(source)
	if err != nil {
		return err
	}
	dir, leaf, err := openLocalParent(target)
	if err != nil {
		return localDestinationError()
	}
	defer dir.Close()
	old, err := dir.Lstat(leaf)
	if (err != nil && !errors.Is(err, fs.ErrNotExist)) || (old != nil && !old.Mode().IsRegular()) {
		return localDestinationError()
	}
	remote, err := f.client.Open(f.wire(source))
	if err != nil {
		return err
	}
	defer remote.Close()
	tmp := ".remoteinstaller-" + rpc.ID() + ".download"
	local, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return localDestinationError()
	}
	committed := false
	defer func() {
		if !committed {
			_ = dir.Remove(tmp)
		}
	}()
	n, copyErr := io.Copy(local, contextReader{ctx: ctx, r: remote})
	var syncErr error
	if copyErr == nil {
		syncErr = local.Sync()
	}
	closeErr := local.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return rpc.Error("DOWNLOAD_FAILED", "传输未完成，未替换本机目标；请检查连接、磁盘空间和权限")
	}
	after, err := f.regular(source)
	if err != nil {
		return err
	}
	if n != before.Size() || !sameFileState(before, after) {
		return fileConflict()
	}
	current, err := dir.Lstat(leaf)
	if old == nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return localDestinationError()
		}
	} else if err != nil || !os.SameFile(old, current) || !sameFileState(old, current) {
		return localDestinationError()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := dir.Rename(tmp, leaf); err != nil {
		return localDestinationError()
	}
	committed = true
	return nil
}
