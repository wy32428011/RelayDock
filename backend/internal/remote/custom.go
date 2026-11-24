package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/rpc"
)

const maxCustomOutput = 96 * 1024

type customStep struct {
	label   string
	command string
}

func (s *Service) runCustom(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		HostID    string `json:"hostId"`
		AppID     string `json:"appId"`
		Action    string `json:"action"`
		Token     string `json:"token"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	var upload bool
	switch p.Action {
	case "start", "stop", "restart", "status":
	case "upload", "uploadFrontend", "uploadAndStart":
		upload = true
	default:
		return nil, rpc.Error("BAD_PARAMS", "不支持此自定义应用动作")
	}
	if !p.Confirmed {
		return nil, confirmationRequired()
	}
	if !validID(p.AppID) || !validID(p.HostID) || (!upload && p.Token != "") {
		return nil, rpc.Error("BAD_PARAMS", "请选择有效的主机与自定义应用")
	}
	if upload && !validLocalPath(p.Token) {
		return nil, localSourceError()
	}
	app, err := s.store.Custom(ctx, p.AppID)
	if err != nil {
		return nil, err
	}
	if !app.Enabled {
		return nil, rpc.Error("CUSTOM_DISABLED", "此自定义应用已停用")
	}
	var steps []customStep
	target := app.Directory
	switch p.Action {
	case "start", "uploadAndStart":
		steps = []customStep{{"启动", app.StartCommand}}
	case "stop":
		steps = []customStep{{"停止", app.StopCommand}}
	case "restart":
		steps = []customStep{{"停止", app.StopCommand}, {"启动", app.StartCommand}}
	case "status":
		steps = []customStep{{"检查状态", app.StatusCommand}}
	case "uploadFrontend":
		if app.Kind != "SUPPORT" {
			return nil, rpc.Error("CUSTOM_ACTION_UNSUPPORTED", "仅 SUPPORT 类型可上传到独立前端目录")
		}
		target = app.FrontendDirectory
	}
	for _, step := range steps {
		if strings.TrimSpace(step.command) == "" || len(step.command) > 64*1024 || strings.IndexByte(step.command, 0) >= 0 {
			return nil, rpc.Error("CUSTOM_COMMAND_REQUIRED", "请先为应用保存有效的"+step.label+"命令")
		}
	}
	if upload && (strings.TrimSpace(target) == "" || !smallText(target, maxRemotePath)) {
		return nil, rpc.Error("REMOTE_PATH", "请先为自定义应用设置有效的远程目录")
	}
	phase, cancel := context.WithTimeout(ctx, transferTime+2*time.Minute)
	defer cancel()
	host, err := s.findHost(phase, p.HostID)
	if err != nil {
		return nil, err
	}
	checked := host
	if err := validateHost(&checked); err != nil {
		return nil, err
	}
	// Keep explicit custom actions against the same target ordered with this
	// core's SFTP mutations. Do not retry commands that might have started.
	unlock, err := s.lockMutations(phase, mutationKey(checked))
	if err != nil {
		return nil, err
	}
	defer unlock()
	client, secret, err := s.dialHostWithCredential(phase, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if upload {
		transfer, done := context.WithTimeout(phase, transferTime)
		_, err := withRemoteFS(transfer, client, host.OS == "windows", func(f *remoteFS) (any, error) {
			path, err := f.resolve(target)
			if err != nil {
				return nil, err
			}
			// The action confirmation covers this app's persisted deployment
			// directory, including replacement of existing regular files.
			return nil, f.upload(transfer, p.Token, path, true, true)
		})
		done()
		if err != nil {
			return nil, err
		}
	}
	var output strings.Builder
	if upload {
		output.WriteString("已上传到配置的远程目录。\n")
	}
	truncated := false
	for i, step := range steps {
		if len(steps) > 1 {
			output.WriteString("[" + step.label + "]\n")
		}
		command, done := context.WithTimeout(phase, 90*time.Second)
		err := Run(command, client, step.command, func(line string) {
			if truncated || len(line)+1 > maxCustomOutput-output.Len() {
				truncated = true
				return
			}
			output.WriteString(line)
			output.WriteByte('\n')
		})
		done()
		if err != nil {
			prefix := ""
			if upload {
				prefix = "文件已上传；"
			} else if i > 0 {
				prefix = "前一步命令已执行；"
			}
			var exit *ssh.ExitError
			if errors.As(err, &exit) {
				return nil, rpc.Error("CUSTOM_COMMAND_FAILED", prefix+step.label+"命令返回退出码 "+strconv.Itoa(exit.ExitStatus())+"，未继续后续动作，请核对远端状态")
			}
			return nil, rpc.Error("CUSTOM_COMMAND_UNCONFIRMED", prefix+"无法确认"+step.label+"命令的结果；不会重试，请核对远端状态")
		}
	}
	text := strings.TrimSuffix(output.String(), "\n")
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[已隐藏凭据]")
		if strings.ContainsAny(secret, "\r\n") {
			for _, part := range strings.FieldsFunc(secret, func(r rune) bool { return r == '\r' || r == '\n' }) {
				text = strings.ReplaceAll(text, part, "[已隐藏凭据]")
			}
		}
	}
	if len(text) > maxCustomOutput {
		// Substitution can expand the output for short secrets. Return no raw
		// prefix rather than cutting a UTF-8 boundary or pre-redacted content.
		text, truncated = "", true
	}
	if truncated {
		text += "\n[部分远程输出过长，已省略]"
	}
	if text == "" {
		text = "命令已执行完成。"
	}
	return map[string]string{"output": text}, nil
}
