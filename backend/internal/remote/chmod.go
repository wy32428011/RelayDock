package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
)

type chmodResult struct {
	OK      bool `json:"ok"`
	Updated int  `json:"updated"`
}

func posixPermBits(m fs.FileMode) uint32 {
	bits := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

func parseChmodMode(raw string) (string, fs.FileMode, error) {
	if len(raw) < 3 || len(raw) > 4 {
		return "", 0, rpc.Error("BAD_PARAMS", "权限格式无效，请使用 3 或 4 位八进制权限（如 755）")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '7' {
			return "", 0, rpc.Error("BAD_PARAMS", "权限格式无效，请使用 3 或 4 位八进制权限（如 755）")
		}
	}
	v, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return "", 0, rpc.Error("BAD_PARAMS", "权限格式无效，请使用 3 或 4 位八进制权限（如 755）")
	}
	mode := fs.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if v&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if v&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	return raw, mode, nil
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *Service) fileChmod(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		filePath
		Mode      string `json:"mode"`
		Recursive bool   `json:"recursive"`
		Sudo      bool   `json:"sudo"`
		Fast      bool   `json:"fast"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if !p.Confirmed {
		return nil, confirmationRequired()
	}
	if p.Path == "" || !smallText(p.Path, maxRemotePath) {
		return nil, remotePathError()
	}
	modeText, fileMode, err := parseChmodMode(p.Mode)
	if err != nil {
		return nil, err
	}

	timeout := fileTimeout
	if p.Recursive {
		timeout = transferTime
	}
	phase, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	host, err := s.findHost(phase, p.HostID)
	if err != nil {
		return nil, err
	}
	checked := host
	if err := validateHost(&checked); err != nil {
		return nil, err
	}
	if host.OS == "windows" {
		return nil, rpc.Error("UNSUPPORTED", "当前远端主机为 Windows，不支持 POSIX 权限修改")
	}
	unlock, err := s.lockMutations(phase, mutationKey(checked))
	if err != nil {
		return nil, err
	}
	defer unlock()

	client, err := s.dialHost(phase, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	var resolved string
	var isDir bool
	var sftpUpdated int
	_, err = withRemoteFS(phase, client, false, func(f *remoteFS) (any, error) {
		target, err := f.resolve(p.Path)
		if err != nil {
			return nil, err
		}
		if rootPath(target, false) {
			return nil, protectedPath()
		}
		info, err := f.stat(target)
		if err != nil {
			if p.Sudo && errors.Is(err, fs.ErrPermission) {
				resolved = target
				isDir = p.Recursive
				return okResult{OK: true}, nil
			}
			return nil, err
		}
		if isLink(info) {
			return nil, rpc.Error("UNSUPPORTED", "不支持直接修改符号链接的权限")
		}
		resolved = target
		isDir = info.IsDir()
		if !p.Sudo && !p.Fast {
			n, err := f.chmodSFTP(phase, target, info, fileMode, p.Recursive && isDir)
			if err != nil {
				return nil, err
			}
			sftpUpdated = n
		}
		return okResult{OK: true}, nil
	})
	if err != nil {
		return nil, err
	}
	if !p.Sudo && !p.Fast {
		return chmodResult{OK: true, Updated: sftpUpdated}, nil
	}

	recursiveFlag := ""
	if p.Recursive && isDir {
		recursiveFlag = "-R "
	}
	baseCmd := "chmod " + recursiveFlag + modeText + " -- " + shellSingleQuote(resolved)
	if !p.Sudo {
		if err := runChmodCommand(phase, client, baseCmd, ""); err != nil {
			return nil, rpc.Error("CHMOD_FAILED", "修改远端权限失败，请确认当前账号具有相应权限或尝试勾选 sudo 提权")
		}
		return chmodResult{OK: true, Updated: 1}, nil
	}

	// Sudo path: try non-interactive sudo first; if password auth is configured
	// and non-interactive sudo fails, supply the host password over stdin only.
	if err := runChmodCommand(phase, client, "sudo -n "+baseCmd, ""); err == nil {
		return chmodResult{OK: true, Updated: 1}, nil
	}
	if host.Auth == "password" {
		if cred, credErr := s.store.CredentialForHost(phase, host); credErr == nil && cred.Secret != "" {
			if err := runChmodCommand(phase, client, "sudo -S -p '' "+baseCmd, cred.Secret+"\n"); err == nil {
				return chmodResult{OK: true, Updated: 1}, nil
			}
		}
	}
	return nil, rpc.Error("CHMOD_FAILED", "sudo 提权修改权限失败，请确认当前账号具有 sudo 权限且密码有效")
}

func runChmodCommand(ctx context.Context, c *ssh.Client, cmd, stdinText string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stopOpen := context.AfterFunc(ctx, closeTransport(c))
	session, err := c.NewSession()
	stopOpen()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		go func() { _ = session.Close() }()
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			_ = c.Close()
		}
	})
	defer func() { close(finished); stop() }()
	defer session.Close()

	if stdinText != "" {
		session.Stdin = strings.NewReader(stdinText)
	}
	session.Stdout = io.Discard
	session.Stderr = io.Discard
	if err = session.Start(cmd); err == nil {
		err = session.Wait()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (f *remoteFS) chmodSFTP(ctx context.Context, target string, info fs.FileInfo, mode fs.FileMode, recursive bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !recursive || !info.IsDir() {
		if err := f.client.Chmod(f.wire(target), mode); err != nil {
			return 0, err
		}
		return 1, nil
	}
	updated := 0
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > maxTreeDepth {
			return rpc.Error("TREE_LIMIT", "目录层级过深，已停止递归修改权限")
		}
		items, err := f.readDir(ctx, dir)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if isLink(item) {
				continue
			}
			child, err := f.child(dir, item.Name())
			if err != nil {
				return err
			}
			if item.IsDir() {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			updated++
			if updated > maxTreeEntries {
				return rpc.Error("TREE_LIMIT", "目录条目数量超出 SFTP 递归上限，请勾选「快速执行」后重试")
			}
			if err := f.client.Chmod(f.wire(child), mode); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(target, 1); err != nil {
		return 0, err
	}
	updated++
	if err := f.client.Chmod(f.wire(target), mode); err != nil {
		return 0, err
	}
	return updated, nil
}
