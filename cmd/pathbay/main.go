package main

import (
	"context"
	"flag"
	"github.com/lsy223622/PathBay/internal/pathbay"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	if offline(os.Args[1:]) {
		return
	}
	listen := flag.String("listen", env("PATHBAY_LISTEN", "127.0.0.1:7284"), "listen address")
	dir := flag.String("data-dir", env("PATHBAY_DATA_DIR", "data"), "instance data directory")
	proxies := flag.String("trusted-proxies", env("PATHBAY_TRUSTED_PROXIES", "127.0.0.1/32,::1/128"), "trusted proxy CIDRs")
	upload := flag.Int64("upload-limit", 64<<20, "maximum upload bytes")
	flag.Parse()
	o := pathbay.Options{DataDir: *dir, UploadLimit: *upload}
	for _, p := range strings.Split(*proxies, ",") {
		v, e := netip.ParsePrefix(strings.TrimSpace(p))
		if e != nil {
			log.Fatal(e)
		}
		o.TrustedProxies = append(o.TrustedProxies, v)
	}
	a, err := pathbay.Open(o)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: a, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan os.Signal, 1)
	shutdownDone := make(chan struct{})
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("request shutdown: %v", err)
			_ = server.Close()
		}
		close(shutdownDone)
	}()
	log.Printf("PathBay listening on %s", *listen)
	err = server.ListenAndServe()
	if err != http.ErrServerClosed {
		log.Print(err)
	} else {
		<-shutdownDone
	}
	if e := a.Close(); e != nil {
		log.Print(e)
	}
}
