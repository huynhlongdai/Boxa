// boxd: Boxa host agent. Dials OUT to core over WebSocket and runs boxes via
// the incus driver (production) or the local driver (dev/tests).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"boxa/core/internal/boxd"
	"boxa/core/internal/manager"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// coreWS turns http(s)://host[/v1/hosts/connect] into ws(s)://host/v1/hosts/connect.
func coreWS(u string) string {
	u = strings.TrimRight(u, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + u[8:]
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + u[7:]
	}
	if !strings.HasSuffix(u, "/v1/hosts/connect") {
		u += "/v1/hosts/connect"
	}
	return u
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("boxd", manager.Version)
		return
	}
	log.SetPrefix("boxd ")
	stateDir := env("BOXD_STATE_DIR", "./boxd-state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Fatal(err)
	}
	driverName := os.Getenv("BOXD_DRIVER")
	if driverName == "" {
		if _, err := exec.LookPath("incus"); err == nil {
			driverName = "incus"
		} else {
			driverName = "local"
		}
	}
	var drv boxd.Driver
	switch driverName {
	case "incus":
		d := boxd.NewIncusDriver(env("BOXD_MEMORY_ROOT", "/var/lib/boxa/memory"), os.Getenv("BOXD_SNAPSHOT_EXPORT_DIR"))
		d.EgressMode = env("BOXD_EGRESS_MODE", "acl") // acl (Incus network ACL) | nft (nftables)
		drv = d
	case "local":
		d, err := boxd.NewLocalDriver(env("BOXD_ROOT", filepath.Join(stateDir, "root")))
		if err != nil {
			log.Fatal(err)
		}
		drv = d
		log.Printf("WARNING: local driver gives no isolation — dev/test only")
	default:
		log.Fatalf("unknown BOXD_DRIVER %q (incus|local)", driverName)
	}
	every, _ := time.ParseDuration(env("BOXD_METRICS_EVERY", "10s"))
	a, err := boxd.NewAgent(boxd.Config{
		CoreURL:        coreWS(env("BOXD_CORE_URL", "http://localhost:8080")),
		BootstrapToken: os.Getenv("BOXD_TOKEN"),
		StateDir:       stateDir,
		Provider:       env("BOXD_PROVIDER", "local"),
		Region:         os.Getenv("BOXD_REGION"),
		IP:             os.Getenv("BOXD_IP"),
		MetricsEvery:   every,
		Version:        manager.Version,
	}, drv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Printf("driver=%s core=%s state=%s", drv.Name(), coreWS(env("BOXD_CORE_URL", "http://localhost:8080")), stateDir)
	a.Run(ctx)
}
