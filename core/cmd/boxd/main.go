// boxd: Boxaz host agent. Dials OUT to core over WebSocket and runs boxes via
// the incus driver (production) or the local driver (dev/tests).
package main

import (
	"boxaz/core/internal/envcompat"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"boxaz/core/internal/boxd"
	"boxaz/core/internal/manager"
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
	if n := envcompat.Note(envcompat.Apply()); n != "" {
		log.Printf("boxd: warning: %s", n)
	}
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("boxd", manager.Version)
		return
	}
	// `boxd ingress --dial <ws url> --box <id> --token <tok>`: the in-sandbox
	// public-URL agent (see internal/boxd/ingressagent.go).
	if len(os.Args) > 1 && os.Args[1] == "ingress" {
		runIngress(os.Args[2:])
		return
	}
	// v1.4: `boxd --dial <core url> [--token <bootstrap token>]` (used inside
	// T2 sandboxes) is the same as BOXD_CORE_URL / BOXD_TOKEN.
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		val := ""
		if k, v, ok := strings.Cut(a, "="); ok {
			a, val = k, v
		} else if i+1 < len(os.Args) {
			val = os.Args[i+1]
		}
		switch a {
		case "--dial", "-dial":
			os.Setenv("BOXD_CORE_URL", val)
		case "--token", "-token":
			os.Setenv("BOXD_TOKEN", val)
		default:
			continue
		}
		if !strings.Contains(os.Args[i], "=") {
			i++
		}
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
		d := boxd.NewIncusDriver(env("BOXD_MEMORY_ROOT", memoryRootDefault()), os.Getenv("BOXD_SNAPSHOT_EXPORT_DIR"))
		d.EgressMode = env("BOXD_EGRESS_MODE", "acl") // acl (Incus network ACL) | nft (nftables)
		drv = d
	case "local", "sandbox":
		d, err := boxd.NewLocalDriver(env("BOXD_ROOT", filepath.Join(stateDir, "root")))
		if err != nil {
			log.Fatal(err)
		}
		drv = d
		if driverName == "local" {
			log.Printf("WARNING: local driver gives no isolation — dev/test only")
		} else {
			log.Printf("sandbox mode: one box per T2 sandbox; isolation comes from the sandbox provider")
		}
	default:
		log.Fatalf("unknown BOXD_DRIVER %q (incus|local|sandbox)", driverName)
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
	startPortHolder(ctx)
	log.Printf("driver=%s core=%s state=%s", drv.Name(), coreWS(env("BOXD_CORE_URL", "http://localhost:8080")), stateDir)
	a.Run(ctx)
}

// startPortHolder starts the provider port holder and keepalive when the
// provider runs boxd as a web service (Render): BOXD_LISTEN_PORT (may be
// "${PORT}"), BOXD_HEALTH_PATH, BOXD_PREVIEW_PORT (default 8000),
// BOXD_KEEPALIVE_URL (may reference ${RENDER_EXTERNAL_URL}),
// BOXD_KEEPALIVE_EVERY (default 5m). Values are expanded against the
// environment because the provider does not interpolate them.
func startPortHolder(ctx context.Context) {
	if lp := strings.TrimSpace(os.ExpandEnv(os.Getenv("BOXD_LISTEN_PORT"))); lp != "" {
		target, err := strconv.Atoi(env("BOXD_PREVIEW_PORT", "8000"))
		if err != nil || target < 0 {
			target = 8000 // 0 = publish nothing (health path only)
		}
		go boxd.RunPortHolder(ctx, boxd.PortHolderConfig{Listen: ":" + lp, HealthPath: os.Getenv("BOXD_HEALTH_PATH"), Target: target})
	}
	if ku := strings.TrimSpace(os.ExpandEnv(os.Getenv("BOXD_KEEPALIVE_URL"))); strings.HasPrefix(ku, "http") {
		every, _ := time.ParseDuration(env("BOXD_KEEPALIVE_EVERY", "5m"))
		go boxd.RunKeepAlive(ctx, ku, every)
	}
}

// memoryRootDefault is /var/lib/boxd/memory, or the pre-rename
// /var/lib/boxa/memory when only that one exists on this host.
func memoryRootDefault() string {
	const cur, legacy = "/var/lib/boxd/memory", "/var/lib/boxa/memory"
	if _, err := os.Stat(cur); err != nil {
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return cur
}

func runIngress(args []string) {
	log.SetPrefix("boxd-ingress ")
	cfg := boxd.IngressAgentConfig{}
	for i := 0; i < len(args); i++ {
		k, v, ok := strings.Cut(args[i], "=")
		if !ok && i+1 < len(args) {
			v = args[i+1]
			i++
		}
		switch strings.TrimLeft(k, "-") {
		case "dial":
			cfg.CoreURL = v
		case "box":
			cfg.BoxID = v
		case "token":
			cfg.Token = v
		case "target":
			cfg.Target = v
		}
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("BOXD_INGRESS_TOKEN")
	}
	if cfg.CoreURL == "" || cfg.BoxID == "" || cfg.Token == "" {
		log.Fatal("usage: boxd ingress --dial <ws(s)://core/v1/ingress/connect> --box <box id> --token <token>")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := boxd.RunIngressAgent(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
