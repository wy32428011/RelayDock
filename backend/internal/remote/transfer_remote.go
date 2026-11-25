package remote

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

const maxExtractBytes = 4 << 30 // 4 GiB total decompressed limit

type transferResult struct {
	OK    bool  `json:"ok"`
	Files int   `json:"files"`
	Dirs  int   `json:"dirs"`
	Bytes int64 `json:"bytes"`
}

type archiveResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

type extractResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Files int    `json:"files"`
	Dirs  int    `json:"dirs"`
}

type remoteEntry struct {
	rel  string
	path string
	info fs.FileInfo
}

type remoteCopyFile struct {
	src    string
	srcInf fs.FileInfo
	dst    string
	before fs.FileInfo
}

func (s *Service) fileTransfer(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		SourceHostID string `json:"sourceHostId"`
		SourcePath   string `json:"sourcePath"`
		TargetHostID string `json:"targetHostId"`
		TargetPath   string `json:"targetPath"`
		Mode         string `json:"mode"`
		Overwrite    bool   `json:"overwrite"`
		Confirmed    bool   `json:"confirmed"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if !p.Confirmed {
		return nil, confirmationRequired()
	}
	if p.Mode != "copy" && p.Mode != "move" {
		return nil, rpc.Error("BAD_PARAMS", "传输模式仅支持 copy 或 move")
	}
	if p.SourcePath == "" || !smallText(p.SourcePath, maxRemotePath) || p.TargetPath == "" || !smallText(p.TargetPath, maxRemotePath) {
		return nil, remotePathError()
	}

	phase, cancel := context.WithTimeout(ctx, transferTime)
	defer cancel()

	srcHost, err := s.findHost(phase, p.SourceHostID)
	if err != nil {
		return nil, err
	}
	srcChecked := srcHost
	if err := validateHost(&srcChecked); err != nil {
		return nil, err
	}

	sameHostID := p.SourceHostID == p.TargetHostID
	dstHost := srcHost
	dstChecked := srcChecked
	if !sameHostID {
		dstHost, err = s.findHost(phase, p.TargetHostID)
		if err != nil {
			return nil, err
		}
		dstChecked = dstHost
		if err := validateHost(&dstChecked); err != nil {
			return nil, err
		}
	}

	// Lock mutation gates in deterministic sorted order to prevent deadlocks
	// when concurrent transfers run in opposite directions (A->B and B->A).
	srcKey := mutationKey(srcChecked)
	dstKey := mutationKey(dstChecked)
	sameIdentity := srcKey == dstKey
	if sameIdentity {
		unlock, err := s.lockMutations(phase, dstKey)
		if err != nil {
			return nil, err
		}
		defer unlock()
	} else if p.Mode == "move" {
		first, second := srcKey, dstKey
		if first > second {
			first, second = second, first
		}
		unlock1, err := s.lockMutations(phase, first)
		if err != nil {
			return nil, err
		}
		defer unlock1()
		unlock2, err := s.lockMutations(phase, second)
		if err != nil {
			return nil, err
		}
		defer unlock2()
	} else {
		unlock, err := s.lockMutations(phase, dstKey)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}

	srcClient, err := s.dialHost(phase, srcHost)
	if err != nil {
		return nil, err
	}
	defer srcClient.Close()

	if sameHostID {
		return withRemoteFS(phase, srcClient, srcHost.OS == "windows", func(fsys *remoteFS) (any, error) {
			src, err := fsys.resolve(p.SourcePath)
			if err != nil {
				return nil, err
			}
			dst, err := fsys.resolve(p.TargetPath)
			if err != nil {
				return nil, err
			}
			return transferRemote(phase, fsys, src, fsys, dst, true, p.Mode == "move", p.Overwrite)
		})
	}

	dstClient, err := s.dialHost(phase, dstHost)
	if err != nil {
		return nil, err
	}
	defer dstClient.Close()

	return withRemoteFS(phase, srcClient, srcHost.OS == "windows", func(srcFS *remoteFS) (any, error) {
		src, err := srcFS.resolve(p.SourcePath)
		if err != nil {
			return nil, err
		}
		return withRemoteFS(phase, dstClient, dstHost.OS == "windows", func(dstFS *remoteFS) (any, error) {
			dst, err := dstFS.resolve(p.TargetPath)
			if err != nil {
				return nil, err
			}
			return transferRemote(phase, srcFS, src, dstFS, dst, sameIdentity, p.Mode == "move", p.Overwrite)
		})
	})
}

func (f *remoteFS) walkEntries(ctx context.Context, root string) ([]remoteEntry, fs.FileInfo, error) {
	if err := f.checkDir(f.parent(root)); err != nil {
		return nil, nil, err
	}
	rootInfo, err := f.stat(root)
	if err != nil {
		return nil, nil, err
	}
	if isLink(rootInfo) {
		return nil, nil, rpc.Error("FILE_LINK", "不支持直接复制或压缩符号链接，请选择实际文件或目录")
	}
	if !rootInfo.IsDir() {
		if !rootInfo.Mode().IsRegular() {
			return nil, nil, rpc.Error("NOT_REGULAR", "仅支持复制或压缩普通文件与目录")
		}
		return []remoteEntry{{rel: "", path: root, info: rootInfo}}, rootInfo, nil
	}
	var entries []remoteEntry
	var visit func(current, rel string, depth int, info fs.FileInfo) error
	visit = func(current, rel string, depth int, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(entries) >= maxTreeEntries || depth > maxTreeDepth {
			return rpc.Error("TRANSFER_LIMIT", "目录条目过多或层级过深，请分批处理")
		}
		entries = append(entries, remoteEntry{rel: rel, path: current, info: info})
		if !info.IsDir() {
			return nil
		}
		children, err := f.readDir(ctx, current)
		if err != nil {
			return err
		}
		for _, child := range children {
			if isLink(child) {
				return rpc.Error("FILE_LINK", "目录中包含符号链接，已停止操作以防止越界读取")
			}
			if !child.IsDir() && !child.Mode().IsRegular() {
				return rpc.Error("NOT_REGULAR", "目录中包含不支持的特殊文件，已停止操作")
			}
			childPath, err := f.child(current, child.Name())
			if err != nil {
				return err
			}
			childRel := child.Name()
			if rel != "" {
				childRel = rel + "/" + child.Name()
			}
			if err := visit(childPath, childRel, depth+1, child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, "", 0, rootInfo); err != nil {
		return nil, nil, err
	}
	return entries, rootInfo, nil
}

func sameOrDescendant(f *remoteFS, parent, target string) bool {
	p := f.pathID(strings.TrimSuffix(parent, "/"))
	t := f.pathID(strings.TrimSuffix(target, "/"))
	return t == p || strings.HasPrefix(t, p+"/")
}

func transferRemote(ctx context.Context, srcFS *remoteFS, src string, dstFS *remoteFS, dst string, sameHost, move, overwrite bool) (transferResult, error) {
	if rootPath(dst, dstFS.windows) {
		return transferResult{}, protectedPath()
	}
	if move && srcFS.protected(src) {
		return transferResult{}, protectedPath()
	}
	if sameHost && sameOrDescendant(srcFS, src, dst) {
		return transferResult{}, rpc.Error("INVALID_DESTINATION", "不能将文件或文件夹复制/移动到自身或其子目录内")
	}

	// Fast path: same-host move without overwrite via SFTP rename.
	if sameHost && move && !overwrite {
		if err := srcFS.checkDir(srcFS.parent(src)); err != nil {
			return transferResult{}, err
		}
		info, err := srcFS.stat(src)
		if err != nil {
			return transferResult{}, err
		}
		if isLink(info) {
			return transferResult{}, rpc.Error("FILE_LINK", "不支持直接移动符号链接")
		}
		if err := srcFS.rename(src, dst); err != nil {
			return transferResult{}, err
		}
		if info.IsDir() {
			return transferResult{OK: true, Dirs: 1}, nil
		}
		return transferResult{OK: true, Files: 1, Bytes: max(info.Size(), 0)}, nil
	}

	entries, rootInfo, err := srcFS.walkEntries(ctx, src)
	if err != nil {
		return transferResult{}, err
	}

	plan := uploadPlan{known: make(map[string]bool)}
	if err := dstFS.planDirectory(&plan, dstFS.parent(dst), false); err != nil {
		return transferResult{}, err
	}

	seen := make(map[string]bool, len(entries))
	var files []remoteCopyFile
	var totalBytes int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return transferResult{}, err
		}
		target := dst
		if rootInfo.IsDir() && entry.rel != "" {
			for component := range strings.SplitSeq(entry.rel, "/") {
				target, err = dstFS.child(target, component)
				if err != nil {
					return transferResult{}, err
				}
			}
		}
		key := dstFS.pathID(target)
		if seen[key] {
			return transferResult{}, rpc.Error("FILE_NAME_CONFLICT", "来源条目在目标系统上存在重名冲突，未执行传输")
		}
		seen[key] = true
		if entry.info.IsDir() {
			if err := dstFS.planDirectory(&plan, target, true); err != nil {
				return transferResult{}, err
			}
			continue
		}
		var before fs.FileInfo
		if plan.known[dstFS.pathID(dstFS.parent(target))] {
			before, err = dstFS.optionallyStat(target)
			if err != nil {
				return transferResult{}, err
			}
			if before != nil {
				if isLink(before) || !before.Mode().IsRegular() {
					return transferResult{}, rpc.Error("FILE_EXISTS", "目标位置已存在同名目录或符号链接，无法覆盖")
				}
				if !overwrite {
					return transferResult{}, fileExists()
				}
				if err := dstFS.atomicReplacement(); err != nil {
					return transferResult{}, err
				}
			}
		}
		files = append(files, remoteCopyFile{src: entry.path, srcInf: entry.info, dst: target, before: before})
		totalBytes += max(entry.info.Size(), 0)
	}

	changed := false
	for _, dir := range plan.dirs {
		if err := ctx.Err(); err != nil {
			return transferResult{}, partialTransferError(err, changed)
		}
		if err := dstFS.mkdir(dir); err != nil {
			return transferResult{}, partialTransferError(err, changed)
		}
		changed = true
	}

	for _, spec := range files {
		if err := ctx.Err(); err != nil {
			return transferResult{}, partialTransferError(err, changed)
		}
		if err := copySingleRemoteFile(ctx, srcFS, dstFS, spec); err != nil {
			return transferResult{}, partialTransferError(err, changed)
		}
		changed = true
	}

	if move {
		if err := srcFS.remove(ctx, src, true); err != nil {
			return transferResult{}, rpc.Error("MOVE_SOURCE_RETAINED", "目标文件已复制完成，但清理源路径失败，请手动检查源文件")
		}
	}

	dirCount := 0
	for _, entry := range entries {
		if entry.info.IsDir() {
			dirCount++
		}
	}
	return transferResult{OK: true, Files: len(files), Dirs: dirCount, Bytes: totalBytes}, nil
}

func partialTransferError(err error, changed bool) error {
	if changed {
		return rpc.Error("TRANSFER_PARTIAL", "仅完成部分传输，请核对目标目录后再操作；不会自动回滚或重试")
	}
	return err
}

func copySingleRemoteFile(ctx context.Context, srcFS, dstFS *remoteFS, spec remoteCopyFile) error {
	beforeSrc, err := srcFS.regular(spec.src)
	if err != nil {
		return err
	}
	if !sameFileState(beforeSrc, spec.srcInf) {
		return fileConflict()
	}
	reader, err := srcFS.client.Open(srcFS.wire(spec.src))
	if err != nil {
		return err
	}
	defer reader.Close()

	perm := spec.srcInf.Mode().Perm() & 0o755
	if spec.before != nil {
		perm = spec.before.Mode().Perm()
	}
	return dstFS.stageFile(ctx, spec.dst, reader, perm, func() (bool, error) {
		afterSrc, err := srcFS.regular(spec.src)
		if err != nil || !sameFileState(beforeSrc, afterSrc) {
			return false, fileConflict()
		}
		remote, err := dstFS.optionallyStat(spec.dst)
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

func (f *remoteFS) archive(ctx context.Context, src, dst string, overwrite bool) (archiveResult, error) {
	if rootPath(dst, f.windows) {
		return archiveResult{}, protectedPath()
	}
	if sameOrDescendant(f, src, dst) {
		return archiveResult{}, rpc.Error("INVALID_DESTINATION", "压缩包不能保存在待压缩目录内部")
	}
	if err := f.checkDir(f.parent(dst)); err != nil {
		return archiveResult{}, err
	}
	before, err := f.optionallyStat(dst)
	if err != nil {
		return archiveResult{}, err
	}
	if before != nil {
		if isLink(before) || !before.Mode().IsRegular() {
			return archiveResult{}, rpc.Error("FILE_EXISTS", "目标压缩路径已存在同名目录或符号链接")
		}
		if !overwrite {
			return archiveResult{}, fileExists()
		}
		if err := f.atomicReplacement(); err != nil {
			return archiveResult{}, err
		}
	}

	entries, _, err := f.walkEntries(ctx, src)
	if err != nil {
		return archiveResult{}, err
	}
	baseName := path.Base(src)

	isZip := strings.HasSuffix(strings.ToLower(dst), ".zip")
	pr, pw := io.Pipe()
	var fileCount int
	var totalBytes int64
	go func() {
		var writeErr error
		defer func() {
			_ = pw.CloseWithError(writeErr)
		}()
		if isZip {
			zw := zip.NewWriter(pw)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					writeErr = err
					return
				}
				zipName := baseName
				if entry.rel != "" {
					zipName = baseName + "/" + entry.rel
				}
				hdr, err := zip.FileInfoHeader(entry.info)
				if err != nil {
					writeErr = err
					return
				}
				hdr.Name = zipName
				if entry.info.IsDir() {
					hdr.Name += "/"
					if _, err := zw.CreateHeader(hdr); err != nil {
						writeErr = err
						return
					}
					continue
				}
				hdr.Method = zip.Deflate
				w, err := zw.CreateHeader(hdr)
				if err != nil {
					writeErr = err
					return
				}
				rf, err := f.client.Open(f.wire(entry.path))
				if err != nil {
					writeErr = err
					return
				}
				n, copyErr := io.Copy(w, io.LimitReader(contextReader{ctx: ctx, r: rf}, entry.info.Size()))
				closeErr := rf.Close()
				if copyErr != nil {
					writeErr = copyErr
					return
				}
				if closeErr != nil {
					writeErr = closeErr
					return
				}
				if n != entry.info.Size() {
					writeErr = fileConflict()
					return
				}
				fileCount++
				totalBytes += n
			}
			writeErr = zw.Close()
			return
		}
		gw := gzip.NewWriter(pw)
		tw := tar.NewWriter(gw)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				writeErr = err
				return
			}
			tarName := baseName
			if entry.rel != "" {
				tarName = baseName + "/" + entry.rel
			}
			hdr, err := tar.FileInfoHeader(entry.info, "")
			if err != nil {
				writeErr = err
				return
			}
			hdr.Name = tarName
			if entry.info.IsDir() {
				hdr.Name += "/"
				if err := tw.WriteHeader(hdr); err != nil {
					writeErr = err
					return
				}
				continue
			}
			hdr.Size = entry.info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				writeErr = err
				return
			}
			rf, err := f.client.Open(f.wire(entry.path))
			if err != nil {
				writeErr = err
				return
			}
			n, copyErr := io.Copy(tw, io.LimitReader(contextReader{ctx: ctx, r: rf}, hdr.Size))
			closeErr := rf.Close()
			if copyErr != nil {
				writeErr = copyErr
				return
			}
			if closeErr != nil {
				writeErr = closeErr
				return
			}
			if n != hdr.Size {
				writeErr = fileConflict()
				return
			}
			fileCount++
			totalBytes += n
		}
		if err := tw.Close(); err != nil {
			writeErr = err
			return
		}
		writeErr = gw.Close()
	}()

	perm := fs.FileMode(0o644)
	if before != nil {
		perm = before.Mode().Perm()
	}
	err = f.stageFile(ctx, dst, pr, perm, func() (bool, error) {
		remote, err := f.optionallyStat(dst)
		if err != nil {
			return false, err
		}
		if before == nil {
			if remote != nil {
				return false, fileExists()
			}
			return false, nil
		}
		if !sameFileState(before, remote) {
			return false, fileConflict()
		}
		return true, nil
	})
	_ = pr.Close()
	if err != nil {
		return archiveResult{}, err
	}
	return archiveResult{OK: true, Path: dst, Files: fileCount, Bytes: totalBytes}, nil
}

func (f *remoteFS) extract(ctx context.Context, src, dst string, overwrite bool) (extractResult, error) {
	info, err := f.regular(src)
	if err != nil {
		return extractResult{}, err
	}
	if info, err := f.optionallyStat(dst); err != nil {
		return extractResult{}, err
	} else if info == nil {
		if err := f.mkdir(dst); err != nil {
			return extractResult{}, err
		}
	} else if err := f.checkDir(dst); err != nil {
		return extractResult{}, err
	}
	lower := strings.ToLower(src)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return f.extractZip(ctx, src, info.Size(), dst, overwrite)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return f.extractTar(ctx, src, dst, true, overwrite)
	case strings.HasSuffix(lower, ".tar"):
		return f.extractTar(ctx, src, dst, false, overwrite)
	default:
		return extractResult{}, rpc.Error("UNSUPPORTED_ARCHIVE", "仅支持解压 .tar.gz、.tgz、.tar 和 .zip 归档文件")
	}
}

func (f *remoteFS) resolveArchiveEntry(dst, rawName string) (string, error) {
	cleaned := strings.ReplaceAll(rawName, `\`, "/")
	cleaned = strings.TrimPrefix(cleaned, "./")
	cleaned = strings.TrimSuffix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", nil
	}
	if strings.HasPrefix(cleaned, "/") {
		return "", rpc.Error("ARCHIVE_UNSAFE", "归档包含绝对路径，已阻止解压")
	}
	target := dst
	parts := strings.Split(cleaned, "/")
	if len(parts) > maxTreeDepth {
		return "", rpc.Error("ARCHIVE_LIMIT", "归档目录层级过深，已停止解压")
	}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || !f.validLeaf(part) {
			return "", rpc.Error("ARCHIVE_UNSAFE", "归档包含非法路径片段，已阻止解压")
		}
		var err error
		target, err = f.child(target, part)
		if err != nil {
			return "", rpc.Error("ARCHIVE_UNSAFE", "归档路径超出安全限制，已阻止解压")
		}
	}
	return target, nil
}

func (f *remoteFS) ensureExtractDir(dir string, created map[string]bool) error {
	key := f.pathID(dir)
	if created[key] {
		return nil
	}
	info, err := f.optionallyStat(dir)
	if err != nil {
		return err
	}
	if info != nil {
		if isLink(info) || !info.IsDir() {
			return rpc.Error("NOT_DIRECTORY", "解压路径与已有文件或链接冲突")
		}
		created[key] = true
		return nil
	}
	parent := f.parent(dir)
	if parent != dir {
		if err := f.ensureExtractDir(parent, created); err != nil {
			return err
		}
	}
	if err := f.mkdir(dir); err != nil {
		return err
	}
	created[key] = true
	return nil
}

func (f *remoteFS) writeExtractedFile(ctx context.Context, target string, r io.Reader, perm fs.FileMode, overwrite bool, created map[string]bool) error {
	if err := f.ensureExtractDir(f.parent(target), created); err != nil {
		return err
	}
	before, err := f.optionallyStat(target)
	if err != nil {
		return err
	}
	if before != nil {
		if isLink(before) || !before.Mode().IsRegular() {
			return rpc.Error("FILE_EXISTS", "解压目标已存在同名目录或符号链接")
		}
		if !overwrite {
			return fileExists()
		}
		if err := f.atomicReplacement(); err != nil {
			return err
		}
	}
	mode := perm & 0o755
	if mode == 0 {
		mode = 0o644
	}
	return f.stageFile(ctx, target, r, mode, func() (bool, error) {
		remote, err := f.optionallyStat(target)
		if err != nil {
			return false, err
		}
		if before == nil {
			if remote != nil {
				return false, fileExists()
			}
			return false, nil
		}
		if !sameFileState(before, remote) {
			return false, fileConflict()
		}
		return true, nil
	})
}

func (f *remoteFS) extractTar(ctx context.Context, src, dst string, compressed, overwrite bool) (extractResult, error) {
	file, err := f.client.Open(f.wire(src))
	if err != nil {
		return extractResult{}, err
	}
	defer file.Close()

	var reader io.Reader = contextReader{ctx: ctx, r: file}
	if compressed {
		gr, err := gzip.NewReader(reader)
		if err != nil {
			return extractResult{}, rpc.Error("ARCHIVE_CORRUPT", "无法读取 gzip 压缩流，文件可能已损坏")
		}
		defer gr.Close()
		reader = gr
	}

	tr := tar.NewReader(reader)
	created := map[string]bool{f.pathID(dst): true}
	var count, files, dirs int
	var totalBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return extractResult{}, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return extractResult{}, rpc.Error("ARCHIVE_CORRUPT", "归档结构损坏，无法继续解压")
		}
		if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeGNULongName || hdr.Typeflag == tar.TypeGNULongLink {
			continue
		}
		count++
		if count > maxTreeEntries {
			return extractResult{}, rpc.Error("ARCHIVE_LIMIT", "归档条目数量超出安全上限，已停止解压")
		}
		target, err := f.resolveArchiveEntry(dst, hdr.Name)
		if err != nil {
			return extractResult{}, err
		}
		if target == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := f.ensureExtractDir(target, created); err != nil {
				return extractResult{}, err
			}
			dirs++
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || totalBytes+hdr.Size > maxExtractBytes {
				return extractResult{}, rpc.Error("ARCHIVE_LIMIT", "解压总大小超出安全上限，已停止解压")
			}
			limited := io.LimitReader(tr, hdr.Size)
			if err := f.writeExtractedFile(ctx, target, limited, fs.FileMode(hdr.Mode).Perm(), overwrite, created); err != nil {
				return extractResult{}, err
			}
			totalBytes += hdr.Size
			files++
		default:
			return extractResult{}, rpc.Error("ARCHIVE_UNSAFE", "归档包含符号链接或特殊设备节点，已阻止解压")
		}
	}
	return extractResult{OK: true, Path: dst, Files: files, Dirs: dirs}, nil
}

func (f *remoteFS) extractZip(ctx context.Context, src string, size int64, dst string, overwrite bool) (extractResult, error) {
	file, err := f.client.Open(f.wire(src))
	if err != nil {
		return extractResult{}, err
	}
	defer file.Close()

	zr, err := zip.NewReader(file, size)
	if err != nil {
		return extractResult{}, rpc.Error("ARCHIVE_CORRUPT", "无法解析 ZIP 归档，文件可能已损坏")
	}
	if len(zr.File) > maxTreeEntries {
		return extractResult{}, rpc.Error("ARCHIVE_LIMIT", "归档条目数量超出安全上限，已停止解压")
	}
	created := map[string]bool{f.pathID(dst): true}
	var files, dirs int
	var totalBytes uint64
	for _, entry := range zr.File {
		if err := ctx.Err(); err != nil {
			return extractResult{}, err
		}
		if entry.Mode()&fs.ModeSymlink != 0 {
			return extractResult{}, rpc.Error("ARCHIVE_UNSAFE", "ZIP 归档包含符号链接，已阻止解压")
		}
		target, err := f.resolveArchiveEntry(dst, entry.Name)
		if err != nil {
			return extractResult{}, err
		}
		if target == "" {
			continue
		}
		if entry.FileInfo().IsDir() {
			if err := f.ensureExtractDir(target, created); err != nil {
				return extractResult{}, err
			}
			dirs++
			continue
		}
		if entry.UncompressedSize64 > uint64(maxExtractBytes) || totalBytes+entry.UncompressedSize64 > uint64(maxExtractBytes) {
			return extractResult{}, rpc.Error("ARCHIVE_LIMIT", "解压总大小超出安全上限，已停止解压")
		}
		rc, err := entry.Open()
		if err != nil {
			return extractResult{}, rpc.Error("ARCHIVE_CORRUPT", "无法读取 ZIP 条目内容")
		}
		err = f.writeExtractedFile(ctx, target, io.LimitReader(rc, int64(entry.UncompressedSize64)+1), entry.Mode().Perm(), overwrite, created)
		_ = rc.Close()
		if err != nil {
			return extractResult{}, err
		}
		totalBytes += entry.UncompressedSize64
		files++
	}
	return extractResult{OK: true, Path: dst, Files: files, Dirs: dirs}, nil
}
