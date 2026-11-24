package operations

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var statusFields = map[string]string{
	"INSTALLED": "installed", "RUNNING": "running", "VERSION": "version", "PORT": "port",
	"UNINSTALLED": "uninstalled", "PACKAGE_INSTALLED": "packageInstalled", "BINARY_FOUND": "binaryFound",
	"SERVICE_FOUND": "serviceFound", "SERVICE_ACTIVE": "serviceActive", "SERVICE_STATUS": "serviceStatus",
	"SERVICE_NAME": "serviceName", "PROCESS_FOUND": "processFound", "PORT_LISTENING": "portListening",
	"SERVICE_ONLY_STALE": "serviceOnlyStale", "SERVICE_ONLY_RESIDUE": "serviceOnlyResidue",
	"CONFIG_FOUND": "configFound", "CONFIG_ONLY_RESIDUE": "configOnlyResidue", "RESIDUE": "residue",
	"DATA_PRESERVED": "dataPreserved", "NON_TARGETS_UNCHANGED": "nonTargetsUnchanged",
}

type evidence struct {
	fields  map[string]string
	outcome string
	warned  bool
}

type progressUpdate struct {
	stage   string
	percent float64
}

func (e *evidence) consume(line string) *progressUpdate {
	line = strings.TrimSpace(ansiSequence.ReplaceAllString(line, ""))
	if line == "" || len(line) > 16*1024 {
		return nil
	}
	if strings.HasPrefix(line, "{") {
		return e.consumeJSON(line)
	}
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return nil
	}
	value = strings.TrimSpace(value)
	switch key = strings.ToUpper(strings.TrimSpace(key)); key {
	case "PROGRESS":
		stage, percent, _ := strings.Cut(value, ":")
		return scriptProgress(stage, strings.TrimSpace(percent))
	case "STAGE", "STATUS", "RESULT":
		e.setOutcome(value)
	case "WARN", "WARNING":
		e.warned = true
	default:
		e.setStatus(key, value)
	}
	return nil
}

func (e *evidence) consumeJSON(line string) *progressUpdate {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &object) != nil {
		return nil
	}
	switch strings.ToLower(jsonScalar(object["type"])) {
	case "progress":
		return scriptProgress(jsonScalar(object["stage"]), jsonScalar(object["percent"]))
	case "result":
		e.setOutcome(jsonScalar(object["stage"]))
		if yes(parseBool(jsonScalar(object["warning"]))) {
			e.warned = true
		}
	case "warning":
		e.warned = true
	case "status":
		if key := jsonScalar(object["key"]); key != "" {
			e.setStatus(strings.ToUpper(key), jsonScalar(object["value"]))
			return nil
		}
		for key, field := range statusFields {
			value := object[field]
			if len(value) == 0 {
				value = object[strings.ToLower(key)]
			}
			e.setStatus(key, jsonScalar(value))
		}
	}
	return nil
}

func jsonScalar(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	if v[0] == '"' {
		var text string
		_ = json.Unmarshal(v, &text)
		return text
	}
	if v[0] == '[' || v[0] == '{' || string(v) == "null" {
		return ""
	}
	return string(v)
}

func scriptProgress(stage, percent string) *progressUpdate {
	stage = strings.TrimSpace(stage)
	value, err := strconv.ParseFloat(percent, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || stage == "" {
		return nil
	}
	return &progressUpdate{stage: stage, percent: max(0, min(100, value))}
}

func (e *evidence) setOutcome(value string) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "SUCCESS", "COMPLETED", "SUCCEEDED":
		e.outcome = succeeded
	case "ERROR", "FAILURE", "FAILED":
		e.outcome = failed
	case "WARNING", "WARN", "SUCCEEDED_WITH_WARNING":
		e.warned = true
	}
}

func (e *evidence) setStatus(key, value string) {
	if _, ok := statusFields[key]; !ok || value == "" || len(value) > 512 {
		return
	}
	if e.fields == nil {
		e.fields = make(map[string]string)
	}
	e.fields[key] = value
}

func parseBool(value string) *bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "running", "active", "installed", "listening":
		v := true
		return &v
	case "false", "0", "no", "inactive", "stopped", "not-installed", "not_installed", "not-listening", "dead":
		v := false
		return &v
	}
	return nil
}

func yes(v *bool) bool { return v != nil && *v }
func no(v *bool) bool  { return v != nil && !*v }

var versionEvidence = regexp.MustCompile(`[0-9]+(?:[._][0-9]+)+(?:[+~-][0-9A-Za-z.]+)?`)

func (e evidence) status() AppStatus {
	installed := parseBool(e.fields["INSTALLED"])
	running := parseBool(e.fields["RUNNING"])
	for _, key := range []string{"PROCESS_FOUND", "SERVICE_ACTIVE", "SERVICE_STATUS", "PORT_LISTENING"} {
		if v := parseBool(e.fields[key]); yes(v) {
			running = v
		}
	}
	for _, key := range []string{"BINARY_FOUND", "PACKAGE_INSTALLED"} {
		if v := parseBool(e.fields[key]); yes(v) {
			installed = v
		}
	}
	onlyResidue := yes(parseBool(e.fields["SERVICE_ONLY_STALE"])) || yes(parseBool(e.fields["SERVICE_ONLY_RESIDUE"])) ||
		yes(parseBool(e.fields["CONFIG_ONLY_RESIDUE"]))
	switch {
	case yes(running):
		installed = running
	case !yes(installed) && (onlyResidue || yes(parseBool(e.fields["UNINSTALLED"]))):
		installed = parseBool("false")
	}
	version := ""
	if yes(installed) {
		version = versionEvidence.FindString(e.fields["VERSION"])
	}
	// Some checks report a configured port list (for example Nginx and Traefik).
	// The protocol can carry one display port; it never conveys runtime evidence.
	configuredPort, _, _ := strings.Cut(e.fields["PORT"], ",")
	port, err := strconv.Atoi(strings.TrimSpace(configuredPort))
	if err != nil || port < 1 || port > 65535 {
		port = 0
	}
	residue := onlyResidue || yes(parseBool(e.fields["RESIDUE"])) ||
		no(installed) && (yes(parseBool(e.fields["SERVICE_FOUND"])) || yes(parseBool(e.fields["CONFIG_FOUND"])))
	return AppStatus{Installed: installed, Running: running, Version: version, Port: port, Residue: residue}
}

func installDecision(script evidence, scriptFailed bool, checked *AppStatus) completion {
	switch {
	case checked != nil && yes(checked.Installed):
		if scriptFailed || script.warned {
			return completion{warning, "状态复查确认已安装，执行过程有告警", checked}
		}
		if yes(checked.Running) {
			return completion{succeeded, "安装完成，服务正在运行", checked}
		}
		if no(checked.Running) {
			return completion{succeeded, "安装完成，服务未运行", checked}
		}
		return completion{warning, "状态复查确认已安装，服务运行状态未知", checked}
	case script.outcome == succeeded:
		status := script.status()
		if yes(status.Installed) {
			return completion{warning, "安装脚本报告已安装，独立复查未确认，请检查远端状态", &status}
		}
	}
	if checked == nil || checked.Installed == nil {
		return completion{interrupted, "无法确认安装后的远端状态，请只读复查", checked}
	}
	return completion{failed, "安装复查未发现应用", checked}
}

func uninstallDecision(script evidence, scriptFailed bool, checked *AppStatus) completion {
	if checked != nil && (yes(checked.Installed) || yes(checked.Running)) {
		return completion{failed, "卸载复查仍发现应用或运行服务", checked}
	}
	data := parseBool(script.fields["DATA_PRESERVED"])
	others := parseBool(script.fields["NON_TARGETS_UNCHANGED"])
	if no(data) || no(others) {
		return completion{failed, "卸载后数据或非目标包、服务与操作前不一致，请复查", checked}
	}
	if checked == nil || !no(checked.Installed) || !no(checked.Running) || !yes(data) || !yes(others) {
		return completion{interrupted, "卸载后的应用、数据或非目标服务未完成复查", checked}
	}
	if checked.Residue || scriptFailed || script.warned {
		return completion{warning, "服务包已移除，数据保留及非目标包、服务已复查，请查看保留项和告警", checked}
	}
	return completion{succeeded, "卸载完成，数据保留及非目标包、服务已复查", checked}
}

func (s *Service) collect(j *job, run func(func(string)) error) (evidence, error) {
	var mu sync.Mutex
	var e evidence
	pending := make([]TaskLog, 0, 32)
	flush := func() {
		s.log(j, pending)
		pending = pending[:0]
	}
	stop := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mu.Lock()
				flush()
				mu.Unlock()
			}
		}
	}()
	err := run(func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		for _, line := range strings.Split(strings.ReplaceAll(chunk, "\r", "\n"), "\n") {
			if progress := e.consume(line); progress != nil {
				flush()
				s.progress(j, j.plan.redact.text(progress.stage, 96), progress.percent)
			}
			if text := j.plan.redact.text(line, 1600); text != "" {
				pending = append(pending, TaskLog{Text: text, At: now()})
			}
			if len(pending) >= 32 {
				flush()
			}
		}
	})
	close(stop)
	<-flushed
	flush()
	return e, err
}
