// boxmes-core: the Boxmes control plane (REST + SSE + WebSocket + MCP + boxd hub).
package main

import (
	"boxmes/core/internal/envcompat"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"boxmes/core/internal/api"
	"boxmes/core/internal/manager"
	"boxmes/core/internal/objstore"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("warning: bad duration %s=%q, using %s", k, v, def)
	}
	return def
}

func envBool(k string) bool {
	v := strings.ToLower(os.Getenv(k))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("boxmes-core", manager.Version)
			return
		case "help", "--help", "-h":
			fmt.Println("boxmes-core — run the Boxmes control plane. Configure with env vars (see README).")
			return
		}
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("boxmes-core ")
	if n := envcompat.Note(envcompat.Apply()); n != "" {
		log.Printf("warning: %s", n)
	}
	dataDir := env("BOXMES_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	dbFile := filepath.Join(dataDir, "boxmes.db")
	if _, err := os.Stat(dbFile); err != nil {
		// compat: data dir from before the renames (Boxaz → Boxmes, Boxa → Boxaz)
		for _, legacy := range []string{"boxaz.db", "boxa.db"} {
			if _, err := os.Stat(filepath.Join(dataDir, legacy)); err == nil {
				dbFile = filepath.Join(dataDir, legacy)
				log.Printf("warning: using legacy database file %s (rename it to boxmes.db)", dbFile)
				break
			}
		}
	}
	dsn := env("DATABASE_URL", dbFile)
	st, err := store.Open(dsn)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	master, created, err := secret.LoadOrCreateMaster(os.Getenv("BOXMES_MASTER_KEY"), filepath.Join(dataDir, "master.key"))
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	if created {
		log.Printf("generated master key at %s — back it up; secrets and provider tokens are encrypted with it (or set BOXMES_MASTER_KEY)", filepath.Join(dataDir, "master.key"))
	}
	crypt, err := secret.New(master)
	if err != nil {
		log.Fatal(err)
	}
	localPrice, _ := strconv.ParseFloat(env("BOXMES_LOCAL_PRICE_MONTH", "0"), 64)
	setupCost, _ := strconv.ParseFloat(env("BOXMES_SETUP_COST_USD", "0.002"), 64)
	var allow []string
	if v := os.Getenv("BOXMES_PRICE_FEED_PROVIDERS"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				allow = append(allow, p)
			}
		}
	}
	var chain []string
	for _, p := range strings.Split(os.Getenv("BOXMES_PROVIDER_CHAIN"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			chain = append(chain, p)
		}
	}
	var objCfg *objstore.Config
	if os.Getenv("BOXMES_S3_BUCKET") != "" {
		objCfg = &objstore.Config{
			Provider: env("BOXMES_S3_PROVIDER", "s3"), Endpoint: os.Getenv("BOXMES_S3_ENDPOINT"), Region: os.Getenv("BOXMES_S3_REGION"),
			Bucket: os.Getenv("BOXMES_S3_BUCKET"), Prefix: os.Getenv("BOXMES_S3_PREFIX"),
			AccessKeyID: os.Getenv("BOXMES_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("BOXMES_S3_SECRET_ACCESS_KEY"), PathStyle: true,
		}
		if !objCfg.Enabled() {
			log.Printf("warning: BOXMES_S3_BUCKET set but BOXMES_S3_ENDPOINT / BOXMES_S3_ACCESS_KEY_ID / BOXMES_S3_SECRET_ACCESS_KEY incomplete: snapshots stay local")
			objCfg = nil
		}
	}
	cfg := manager.Config{
		PublicURL:          os.Getenv("BOXMES_PUBLIC_URL"),
		SignupMode:         os.Getenv("BOXMES_SIGNUP_MODE"),
		SessionTTL:         envDur("BOXMES_SESSION_TTL", 0),
		DisableTunnel:      envBool("BOXMES_DISABLE_TUNNEL"),
		AssumePublicURL:    envBool("BOXMES_INGRESS_ASSUME_PUBLIC"),
		BaseDomain:         os.Getenv("BASE_DOMAIN"),
		BootstrapToken:     os.Getenv("BOXMES_BOOTSTRAP_TOKEN"),
		AutoBurst:          envBool("BOXMES_AUTO_BURST"),
		LocalPriceMonth:    localPrice,
		LedgerInterval:     envDur("BOXMES_LEDGER_INTERVAL", time.Minute),
		ReaperInterval:     envDur("BOXMES_REAPER_INTERVAL", 30*time.Second),
		SnapshotDir:        filepath.Join(dataDir, "snapshots"),
		PriceFeedInterval:  envDur("BOXMES_PRICE_FEED_INTERVAL", 6*time.Hour),
		PriceFeedURL:       env("BOXMES_PRICE_FEED_URL", "https://battleships.dev"),
		PriceFeedAllowlist: allow,
		DisablePriceFeed:   strings.ToLower(os.Getenv("BOXMES_PRICE_FEED")) == "off",
		BoxdDownloadURL:    os.Getenv("BOXMES_BOXD_URL"),
		SetupCostUSD:       setupCost,
		OrphanAfter:        envDur("BOXMES_ORPHAN_AFTER", 24*time.Hour),
		BurstEmptyAfter:    envDur("BOXMES_BURST_EMPTY_AFTER", 10*time.Minute),
		HostDownAfter:      envDur("BOXMES_HOST_DOWN_AFTER", 2*time.Minute),
		CreateWait:         envDur("BOXMES_CREATE_WAIT", 120*time.Second),
		HealthInterval:     envDur("BOXMES_HEALTH_INTERVAL", time.Minute),
		CatalogStaleAfter:  envDur("BOXMES_CATALOG_STALE_AFTER", 24*time.Hour),
		WarmInterval:       envDur("BOXMES_WARM_INTERVAL", 10*time.Second),
		Placement:          env("BOXMES_PLACEMENT", "binpack"),
		ClientLocation:     os.Getenv("BOXMES_CLIENT_LOCATION"),
		HostCacheIP:        os.Getenv("BOXMES_HOST_CACHE_IP"),
		Mesh: manager.MeshConfig{
			Enabled: envBool("BOXMES_MESH_ENABLED"), HeadscaleURL: os.Getenv("BOXMES_MESH_URL"), APIKey: os.Getenv("BOXMES_MESH_API_KEY"),
			User: os.Getenv("BOXMES_MESH_USER"), BaseDomain: os.Getenv("BOXMES_MESH_DOMAIN"), DERPHost: os.Getenv("BOXMES_MESH_DERP_HOST"),
		},
		URLKey:          secret.Hash("url:" + master),
		ProviderChain:   chain,
		AutoSandbox:     envBool("BOXMES_AUTO_SANDBOX"),
		SandboxMode:     os.Getenv("BOXMES_SANDBOX_MODE"),
		ObjectStore:     objCfg,
		GatewayDomain:   os.Getenv("BOXMES_GATEWAY_DOMAIN"),
		ProviderPreview: os.Getenv("BOXMES_PROVIDER_PREVIEW"),
		// v1.10 access
		DashboardURL: os.Getenv("BOXMES_DASHBOARD_URL"),
		SSHAddr:      os.Getenv("BOXMES_SSH_ADDR"),
		SSHPublic:    os.Getenv("BOXMES_SSH_PUBLIC"),
		// v1.11 access P1
		SFTPMode:       os.Getenv("BOXMES_SFTP"),
		AllowMetadata:  envBool("BOXMES_EGRESS_ALLOW_METADATA"),
		BrowserInstall: os.Getenv("BOXMES_BROWSER_INSTALL"),
	}
	if d, err := time.ParseDuration(os.Getenv("BOXMES_BROWSER_IDLE")); err == nil && d > 0 {
		cfg.BrowserIdle = d
	}
	if d, err := time.ParseDuration(os.Getenv("BOXMES_DESKTOP_IDLE")); err == nil && d > 0 {
		cfg.DesktopIdle = d
	}
	for _, o := range strings.Split(os.Getenv("BOXMES_ACCESS_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AccessOrigins = append(cfg.AccessOrigins, o)
		}
	}
	if d, err := time.ParseDuration(os.Getenv("BOXMES_TERMINAL_DETACH_TTL")); err == nil && d > 0 {
		cfg.TerminalDetachTTL = d
	}
	if n, err := strconv.Atoi(os.Getenv("BOXMES_TERMINAL_BUFFER")); err == nil && n >= 4096 {
		cfg.TerminalBuffer = n
	}
	if d, err := time.ParseDuration(os.Getenv("BOXMES_WAKE_HOLD")); err == nil && d > 0 {
		cfg.WakeHold = d
	}
	m := manager.New(st, sandbox.DefaultRegistry(nil), crypt, cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	adminKey, err := m.Init(ctx, os.Getenv("BOXMES_ADMIN_KEY"))
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if adminKey != "" {
		fmt.Printf("\n==============================================================\n BOXMES ADMIN KEY (shown once, store it safely):\n   %s\n==============================================================\n\n", adminKey)
		if path := os.Getenv("BOXMES_ADMIN_KEY_FILE"); path != "" {
			os.WriteFile(path, []byte(adminKey+"\n"), 0o600)
		}
	}
	if cfg.BootstrapToken == "" {
		log.Printf("BOXMES_BOOTSTRAP_TOKEN not set: only hosts created by core (or pre-registered via POST /v1/hosts provider=local) can connect")
	}
	if ids := m.ConfigureProvidersFromEnv(ctx, os.Getenv); len(ids) > 0 {
		log.Printf("providers connected from env: %s", strings.Join(ids, ", "))
	}
	m.Start(ctx)
	if err := m.StartSSH(ctx); err != nil {
		log.Printf("ssh gateway (BOXMES_SSH_ADDR=%s): %v", cfg.SSHAddr, err)
	}
	srv := &http.Server{
		Addr:              env("BOXMES_ADDR", ":8080"),
		Handler:           api.New(m, os.Getenv("BOXMES_DIST_DIR"), env("BOXMES_CORS_ORIGINS", "*")),
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		srv.Shutdown(sctx)
	}()
	log.Printf("listening on %s (db=%s, auto_burst=%v, public_url=%q)", srv.Addr, st.Dialect(), cfg.AutoBurst, cfg.PublicURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
