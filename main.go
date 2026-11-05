// Package main serves a remote MCP server for logging food, water and
// workouts by talking to an assistant such as Gemini, with Google sign-in.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/oauth"
	"go.icco.me/foods/internal/service"
	"go.icco.me/foods/internal/store"
	"go.icco.me/foods/internal/tools"
	"go.icco.me/gutil/logging"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const serviceName = "foods"

// version is set at build time.
var version = "dev"

type config struct {
	DatabaseURL   string
	BaseURL       string
	GoogleID      string
	GoogleSecret  string
	AllowedEmails []string
	RedirectHosts []string
	USDAKey       string
	Port          string
}

func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func loadConfig(getenv func(string) string) (*config, error) {
	c := &config{
		DatabaseURL:   getenv("DATABASE_URL"),
		BaseURL:       strings.TrimRight(getenv("BASE_URL"), "/"),
		GoogleID:      getenv("GOOGLE_CLIENT_ID"),
		GoogleSecret:  getenv("GOOGLE_CLIENT_SECRET"),
		AllowedEmails: list(getenv("ALLOWED_EMAILS")),
		RedirectHosts: append(append([]string{}, oauth.DefaultRedirectHosts...), list(getenv("REDIRECT_HOSTS"))...),
		USDAKey:       getenv("USDA_API_KEY"),
		Port:          getenv("PORT"),
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	var missing []string
	for k, v := range map[string]string{"DATABASE_URL": c.DatabaseURL, "BASE_URL": c.BaseURL, "GOOGLE_CLIENT_ID": c.GoogleID, "GOOGLE_CLIENT_SECRET": c.GoogleSecret, "USDA_API_KEY": c.USDAKey} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(c.AllowedEmails) == 0 {
		missing = append(missing, "ALLOWED_EMAILS")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(c.BaseURL, "https://") && !strings.HasPrefix(c.BaseURL, "http://localhost") {
		return nil, errors.New("BASE_URL must be https")
	}
	return c, nil
}

func main() {
	log := logging.Must(logging.NewLogger(serviceName))
	defer logging.Sync(log)
	ctx, stop := signal.NotifyContext(logging.NewContext(context.Background(), log), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, log); err != nil {
		log.Fatalw("exit", zap.Error(err))
	}
}

func run(ctx context.Context, getenv func(string) string, log *zap.SugaredLogger) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	google, err := oauth.NewGoogle(ctx, cfg.GoogleID, cfg.GoogleSecret, cfg.BaseURL+"/callback")
	if err != nil {
		return err
	}
	h, err := newHandler(ctx, cfg, db, google, foodapi.New(cfg.USDAKey), log)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: ":" + cfg.Port, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warnw("shutdown", zap.Error(err))
		}
	}()
	log.Infow("listening", "addr", srv.Addr, "mcp", cfg.BaseURL+"/mcp")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func newHandler(ctx context.Context, cfg *config, db *gorm.DB, up oauth.Upstream, foods service.FoodDatabases, log *zap.SugaredLogger) (http.Handler, error) {
	st := store.New(db)
	if err := st.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	as := &oauth.Server{DB: db, Users: st, Upstream: up, BaseURL: cfg.BaseURL, AllowedEmails: cfg.AllowedEmails, RedirectHosts: cfg.RedirectHosts, Now: time.Now}
	if err := as.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate oauth: %w", err)
	}

	m := mcp.NewServer(&mcp.Implementation{Name: serviceName, Version: version}, &mcp.ServerOptions{Instructions: tools.Instructions})
	tools.Register(m, service.New(st, foods))
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return m }, &mcp.StreamableHTTPOptions{Stateless: true})
	protected := auth.RequireBearerToken(as.Verify, &auth.RequireBearerTokenOptions{ResourceMetadataURL: as.ResourceMetadataURL(), Scopes: []string{oauth.Scope}})(mcpHandler)

	r := chi.NewRouter()
	r.Use(logging.Middleware(log.Desugar()))
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "foods: a food tracking MCP server. Add %s/mcp as a custom connector.\n", cfg.BaseURL)
	})
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		sqlDB, err := db.DB()
		if err == nil {
			err = sqlDB.PingContext(r.Context())
		}
		if err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	as.Routes(r)
	r.Handle("/mcp", protected)
	return r, nil
}
