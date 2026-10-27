// boxaz-core: the Boxaz control plane (REST + SSE + WebSocket + MCP + boxd hub).
package main

import (
	"boxaz/core/internal/envcompat"
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

	"boxaz/core/internal/api"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/objstore"
	"boxaz/core/internal/sandbox"
	"boxaz/core/internal/secret"
	"boxaz/core/internal/store"
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
			fmt.Println("boxaz-core", manager.Version)
			return
		case "help", "--help", "-h":
			fmt.Println("boxaz-core — run the Boxaz control plane. Configure with env vars (see README).")
			return
		}
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("boxaz-core ")
	if n := envcompat.Note(envcompat.Apply()); n != "" {
		log.Printf("warning: %s", n)
	}
	dataDir := env("BOXAZ_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	dbFile := filepath.Join(dataDir, "boxaz.db")
	if _, err := os.Stat(dbFile); err != nil {
		if _, err := os.Stat(filepath.Join(dataDir, "boxa.db")); err == nil {
			dbFile = filepath.Join(dataDir, "boxa.db") // compat: data dir from before the rename
			log.Printf("warning: using legacy database file %s (rename it to boxaz.db)", dbFile)
		}
	}
	dsn := env("DATABASE_URL", dbFile)
	st, err := store.Open(dsn)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	master, created, err := secret.LoadOrCreateMaster(os.Getenv("BOXAZ_MASTER_KEY"), filepath.Join(dataDir, "master.key"))
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	if created {
		log.Printf("generated master key at %s — back it up; secrets and provider tokens are encrypted with it (or set BOXAZ_MASTER_KEY)", filepath.Join(dataDir, "master.key"))
	}
	crypt, err := secret.New(master)
	if err != nil {
		log.Fatal(err)
	}
	localPrice, _ := strconv.ParseFloat(env("BOXAZ_LOCAL_PRICE_MONTH", "0"), 64)
	setupCost, _ := strconv.ParseFloat(env("BOXAZ_SETUP_COST_USD", "0.002"), 64)
	var allow []string
	if v := os.Getenv("BOXAZ_PRICE_FEED_PROVIDERS"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				allow = append(allow, p)
			}
		}
	}
	var chain []string
	for _, p := range strings.Split(os.Getenv("BOXAZ_PROVIDER_CHAIN"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			chain = append(chain, p)
		}
	}
	var objCfg *objstore.Config
	if os.Getenv("BOXAZ_S3_BUCKET") != "" {
		objCfg = &objstore.Config{
			Provider: env("BOXAZ_S3_PROVIDER", "s3"), Endpoint: os.Getenv("BOXAZ_S3_ENDPOINT"), Region: os.Getenv("BOXAZ_S3_REGION"),
			Bucket: os.Getenv("BOXAZ_S3_BUCKET"), Prefix: os.Getenv("BOXAZ_S3_PREFIX"),
			AccessKeyID: os.Getenv("BOXAZ_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("BOXAZ_S3_SECRET_ACCESS_KEY"), PathStyle: true,
		}
		if !objCfg.Enabled() {
			log.Printf("warning: BOXAZ_S3_BUCKET set but BOXAZ_S3_ENDPOINT / BOXAZ_S3_ACCESS_KEY_ID / BOXAZ_S3_SECRET_ACCESS_KEY incomplete: snapshots stay local")
			objCfg = nil
		}
	}
	cfg := manager.Config{
		PublicURL:          os.Getenv("BOXAZ_PUBLIC_URL"),
		DisableTunnel:      envBool("BOXAZ_DISABLE_TUNNEL"),
		AssumePublicURL:    envBool("BOXAZ_INGRESS_ASSUME_PUBLIC"),
		BaseDomain:         os.Getenv("BASE_DOMAIN"),
		BootstrapToken:     os.Getenv("BOXAZ_BOOTSTRAP_TOKEN"),
		AutoBurst:          envBool("BOXAZ_AUTO_BURST"),
		LocalPriceMonth:    localPrice,
		LedgerInterval:     envDur("BOXAZ_LEDGER_INTERVAL", time.Minute),
		ReaperInterval:     envDur("BOXAZ_REAPER_INTERVAL", 30*time.Second),
		SnapshotDir:        filepath.Join(dataDir, "snapshots"),
		PriceFeedInterval:  envDur("BOXAZ_PRICE_FEED_INTERVAL", 6*time.Hour),
		PriceFeedURL:       env("BOXAZ_PRICE_FEED_URL", "https://battleships.dev"),
		PriceFeedAllowlist: allow,
		DisablePriceFeed:   strings.ToLower(os.Getenv("BOXAZ_PRICE_FEED")) == "off",
		BoxdDownloadURL:    os.Getenv("BOXAZ_BOXD_URL"),
		SetupCostUSD:       setupCost,
		OrphanAfter:        envDur("BOXAZ_ORPHAN_AFTER", 24*time.Hour),
		BurstEmptyAfter:    envDur("BOXAZ_BURST_EMPTY_AFTER", 10*time.Minute),
		HostDownAfter:      envDur("BOXAZ_HOST_DOWN_AFTER", 2*time.Minute),
		CreateWait:         envDur("BOXAZ_CREATE_WAIT", 120*time.Second),
		HealthInterval:     envDur("BOXAZ_HEALTH_INTERVAL", time.Minute),
		CatalogStaleAfter:  envDur("BOXAZ_CATALOG_STALE_AFTER", 24*time.Hour),
		WarmInterval:       envDur("BOXAZ_WARM_INTERVAL", 10*time.Second),
		Placement:          env("BOXAZ_PLACEMENT", "binpack"),
		ClientLocation:     os.Getenv("BOXAZ_CLIENT_LOCATION"),
		HostCacheIP:        os.Getenv("BOXAZ_HOST_CACHE_IP"),
		Mesh: manager.MeshConfig{
			Enabled: envBool("BOXAZ_MESH_ENABLED"), HeadscaleURL: os.Getenv("BOXAZ_MESH_URL"), APIKey: os.Getenv("BOXAZ_MESH_API_KEY"),
			User: os.Getenv("BOXAZ_MESH_USER"), BaseDomain: os.Getenv("BOXAZ_MESH_DOMAIN"), DERPHost: os.Getenv("BOXAZ_MESH_DERP_HOST"),
		},
		URLKey:          secret.Hash("url:" + master),
		ProviderChain:   chain,
		AutoSandbox:     envBool("BOXAZ_AUTO_SANDBOX"),
		SandboxMode:     os.Getenv("BOXAZ_SANDBOX_MODE"),
		ObjectStore:     objCfg,
		GatewayDomain:   os.Getenv("BOXAZ_GATEWAY_DOMAIN"),
		ProviderPreview: os.Getenv("BOXAZ_PROVIDER_PREVIEW"),
	}
	if d, err := time.ParseDuration(os.Getenv("BOXAZ_WAKE_HOLD")); err == nil && d > 0 {
		cfg.WakeHold = d
	}
	m := manager.New(st, sandbox.DefaultRegistry(nil), crypt, cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	adminKey, err := m.Init(ctx, os.Getenv("BOXAZ_ADMIN_KEY"))
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if adminKey != "" {
		fmt.Printf("\n==============================================================\n BOXAZ ADMIN KEY (shown once, store it safely):\n   %s\n==============================================================\n\n", adminKey)
		if path := os.Getenv("BOXAZ_ADMIN_KEY_FILE"); path != "" {
			os.WriteFile(path, []byte(adminKey+"\n"), 0o600)
		}
	}
	if cfg.BootstrapToken == "" {
		log.Printf("BOXAZ_BOOTSTRAP_TOKEN not set: only hosts created by core (or pre-registered via POST /v1/hosts provider=local) can connect")
	}
	if ids := m.ConfigureProvidersFromEnv(ctx, os.Getenv); len(ids) > 0 {
		log.Printf("providers connected from env: %s", strings.Join(ids, ", "))
	}
	m.Start(ctx)
	srv := &http.Server{
		Addr:              env("BOXAZ_ADDR", ":8080"),
		Handler:           api.New(m, os.Getenv("BOXAZ_DIST_DIR"), env("BOXAZ_CORS_ORIGINS", "*")),
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
