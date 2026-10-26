// boxa-core: the Boxa control plane (REST + SSE + WebSocket + MCP + boxd hub).
package main

import (
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

	"boxa/core/internal/api"
	"boxa/core/internal/manager"
	"boxa/core/internal/objstore"
	"boxa/core/internal/provider"
	"boxa/core/internal/secret"
	"boxa/core/internal/store"
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
			fmt.Println("boxa-core", manager.Version)
			return
		case "help", "--help", "-h":
			fmt.Println("boxa-core — run the Boxa control plane. Configure with env vars (see README).")
			return
		}
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("boxa-core ")
	dataDir := env("BOXA_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	dsn := env("DATABASE_URL", filepath.Join(dataDir, "boxa.db"))
	st, err := store.Open(dsn)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	master, created, err := secret.LoadOrCreateMaster(os.Getenv("BOXA_MASTER_KEY"), filepath.Join(dataDir, "master.key"))
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	if created {
		log.Printf("generated master key at %s — back it up; secrets and provider tokens are encrypted with it (or set BOXA_MASTER_KEY)", filepath.Join(dataDir, "master.key"))
	}
	crypt, err := secret.New(master)
	if err != nil {
		log.Fatal(err)
	}
	localPrice, _ := strconv.ParseFloat(env("BOXA_LOCAL_PRICE_MONTH", "0"), 64)
	setupCost, _ := strconv.ParseFloat(env("BOXA_SETUP_COST_USD", "0.002"), 64)
	var allow []string
	if v := os.Getenv("BOXA_PRICE_FEED_PROVIDERS"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				allow = append(allow, p)
			}
		}
	}
	var chain []string
	for _, p := range strings.Split(os.Getenv("BOXA_PROVIDER_CHAIN"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			chain = append(chain, p)
		}
	}
	var objCfg *objstore.Config
	if os.Getenv("BOXA_S3_BUCKET") != "" {
		objCfg = &objstore.Config{
			Provider: env("BOXA_S3_PROVIDER", "s3"), Endpoint: os.Getenv("BOXA_S3_ENDPOINT"), Region: os.Getenv("BOXA_S3_REGION"),
			Bucket: os.Getenv("BOXA_S3_BUCKET"), Prefix: env("BOXA_S3_PREFIX", "boxa"),
			AccessKeyID: os.Getenv("BOXA_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("BOXA_S3_SECRET_ACCESS_KEY"), PathStyle: true,
		}
		if !objCfg.Enabled() {
			log.Printf("warning: BOXA_S3_BUCKET set but BOXA_S3_ENDPOINT / BOXA_S3_ACCESS_KEY_ID / BOXA_S3_SECRET_ACCESS_KEY incomplete: snapshots stay local")
			objCfg = nil
		}
	}
	cfg := manager.Config{
		PublicURL:          os.Getenv("BOXA_PUBLIC_URL"),
		BaseDomain:         os.Getenv("BASE_DOMAIN"),
		BootstrapToken:     os.Getenv("BOXA_BOOTSTRAP_TOKEN"),
		AutoBurst:          envBool("BOXA_AUTO_BURST"),
		LocalPriceMonth:    localPrice,
		LedgerInterval:     envDur("BOXA_LEDGER_INTERVAL", time.Minute),
		ReaperInterval:     envDur("BOXA_REAPER_INTERVAL", 30*time.Second),
		PriceFeedInterval:  envDur("BOXA_PRICE_FEED_INTERVAL", 6*time.Hour),
		PriceFeedURL:       env("BOXA_PRICE_FEED_URL", "https://battleships.dev"),
		PriceFeedAllowlist: allow,
		DisablePriceFeed:   strings.ToLower(os.Getenv("BOXA_PRICE_FEED")) == "off",
		BoxdDownloadURL:    os.Getenv("BOXA_BOXD_URL"),
		SetupCostUSD:       setupCost,
		OrphanAfter:        envDur("BOXA_ORPHAN_AFTER", 24*time.Hour),
		BurstEmptyAfter:    envDur("BOXA_BURST_EMPTY_AFTER", 10*time.Minute),
		HostDownAfter:      envDur("BOXA_HOST_DOWN_AFTER", 2*time.Minute),
		CreateWait:         envDur("BOXA_CREATE_WAIT", 120*time.Second),
		HealthInterval:     envDur("BOXA_HEALTH_INTERVAL", time.Minute),
		CatalogStaleAfter:  envDur("BOXA_CATALOG_STALE_AFTER", 24*time.Hour),
		WarmInterval:       envDur("BOXA_WARM_INTERVAL", 10*time.Second),
		Placement:          env("BOXA_PLACEMENT", "binpack"),
		ClientLocation:     os.Getenv("BOXA_CLIENT_LOCATION"),
		HostCacheIP:        os.Getenv("BOXA_HOST_CACHE_IP"),
		Mesh: manager.MeshConfig{
			Enabled: envBool("BOXA_MESH_ENABLED"), HeadscaleURL: os.Getenv("BOXA_MESH_URL"), APIKey: os.Getenv("BOXA_MESH_API_KEY"),
			User: os.Getenv("BOXA_MESH_USER"), BaseDomain: os.Getenv("BOXA_MESH_DOMAIN"), DERPHost: os.Getenv("BOXA_MESH_DERP_HOST"),
		},
		URLKey:        secret.Hash("url:" + master),
		ProviderChain: chain,
		ObjectStore:   objCfg,
	}
	m := manager.New(st, provider.Default(nil), crypt, cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	adminKey, err := m.Init(ctx, os.Getenv("BOXA_ADMIN_KEY"))
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if adminKey != "" {
		fmt.Printf("\n==============================================================\n BOXA ADMIN KEY (shown once, store it safely):\n   %s\n==============================================================\n\n", adminKey)
		if path := os.Getenv("BOXA_ADMIN_KEY_FILE"); path != "" {
			os.WriteFile(path, []byte(adminKey+"\n"), 0o600)
		}
	}
	if cfg.BootstrapToken == "" {
		log.Printf("BOXA_BOOTSTRAP_TOKEN not set: only hosts created by core (or pre-registered via POST /v1/hosts provider=local) can connect")
	}
	if ids := m.ConfigureProvidersFromEnv(ctx, os.Getenv); len(ids) > 0 {
		log.Printf("providers connected from env: %s", strings.Join(ids, ", "))
	}
	m.Start(ctx)
	srv := &http.Server{
		Addr:              env("BOXA_ADDR", ":8080"),
		Handler:           api.New(m, os.Getenv("BOXA_DIST_DIR"), env("BOXA_CORS_ORIGINS", "*")),
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
