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
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/reddec/wd"
	"golang.org/x/crypto/acme/autocert"
)

type Config struct {
	Serve CmdServe `command:"serve" description:"serve server from directory"`
	Run   CmdRun   `command:"run" description:"run single script"`

	Bind           string        `short:"b" long:"bind" env:"BIND" description:"Binding address" default:"127.0.0.1:8080"`
	Timeout        time.Duration `short:"t" long:"timeout" env:"TIMEOUT" description:"Maximum execution timeout" default:"120s"`
	Tokens         []string      `short:"T" long:"tokens" env:"TOKENS" description:"Basic authorization (if at least one defined) by Authorization content or token in query"`
	Buffer         int           `short:"B" long:"buffer" env:"BUFFER" description:"Buffer response size" default:"8192"`
	DisableMetrics bool          `short:"M" long:"disable-metrics" env:"DISABLE_METRICS" description:"Disable prometheus metrics"`
	// TLS
	AutoTLS         []string `long:"auto-tls" env:"AUTO_TLS" description:"Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls"`
	AutoTLSCacheDir string   `long:"auto-tls-cache-dir" env:"AUTO_TLS_CACHE_DIR" description:"Location where to store certificates" default:".certs"`
	TLS             bool     `long:"tls" env:"TLS" description:"Enable HTTPS serving with TLS. Ignored with --auto-tls'"`
	TLSCert         string   `long:"tls-cert" env:"TLS_CERT" description:"Path to TLS certificate" default:"server.crt"`
	TLSKey          string   `long:"tls-key" env:"TLS_KEY" description:"Path to TLS key" default:"server.key"`
}

type CmdServe struct {
	WorkDir          string `short:"w" long:"work-dir" env:"WORK_DIR" description:"Working directory"`
	DisableIsolation bool   `short:"I" long:"disable-isolation" env:"DISABLE_ISOLATION" description:"Disable isolated work dirs"`
	EnableDotFiles   bool   `short:"D" long:"enable-dot-files" env:"ENABLE_DOT_FILES" description:"Enable lookup for scripts in dor directories and files"`
	Args             struct {
		Scripts string `positional-arg:"scripts-dir" required:"true" env:"SCRIPTS" description:"Scripts directory"`
	} `positional-args:"yes"`
}

type CmdRun struct {
	Args struct {
		Binary string   `positional-arg:"binary" required:"true" description:"binary to run"`
		Args   []string `positional-arg:"args"  description:"arguments"`
	} `positional-args:"yes"`
}

var config Config

func main() {
	parser := flags.NewParser(&config, flags.Default)
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	switch parser.Active.Name {
	case "serve":
		err = serve(ctx)
	case "run":
		err = run(ctx)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}

func serve(global context.Context) error {
	rootPath, err := filepath.Abs(config.Serve.Args.Scripts)
	if err != nil {
		return fmt.Errorf("detect scripts path: %w", err)
	}
	metrics := wd.NewDefaultMetrics()
	webhook := &wd.Webhook{
		TempDir:    !config.Serve.DisableIsolation,
		WorkDir:    config.Serve.WorkDir,
		Timeout:    config.Timeout,
		BufferSize: config.Buffer,
		Metrics:    metrics,
		Runner: &wd.DirectoryRunner{
			AllowDotFiles: config.Serve.EnableDotFiles,
			ScriptsDir:    rootPath,
		},
	}
	return runWebhook(global, webhook)
}

func run(global context.Context) error {
	metrics := wd.NewDefaultMetrics()
	webhook := &wd.Webhook{
		TempDir:    false,
		WorkDir:    ".",
		Timeout:    config.Timeout,
		BufferSize: config.Buffer,
		Metrics:    metrics,
		Runner:     wd.StaticScript(config.Run.Args.Binary, config.Run.Args.Args),
	}
	return runWebhook(global, webhook)
}

func runWebhook(global context.Context, webhook *wd.Webhook) error {
	mux := http.NewServeMux()
	if !config.DisableMetrics {
		mux.Handle("/metrics", promhttp.Handler())
	}
	if len(config.Tokens) == 0 {
		mux.Handle("/", webhook)
	} else {
		mux.Handle("/", protected(config.Tokens, webhook))
	}

	srv := http.Server{
		Addr:    config.Bind,
		Handler: mux,
	}

	ctx, cancel := context.WithCancel(global)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Println("started on", config.Bind)

	switch {
	case len(config.AutoTLS) > 0:
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(config.AutoTLSCacheDir),
			HostPolicy: autocert.HostWhitelist(config.AutoTLS...),
		}
		return srv.Serve(manager.Listener())
	case config.TLS:
		return srv.ListenAndServeTLS(config.TLSCert, config.TLSKey)
	default:
		return srv.ListenAndServe()
	}
}

func protected(tokens []string, handler *wd.Webhook) http.Handler {
	index := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		index[t] = true
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token := request.Header.Get("Authorization")
		if token == "" {
			token = request.URL.Query().Get("token")
		}
		if !index[token] {
			handler.Metrics.RecordForbidden(request.URL.Path)
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		handler.ServeHTTP(writer, request)
	})
}
