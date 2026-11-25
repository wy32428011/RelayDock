package remote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseChmodMode(t *testing.T) {
	if text, mode, err := parseChmodMode("755"); err != nil || text != "755" || mode.Perm() != 0o755 {
		t.Fatalf("parseChmodMode(755) = (%q, %v, %v)", text, mode, err)
	}
	if text, mode, err := parseChmodMode("1776"); err != nil || text != "1776" || posixPermBits(mode) != 0o1776 {
		t.Fatalf("parseChmodMode(1776) = (%q, %#o, %v)", text, posixPermBits(mode), err)
	}
	for _, bad := range []string{"", "75", "75555", "888", "rwx", "-755"} {
		if _, _, err := parseChmodMode(bad); err == nil {
			t.Fatalf("expected error for invalid mode %q", bad)
		}
	}
}

func TestFileChmodSFTPAndRecursive(t *testing.T) {
	fx := startSSH(t, false, false, nil)
	store := newMultiHostStore()
	store.add("host-a", fx)
	svc := newService(store, nil)
	defer svc.Close()

	// Seed a directory and nested file via SFTP.
	c := dialFixtureSFTP(t, svc, "host-a")
	defer c.Close()
	if err := c.MkdirAll("/app/sub"); err != nil {
		t.Fatal(err)
	}
	f, err := c.Create("/app/sub/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("#!/bin/sh\n"))
	_ = f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Must reject without confirmation.
	rawNoConfirm, _ := json.Marshal(map[string]any{
		"hostId":    "host-a",
		"path":      "/app/sub/run.sh",
		"mode":      "755",
		"confirmed": false,
	})
	if _, err := svc.Handle(ctx, "files.chmod", rawNoConfirm); err == nil {
		t.Fatal("expected error when confirmed=false")
	}

	// Must reject root path "/".
	rawRoot, _ := json.Marshal(map[string]any{
		"hostId":    "host-a",
		"path":      "/",
		"mode":      "755",
		"confirmed": true,
	})
	if _, err := svc.Handle(ctx, "files.chmod", rawRoot); err == nil {
		t.Fatal("expected error when chmod target is root /")
	}

	// Recursive SFTP chmod (fast=false, sudo=false).
	rawSFTP, _ := json.Marshal(map[string]any{
		"hostId":    "host-a",
		"path":      "/app",
		"mode":      "750",
		"recursive": true,
		"fast":      false,
		"sudo":      false,
		"confirmed": true,
	})
	res, err := svc.Handle(ctx, "files.chmod", rawSFTP)
	if err != nil {
		t.Fatalf("files.chmod SFTP recursive failed: %v", err)
	}
	out, ok := res.(chmodResult)
	if !ok || !out.OK || out.Updated != 3 {
		t.Fatalf("unexpected chmodResult: %+v", res)
	}

	// files.list should include POSIX mode bits.
	rawList, _ := json.Marshal(map[string]any{"hostId": "host-a", "path": "/app/sub"})
	listRes, err := svc.Handle(ctx, "files.list", rawList)
	if err != nil {
		t.Fatalf("files.list failed: %v", err)
	}
	dirRes, ok := listRes.(directoryResult)
	if !ok || len(dirRes.Entries) != 1 || dirRes.Entries[0].Mode == nil {
		t.Fatalf("expected files.list entry with Mode, got %+v", listRes)
	}
}

func TestFileChmodFastAndSudoCommands(t *testing.T) {
	fx := startSSH(t, false, false, nil)
	store := newMultiHostStore()
	store.add("host-a", fx)
	svc := newService(store, nil)
	defer svc.Close()

	c := dialFixtureSFTP(t, svc, "host-a")
	defer c.Close()
	if err := c.MkdirAll("/opt/data"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Fast mode invokes SSH chmod -R.
	rawFast, _ := json.Marshal(map[string]any{
		"hostId":    "host-a",
		"path":      "/opt/data",
		"mode":      "776",
		"recursive": true,
		"fast":      true,
		"sudo":      false,
		"confirmed": true,
	})
	if _, err := svc.Handle(ctx, "files.chmod", rawFast); err != nil {
		t.Fatalf("files.chmod fast failed: %v", err)
	}

	// Sudo mode invokes SSH sudo -n chmod.
	rawSudo, _ := json.Marshal(map[string]any{
		"hostId":    "host-a",
		"path":      "/opt/data",
		"mode":      "755",
		"recursive": false,
		"fast":      true,
		"sudo":      true,
		"confirmed": true,
	})
	if _, err := svc.Handle(ctx, "files.chmod", rawSudo); err != nil {
		t.Fatalf("files.chmod sudo failed: %v", err)
	}

	fx.mu.Lock()
	logged := append([]string(nil), fx.log...)
	fx.mu.Unlock()

	var sawFast, sawSudo bool
	for _, cmd := range logged {
		if cmd == "chmod -R 776 -- '/opt/data'" {
			sawFast = true
		}
		if strings.HasPrefix(cmd, "sudo -n chmod 755 -- '/opt/data'") {
			sawSudo = true
		}
	}
	if !sawFast || !sawSudo {
		t.Fatalf("expected fast and sudo chmod commands in SSH log, got %v", logged)
	}
}
