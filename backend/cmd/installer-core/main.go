package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"remoteinstaller.local/core/internal/assets"
	"remoteinstaller.local/core/internal/operations"
	"remoteinstaller.local/core/internal/remote"
	"remoteinstaller.local/core/internal/rpc"
	"remoteinstaller.local/core/internal/state"
)

var version = state.DefaultVersion

func run() error {
	data := flag.String("data", "", "用户数据目录（由桌面壳指定）")
	resources := flag.String("resources", "", "只读的内置脚本目录")
	showVersion := flag.Bool("version", false, "打印版本")
	flag.Parse()
	if *showVersion {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}
	if *data == "" || *resources == "" {
		return fmt.Errorf("请由桌面应用启动 Go 核心：缺少数据目录或脚本目录")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := state.Open(*data)
	if err != nil {
		return fmt.Errorf("无法打开新版数据库，请检查目录权限和磁盘空间")
	}
	defer st.Close()
	st.Version = version
	as, err := assets.New(*resources, *data)
	if err != nil {
		return fmt.Errorf("无法读取随客户端发布的应用目录")
	}
	as.SetCacheQuotaProvider(func(ctx context.Context) (int, error) {
		cfg, err := st.Settings(ctx)
		return cfg.CacheLimitMiB, err
	})
	srv := rpc.New(os.Stdout)
	rem := remote.New(st, srv.Emit)
	defer rem.Close()
	ops, err := operations.New(st, as, rem, srv.Emit)
	if err != nil {
		return fmt.Errorf("无法初始化任务队列")
	}
	defer ops.Close()
	go func() { <-ctx.Done(); _ = os.Stdin.Close() }()
	return srv.Serve(ctx, os.Stdin, func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		switch {
		case method == "hello":
			return map[string]any{"selected": rpc.Version, "version": version, "platform": runtime.GOOS, "arch": runtime.GOARCH, "capabilities": []string{"host.probe", "operation.detect", "operation.install", "operation.uninstall", "resources", "terminal", "files", "migration"}}, nil
		case method == "catalog.list" || strings.HasPrefix(method, "resource."):
			return as.Handle(ctx, method, raw)
		case strings.HasPrefix(method, "operation."):
			return ops.Handle(ctx, method, raw)
		case method == "host.probe" || method == "host.save" || method == "custom.run" || strings.HasPrefix(method, "terminal.") || strings.HasPrefix(method, "files."):
			return rem.Handle(ctx, method, raw)
		case method == "host.list" || method == "host.delete" || strings.HasPrefix(method, "custom.") || strings.HasPrefix(method, "settings.") || strings.HasPrefix(method, "migration."):
			return st.Handle(ctx, method, raw)
		default:
			return nil, rpc.Error("UNKNOWN_METHOD", "不受支持的本地操作")
		}
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
