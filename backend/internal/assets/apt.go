package assets

import (
	"bufio"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"remoteinstaller.local/core/internal/rpc"
)

var primaryDebNames = map[string][]string{
	"mysql":     {"mysql-server", "mysql-community-server"},
	"mariadb":   {"mariadb-server"},
	"redis":     {"redis-server"},
	"nginx":     {"nginx"},
	"rabbitmq":  {"rabbitmq-server"},
	"mosquitto": {"mosquitto"},
}

type aptValidation struct {
	seenFiles map[string]bool
	primary   bool
}

func (p resourcePlan) checkAptIndex(name string, r io.Reader, state *aptValidation) error {
	invalid := rpc.Error("RESOURCE_INVALID", "APT Packages 索引格式无效，或引用的包未列入清单")
	mismatch := rpc.Error("RESOURCE_MISMATCH", "APT Packages 索引中的应用版本或 CPU 架构与资源清单不一致")
	fields := map[string]string{}
	lastField := ""
	count := 0
	finish := func() error {
		if len(fields) == 0 {
			return nil
		}
		filename := strings.TrimPrefix(fields["filename"], "./")
		if !safePath(filename) || !strings.HasSuffix(filename, ".deb") || fields["package"] == "" || fields["version"] == "" {
			return invalid
		}
		full := path.Join(path.Dir(name), filename)
		f, ok := p.byPath[full]
		size, err := strconv.ParseInt(fields["size"], 10, 64)
		if !ok || state.seenFiles[full] || err != nil || size != *f.Size || !strings.EqualFold(fields["sha256"], f.SHA256) {
			return invalid
		}
		arch := fields["architecture"]
		if (arch != "all" && arch != p.bundle.Arch) || !strings.HasSuffix(filename, "_"+arch+".deb") {
			return mismatch
		}
		if slices.Contains(primaryDebNames[p.bundle.AppID], fields["package"]) {
			if !debVersionMatches(fields["version"], p.bundle.Version) {
				return mismatch
			}
			state.primary = true
		}
		state.seenFiles[full] = true
		count++
		clear(fields)
		lastField = ""
		return nil
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 128<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := finish(); err != nil {
				return err
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			switch lastField {
			case "", "package", "version", "architecture", "filename", "size", "sha256":
				return invalid
			}
			continue // Fields like Description may have Debian continuation lines.
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.ToLower(key)
		if _, dup := fields[key]; !ok || key == "" || dup {
			return invalid
		}
		fields[key], lastField = strings.TrimSpace(value), key
	}
	if scanner.Err() != nil {
		return invalid
	}
	if err := finish(); err != nil {
		return err
	}
	if count == 0 {
		return invalid
	}
	return nil
}

func debVersionMatches(version, upstream string) bool {
	if epoch, rest, ok := strings.Cut(version, ":"); ok {
		if _, err := strconv.ParseUint(epoch, 10, 64); err != nil {
			return false
		}
		version = rest
	}
	if version == upstream {
		return true
	}
	// Repository revisions and distro qualifiers are not the upstream version.
	return strings.HasPrefix(version, upstream) && strings.ContainsRune("-+~", rune(version[len(upstream)]))
}

func (p resourcePlan) checkAptCoverage(state aptValidation) error {
	if p.bundle.OS != "ubuntu" || p.bundle.Arch != "arm64" || !usesSystemPackages(p.bundle.AppID) {
		return nil
	}
	if !state.primary {
		return rpc.Error("RESOURCE_INVALID", "Ubuntu ARM64 资源缺少有效的 Packages 索引或所选应用的主包")
	}
	for name := range p.byPath {
		if strings.HasSuffix(strings.ToLower(name), ".deb") && !state.seenFiles[name] {
			return rpc.Error("RESOURCE_INVALID", "Ubuntu ARM64 资源含有未被 Packages 索引覆盖的 DEB")
		}
	}
	return nil
}
