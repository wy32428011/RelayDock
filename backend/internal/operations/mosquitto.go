package operations

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/remote"
	"remoteinstaller.local/core/internal/rpc"
)

//go:embed mosquitto.sh
var mosquittoScript string

var (
	stagingPath = regexp.MustCompile(`^/tmp/remote-installer\.[a-f0-9]{32}$`)
	debName     = regexp.MustCompile(`^([a-z0-9][a-z0-9+.-]*)_([A-Za-z0-9.+~%:-]+)_(amd64|arm64|all)\.deb$`)
)

type payloadFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func mosquittoPayload(b assets.Bundle, arch string) ([]payloadFile, error) {
	return mosquittoPayloadWithPins(b, arch, reviewedMosquittoDEBs[arch])
}

func mosquittoPayloadWithPins(b assets.Bundle, arch string, pins map[string]string) ([]payloadFile, error) {
	invalid := rpc.Error("RESOURCE_INVALID", "Mosquitto 离线包清单已变化或含有无效的 DEB 文件")
	mismatch := rpc.Error("RESOURCE_MISMATCH", "离线资源缺少与所选版本和 CPU 架构一致的 Mosquitto 主包")
	if !filepath.IsAbs(b.Dir) {
		return nil, invalid
	}
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return nil, invalid
	}
	defer root.Close()
	file, err := root.Open("manifest.json")
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, invalid
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != b.Digest {
		return nil, invalid
	}
	var manifest struct {
		Files []payloadFile `json:"files"`
	}
	if json.Unmarshal(data, &manifest) != nil || len(manifest.Files) != b.FileCount {
		return nil, invalid
	}
	primary := false
	files := make([]payloadFile, 0, len(manifest.Files))
	packages := make(map[string]bool)
	for _, f := range manifest.Files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".deb") {
			continue
		}
		matches := debName.FindStringSubmatch(path.Base(f.Path))
		if matches == nil || !filepath.IsLocal(filepath.FromSlash(f.Path)) || path.Clean(f.Path) != f.Path ||
			strings.ContainsAny(f.Path, `\:`) || f.Size <= 0 || packages[matches[1]] {
			return nil, invalid
		}
		pkg := matches[1]
		packages[pkg] = true
		if matches[3] != arch && matches[3] != "all" {
			return nil, mismatch
		}
		pinned, reviewed := pins[pkg]
		if !reviewed {
			continue // Ignore system packages shipped in the broader repository.
		}
		if f.SHA256 != pinned {
			return nil, rpc.Error("RESOURCE_UNTRUSTED", "离线资源中的 Mosquitto 或依赖不是已核实的软件包")
		}
		if pkg == "mosquitto" {
			if matches[3] != arch || !debVersionMatches(matches[2], b.Version) || b.Entrypoint != "" && b.Entrypoint != f.Path {
				return nil, mismatch
			}
			primary = true
		}
		files = append(files, f)
	}
	if !primary {
		return nil, mismatch
	}
	return files, nil
}

func debVersionMatches(debVersion, upstream string) bool {
	debVersion = strings.ReplaceAll(debVersion, "%3a", ":")
	debVersion = strings.ReplaceAll(debVersion, "%3A", ":")
	if epoch, rest, ok := strings.Cut(debVersion, ":"); ok {
		if _, err := strconv.Atoi(epoch); err != nil {
			return false
		}
		debVersion = rest
	}
	return debVersion == upstream || strings.HasPrefix(debVersion, upstream) && strings.ContainsAny(debVersion[len(upstream):len(upstream)+1], "-+~")
}

func mosquittoCommand(p plan, mode string, extra ...string) string {
	args := append([]string{mode, canonicalArch(p.host.Arch), p.version}, extra...)
	cmd := "env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C LC_ALL=C bash --noprofile --norc -c " + shellQuote(mosquittoScript) + " --"
	for _, arg := range args {
		cmd += " " + shellQuote(arg)
	}
	return cmd
}

func (s *Service) mosquittoOperation(j *job, c *ssh.Client) (out completion) {
	p := j.plan
	ctx, stop := context.WithTimeout(j.ctx, detectionTimeout)
	_, err := s.collect(j, func(line func(string)) error {
		return remote.Run(ctx, c, mosquittoCommand(p, "check-"+p.kind), line)
	})
	stop()
	if err != nil {
		return operationFailure(err, "远端执行前检查失败，请查看日志")
	}
	var dir string
	var changeAttempted, remoteExited bool
	if p.kind == "install" {
		ctx, stop := context.WithTimeout(j.ctx, transferTimeout)
		defer stop()
		bundle, err := s.assets.Resolve(ctx, p.appID, p.version, p.host.OS, p.host.OSVersion, p.host.Arch, p.bundle.ID)
		if err != nil || bundle.Digest != p.bundle.Digest {
			return operationFailure(err, "已核对的离线资源发生变化，请重新选择")
		}
		files, err := s.pickPayload(bundle, canonicalArch(p.host.Arch))
		if err != nil {
			return operationFailure(err, "离线资源不可读")
		}
		dir = "/tmp/remote-installer." + j.id
		if !stagingPath.MatchString(dir) || remote.Run(ctx, c, "mkdir -m 0700 -- "+shellQuote(dir), nil) != nil {
			return completion{state: failed, message: "无法创建私有的远端任务暂存目录"}
		}
		defer func() {
			// A command that has not reported an exit may still need its package
			// files. Its staging path is unique and is never reused by a retry.
			if !changeAttempted || remoteExited {
				if err := removeStaging(c, dir); err != nil {
					s.log(j, []TaskLog{{Text: "远端暂存目录未确认清理：" + dir, At: now()}})
					if out.state == succeeded {
						out.state, out.message = warning, "安装完成，远端暂存目录未确认清理"
					}
				}
			} else {
				s.log(j, []TaskLog{{Text: "远端状态未知，暂存目录需人工复查：" + dir, At: now()}})
			}
		}()
		if err := uploadPayload(ctx, c, bundle, dir, files); err != nil {
			return operationFailure(err, "离线资源上传失败，请检查远端 SFTP 和剩余空间")
		}
	}
	// Loading an offline resource or transferring it may take time. Recheck
	// the saved target before dispatching the write to the existing transport.
	if err := s.checkTarget(j); err != nil {
		return operationFailure(err, "主机信息已改变，请重新提交任务")
	}
	if !s.phase(j, running, 20, "正在执行远端操作") || !s.beginChange(j) {
		return completion{state: cancelled, message: "任务已取消"}
	}
	changeAttempted = true
	ctx, stop = context.WithTimeout(j.ctx, writeTimeout)
	script, err := s.collect(j, func(line func(string)) error {
		if p.kind == "install" {
			cmd := mosquittoCommand(p, "install", dir, p.parameters["MQTT_PORT"], p.parameters["USERNAME"])
			return runInput(ctx, c, cmd, p.parameters["PASSWORD"]+"\n", line)
		}
		return remote.Run(ctx, c, mosquittoCommand(p, "uninstall"), line)
	})
	stop()
	remoteExited = completedCommand(err)
	if !remoteExited {
		return completion{state: interrupted, message: "连接中断或远端退出状态未知，请只读复查后续状态"}
	}
	if !s.phase(j, verifying, 94, "正在独立复查应用状态") {
		return completion{state: interrupted, message: "执行后复查已中断"}
	}
	status, _, verifyErr := s.detect(j, c, true)
	if verifyErr != nil {
		status = nil
	}
	scriptFailed := err != nil || script.outcome == failed
	if p.kind == "uninstall" {
		return uninstallDecision(script, scriptFailed, status)
	}
	if status != nil && yes(status.Installed) {
		port, _ := strconv.Atoi(p.parameters["MQTT_PORT"])
		if status.Version != "" && status.Version != p.version {
			return completion{failed, "安装后的软件包版本与所选版本不符", status}
		}
		if status.Port != 0 && status.Port != port {
			return completion{failed, "安装后的监听配置与所选端口不符", status}
		}
		if status.Version == "" || status.Port == 0 {
			return completion{warning, "已检测到安装，版本或监听配置仍需复查", status}
		}
	}
	return installDecision(script, scriptFailed, status)
}

func removeStaging(c *ssh.Client, dir string) error {
	if !stagingPath.MatchString(dir) {
		return rpc.Error("STAGING_FAILED", "任务暂存目录无效")
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return remote.Run(ctx, c, "rm -rf -- "+shellQuote(dir), nil)
}

func uploadPayload(ctx context.Context, c *ssh.Client, b assets.Bundle, dir string, files []payloadFile) error {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	client, err := sftp.NewClient(c)
	if err != nil {
		return rpc.Error("SFTP_UNAVAILABLE", "此主机没有可用的 SFTP，无法上传离线资源")
	}
	defer client.Close()
	remoteDir := path.Join(dir, "packages")
	if err := client.Mkdir(remoteDir); err != nil {
		return err
	}
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, item := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := sendPayloadFile(root, client, remoteDir, item); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func sendPayloadFile(root *os.Root, client *sftp.Client, dir string, item payloadFile) error {
	invalid := rpc.Error("RESOURCE_INVALID", "离线包文件在上传前或上传期间发生变化")
	name := filepath.FromSlash(item.Path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
		return invalid
	}
	in, err := root.Open(name)
	if err != nil {
		return invalid
	}
	defer in.Close()
	out, err := client.OpenFile(path.Join(dir, path.Base(item.Path)), os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	h := sha256.New()
	written, err := io.Copy(out, io.TeeReader(io.LimitReader(in, item.Size+1), h))
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
		return invalid
	}
	return nil
}
