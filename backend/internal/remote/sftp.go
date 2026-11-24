package remote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
)

const (
	maxRemotePath  = 4096
	maxTreeEntries = 10000
	maxTreeDepth   = 64
	posixRename    = "posix-rename@openssh.com"
)

type remoteFS struct {
	client          *sftp.Client
	input           *sftpInput
	home            string
	windows         bool
	nativeDrivePath bool
}

func openSFTP(transport *ssh.Client) (*sftp.Client, *sftpInput, error) {
	session, err := transport.NewSession()
	if err != nil {
		return nil, nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	if err := session.RequestSubsystem("sftp"); err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	input := &sftpInput{src: stdout}
	c, err := sftp.NewClientPipe(input, sftpSink{Writer: stdin, Closer: session})
	if err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	return c, input, nil
}

type sftpSink struct {
	io.Writer
	io.Closer
}

func closeSFTP(c *sftp.Client, transport *ssh.Client) {
	// Some servers do not acknowledge a closing channel. Keep cleanup bounded
	// without normally discarding the SSH connection used by uploadAndStart.
	force := time.AfterFunc(time.Second, closeTransport(transport))
	defer force.Stop()
	_ = c.Close()
}

var errSFTPDirectoryLimit = errors.New("SFTP directory reply exceeded byte limit")

type sftpInput struct {
	src       io.Reader
	mu        sync.Mutex
	remaining int
	limited   bool
	exceeded  bool
}

func (r *sftpInput) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limited {
		r.remaining -= n
		if r.remaining < 0 {
			r.exceeded = true
			return 0, errSFTPDirectoryLimit
		}
	}
	return n, err
}

func (f *remoteFS) readDir(ctx context.Context, p string) ([]fs.FileInfo, error) {
	// The client buffers an entire listing before returning. Bound bytes at
	// its SSH input, not only entry count after the allocation has happened.
	f.input.mu.Lock()
	f.input.limited, f.input.remaining = true, 4<<20
	f.input.mu.Unlock()
	files, err := f.client.ReadDirContext(ctx, f.wire(p))
	f.input.mu.Lock()
	f.input.limited = false
	exceeded := f.input.exceeded
	f.input.mu.Unlock()
	if exceeded {
		return nil, rpc.Error("DIRECTORY_LIMIT", "远程目录内容过多，请直接打开更小的子目录")
	}
	return files, err
}

func withRemoteFS(ctx context.Context, client *ssh.Client, windows bool, fn func(*remoteFS) (any, error)) (any, error) {
	stop := context.AfterFunc(ctx, closeTransport(client))
	defer stop()
	setup, cancelSetup := context.WithTimeout(ctx, 12*time.Second)
	defer cancelSetup()
	stopSetup := context.AfterFunc(setup, closeTransport(client))
	defer stopSetup()
	c, input, err := openSFTP(client)
	if err != nil {
		if setup.Err() != nil {
			return nil, setup.Err()
		}
		return nil, rpc.Error("SFTP_UNAVAILABLE", "远程 SSH 服务未提供可用的 SFTP 文件子系统；终端仍可单独连接")
	}
	defer closeSFTP(c, client)
	home, err := c.Getwd()
	if !stopSetup() || setup.Err() != nil {
		return nil, setup.Err()
	}
	cancelSetup()
	if err != nil {
		return nil, sftpFault(ctx, err)
	}
	f := &remoteFS{client: c, input: input, home: "/", windows: windows}
	if windows {
		wireHome := strings.ReplaceAll(home, `\`, "/")
		f.nativeDrivePath = len(wireHome) > 2 && wireHome[1:3] == ":/"
	}
	f.home, err = f.resolve(home)
	if err != nil || !strings.ContainsAny(home, "/\\") || (!strings.HasPrefix(home, "/") && !(windows && (strings.HasPrefix(home, `\`) || f.nativeDrivePath))) {
		return nil, rpc.Error("SFTP_HOME", "远端没有返回有效的 SFTP 登录目录")
	}
	result, err := fn(f)
	if err != nil {
		return nil, sftpFault(ctx, err)
	}
	return result, nil
}

func sftpFault(ctx context.Context, err error) error {
	var fault *rpc.Fault
	if errors.As(err, &fault) {
		return fault
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, fs.ErrNotExist) {
		return rpc.Error("FILE_NOT_FOUND", "远程条目或其上级目录不存在，请刷新后重试")
	}
	if errors.Is(err, fs.ErrPermission) {
		return rpc.Error("FILE_PERMISSION", "远程 SSH 账户没有执行此文件操作的权限")
	}
	var status *sftp.StatusError
	if errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxOpUnsupported {
		return rpc.Error("SFTP_UNSUPPORTED", "远程 SFTP 服务不支持此文件操作")
	}
	return rpc.Error("FILE_OPERATION_FAILED", "远程文件操作未完成；请核对 SFTP 连接、目标路径和权限")
}

func remotePathError() error {
	return rpc.Error("REMOTE_PATH", "远程路径无效：不支持上级跳转、歧义路径或特殊设备名称")
}

func windowsDrive(part string) bool {
	return len(part) == 2 && part[1] == ':' && (part[0] >= 'A' && part[0] <= 'Z' || part[0] >= 'a' && part[0] <= 'z')
}

func driveOf(p string) string {
	if len(p) >= 4 && p[0] == '/' && p[3] == '/' && windowsDrive(p[1:3]) {
		return p[:4]
	}
	return "/"
}

func rootPath(p string, windows bool) bool { return p == "/" || (windows && p == driveOf(p)) }

func (f *remoteFS) resolve(p string) (string, error) {
	if !smallText(p, maxRemotePath) {
		return "", remotePathError()
	}
	if f.windows {
		p = strings.ReplaceAll(p, `\`, "/")
		if strings.HasPrefix(p, "//") { // UNC and Windows device namespaces are not SFTP paths.
			return "", remotePathError()
		}
	}
	root := f.home
	if strings.HasPrefix(p, "/") {
		root, p = "/", strings.TrimLeft(p, "/")
		if f.windows {
			root = driveOf(f.home)
		}
	}
	parts := strings.Split(p, "/")
	if f.windows && windowsDrive(parts[0]) {
		// Both /C:/... and C:/... designate a volume. Bare C: is accepted
		// only as the safeguarded root, never as a drive-relative prefix.
		root, parts = "/"+strings.ToUpper(parts[0])+"/", parts[1:]
	}
	for _, component := range parts {
		if component == "" || component == "." {
			continue
		}
		if !f.validLeaf(component) {
			return "", remotePathError()
		}
		root = strings.TrimSuffix(root, "/") + "/" + component
	}
	if len(root) > maxRemotePath || strings.Count(root, "/") > maxTreeDepth+2 {
		return "", remotePathError()
	}
	return root, nil
}

func (f *remoteFS) validLeaf(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || !smallText(name, 1024) {
		return false
	}
	if !f.windows {
		return true
	}
	if strings.ContainsAny(name, `\:*?"<>|`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	// Windows DOS-device names are invalid even when followed by an extension.
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		switch base[3:] {
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return false
		}
	}
	return true
}

func (f *remoteFS) child(dir, leaf string) (string, error) {
	child := strings.TrimSuffix(dir, "/") + "/" + leaf
	if !f.validLeaf(leaf) || len(child) > maxRemotePath || strings.Count(child, "/") > maxTreeDepth+2 {
		return "", remotePathError()
	}
	return child, nil
}

func (f *remoteFS) parent(p string) string {
	if rootPath(p, f.windows) {
		return p
	}
	dir := path.Dir(p)
	if f.windows && len(dir) == 3 && dir[0] == '/' && windowsDrive(dir[1:]) {
		return dir + "/"
	}
	return dir
}

func (f *remoteFS) protected(p string) bool {
	if rootPath(p, f.windows) {
		return true
	}
	home := f.home
	if f.windows {
		p, home = strings.ToLower(p), strings.ToLower(home)
	}
	// Do not rename/delete the login directory or an ancestor that contains it.
	return p == home || strings.HasPrefix(home, strings.TrimSuffix(p, "/")+"/")
}

func (f *remoteFS) wire(p string) string {
	if f.nativeDrivePath && driveOf(p) != "/" {
		return p[1:]
	}
	return p
}

func isLink(info fs.FileInfo) bool { return info.Mode()&fs.ModeSymlink != 0 }

func (f *remoteFS) stat(p string) (fs.FileInfo, error) { return f.client.Lstat(f.wire(p)) }

func (f *remoteFS) optionallyStat(p string) (fs.FileInfo, error) {
	info, err := f.stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return info, err
}

// Check path components before asking the server to traverse them. SFTP v3 has
// no directory-handle-relative open/rename or no-follow flag; these checks must
// not be mistaken for isolation from a concurrent remote filesystem writer.
func (f *remoteFS) checkDir(p string) error {
	dirs := []string{p}
	for !rootPath(p, f.windows) {
		p = f.parent(p)
		dirs = append(dirs, p)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		info, err := f.stat(dirs[i])
		if err != nil {
			return err
		}
		if isLink(info) {
			return rpc.Error("FILE_LINK", "不会通过目录链接访问或修改目标，请使用其实际路径")
		}
		if !info.IsDir() {
			return rpc.Error("NOT_DIRECTORY", "目标路径包含非目录条目")
		}
	}
	return nil
}

func (f *remoteFS) regular(p string) (fs.FileInfo, error) {
	if err := f.checkDir(f.parent(p)); err != nil {
		return nil, err
	}
	info, err := f.stat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, rpc.Error("NOT_REGULAR_FILE", "只允许读取、下载或编辑常规文件，不跟随链接")
	}
	return info, nil
}

func (f *remoteFS) atomicReplacement() error {
	if _, ok := f.client.HasExtension(posixRename); !ok {
		return rpc.Error("ATOMIC_REPLACE_UNAVAILABLE", "远程 SFTP 不支持原子覆盖，原文件未改动；可改用新文件名")
	}
	return nil
}
