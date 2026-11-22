package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ipguard/backend/internal/ipguard"
)

// The worker owns mihomo: certificate checks, proxy probes, CF checks, DNS
// reconciliation, health recovery and every scheduled scan. Its only HTTP
// surface is a loopback control adapter for the Rust control plane.
func main() {
	initialize := flag.Bool("init", false, "初始化数据库和管理员后退出")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app, closeApp, err := ipguard.Open(ctx, *initialize)
	if err != nil {
		log.Fatal(err)
	}
	defer closeApp()
	if *initialize {
		return
	}
	ipguard.EnableLocalEngine()
	if err = app.StartBackground(ctx); err != nil {
		log.Fatal(err)
	}
	go app.RunEngine(ctx)
	addr := os.Getenv("INTERNAL_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8082"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           app.InternalHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("Go 数据面 worker 已启动：扫描、巡检、DNS 与健康复查，内部接口 %s", addr)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("内部接口退出: %v", serveErr)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}

