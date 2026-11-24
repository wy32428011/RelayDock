package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/state"
)

func payloadFixture(t *testing.T, files []payloadFile) assets.Bundle {
	t.Helper()
	root := t.TempDir()
	data, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, root, "manifest.json", string(data))
	digest := sha256.Sum256(data)
	return assets.Bundle{ID: "sample-resource", Digest: hex.EncodeToString(digest[:]), Dir: root,
		AppID: "mosquitto", Version: reviewedMosquittoVersion, OS: "ubuntu", OSVersion: "24.04",
		Arch: "amd64", Trust: "reviewed", FileCount: len(files)}
}

func TestExactPinnedOfflinePayload(t *testing.T) {
	const mainPath = "packages/mosquitto_2.1.2-0mosquitto1~noble1_amd64.deb"
	main := payloadFile{Path: mainPath, Size: 42, SHA256: reviewedMosquittoDEBs["amd64"]["mosquitto"]}
	ignored := payloadFile{Path: "arbitrary-utils_1.0_amd64.deb", Size: 42, SHA256: "fixture"}
	b := payloadFixture(t, []payloadFile{main, ignored})
	b.Entrypoint = mainPath
	files, err := mosquittoPayload(b, "amd64")
	if err != nil || len(files) != 1 || files[0].Path != mainPath {
		t.Fatalf("payload selected %+v %v", files, err)
	}
	if _, err := mosquittoPayload(b, "arm64"); !hasCode(err, "RESOURCE_MISMATCH") {
		t.Fatalf("architecture should match exactly: %v", err)
	}
	main.SHA256 = "unreviewed"
	if _, err := mosquittoPayload(payloadFixture(t, []payloadFile{main}), "amd64"); !hasCode(err, "RESOURCE_UNTRUSTED") {
		t.Fatalf("an integrity-only manifest should not authorize new executable bytes: %v", err)
	}
	b.Digest = "wrong-digest"
	if _, err := mosquittoPayload(b, "amd64"); !hasCode(err, "RESOURCE_INVALID") {
		t.Fatalf("changed manifest not rejected: %v", err)
	}
}

func TestCatalogDetectionCoverage(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "resources"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := assets.New(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := a.Handle(ctx, "catalog.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	apps, ok := value.([]assets.App)
	if !ok || len(apps) != 12 {
		t.Fatalf("unexpected catalog: %T (%d entries)", value, len(apps))
	}
	for _, app := range apps {
		for _, os := range app.Platforms {
			t.Run(app.ID+"-"+os, func(t *testing.T) {
				// Read only packaged source. Fixture hosts exercise the workflow;
				// never run a legacy script against this developer machine.
				dir, entry, err := a.Script(os, app.ID, "", "detect")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := readDetection(dir, entry, isWindows(os)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestOperationsRejectBeforeDial(t *testing.T) {
	ctx := context.Background()
	h := hostsFixture(1)
	host := h.hosts["host-1"]
	host.Arch = "arm64"
	h.hosts[host.ID] = host
	main := payloadFile{Path: "mosquitto_2.1.2-0mosquitto1~noble1_amd64.deb", Size: 42, SHA256: reviewedMosquittoDEBs["amd64"]["mosquitto"]}
	bundle := payloadFixture(t, []payloadFile{main})
	s, err := newService(sampleDB(t), h, sampleAssets{bundle: bundle}, func(context.Context, state.Host) (*ssh.Client, error) {
		t.Error("invalid operation attempted to dial")
		return nil, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	base, _ := createJob(h, host.ID, "install", "validate-fixture")
	clear := false
	for _, entry := range []struct {
		name string
		want string
		set  func(*operation)
	}{
		{"wrong remote architecture", "RESOURCE_MISMATCH", func(o *operation) {}},
		{"explicit online unavailable", "ONLINE_UNSUPPORTED", func(o *operation) { o.SourceMode = "online" }},
		{"confirmation required", "CONFIRMATION_REQUIRED", func(o *operation) { o.Confirmed = false }},
		{"missing companion password", "BAD_PARAMS", func(o *operation) { o.Parameters = map[string]string{"username": "fixture"} }},
		{"invalid port", "BAD_PARAMS", func(o *operation) { o.Parameters = map[string]string{"MQTT_PORT": "65536"} }},
		{"runtime variables not permitted", "BAD_PARAMS", func(o *operation) { o.Parameters = map[string]string{"BASH_ENV": "/tmp/file"} }},
		{"destructive data deletion unavailable", "UNINSTALL_UNSUPPORTED", func(o *operation) { o.Kind, o.KeepData = "uninstall", &clear }},
		{"MySQL cannot affect MariaDB", "UNINSTALL_UNSUPPORTED", func(o *operation) { o.AppID, o.Kind = "mysql", "uninstall" }},
		{"old Mosquitto package is not audited on Noble", "INSTALL_UNSUPPORTED", func(o *operation) { o.Version = "2.0.21" }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			op := base
			entry.set(&op)
			if _, err := s.submit(ctx, []operation{op}, false); !hasCode(err, entry.want) {
				t.Fatalf("expected %s, got %v", entry.want, err)
			}
		})
	}
	tasks, err := s.list(ctx, listParams{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("invalid request persisted a write task: %+v %v", tasks, err)
	}
}
