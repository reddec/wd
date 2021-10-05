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
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/jessevdk/go-flags"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/reddec/wd"
	"github.com/rs/cors"
	"golang.org/x/crypto/acme/autocert"
)

type Config struct {
	Serve CmdServe `command:"serve" description:"serve server from directory"`
	Run   CmdRun   `command:"run" description:"run single script"`
	Token CmdToken `command:"token" description:"issue token"`

	CORS           bool          `long:"cors" env:"CORS" description:"Enable CORS"`
	Bind           string        `short:"b" long:"bind" env:"BIND" description:"Binding address" default:"127.0.0.1:8080"`
	Timeout        time.Duration `short:"t" long:"timeout" env:"TIMEOUT" description:"Maximum execution timeout" default:"120s"`
	Secret         string        `short:"s" long:"secret" env:"SECRET" description:"JWT secret for checking tokens. Use token command to create token"`
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

type CmdToken struct {
	Name       string        `short:"n" long:"name" env:"NAME" description:"Name of token, will be mapped as sub"`
	Expiration time.Duration `short:"e" long:"expiration" env:"EXPIRATION" description:"Token expiration. Zero means no expiration" default:"0"`
	Args       struct {
		Hooks []string `positional-arg:"hooks" description:"allowed hooks (nothing means all hooks)"`
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
	case "token":
		err = token()
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

func token() error {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:   "wd",
		Subject:  config.Token.Name,
		Audience: config.Token.Args.Hooks,
		IssuedAt: jwt.NewNumericDate(now),
	}

	if config.Token.Expiration > 0 {
		claims.ExpiresAt = jwt.NewNumericDate(now.Add(config.Token.Expiration))
	}

	tokenString, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.Secret))
	if err != nil {
		return err
	}
	fmt.Println(tokenString)
	return nil
}

func runWebhook(global context.Context, webhook *wd.Webhook) error {
	mux := http.NewServeMux()
	if !config.DisableMetrics {
		mux.Handle("/metrics", promhttp.Handler())
	}
	if len(config.Secret) == 0 {
		mux.Handle("/", webhook)
	} else {
		mux.Handle("/", protected(config.Secret, webhook))
	}

	var handler http.Handler = mux

	if config.CORS {
		handler = cors.AllowAll().Handler(handler)
	}

	srv := http.Server{
		Addr:    config.Bind,
		Handler: handler,
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

func protected(secret string, handler *wd.Webhook) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenString := request.Header.Get("Authorization")
		if tokenString == "" {
			tokenString = request.URL.Query().Get("token")
		}
		parts := strings.Split(tokenString, " ")
		tokenString = parts[len(parts)-1]
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(secret), nil
		})
		if err != nil {
			handler.Metrics.RecordForbidden(request.URL.Path)
			writer.WriteHeader(http.StatusForbidden)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok || !token.Valid {
			handler.Metrics.RecordForbidden(request.URL.Path)
			writer.WriteHeader(http.StatusForbidden)
			return
		}

		if allowedAud, ok := claims["aud"].([]string); ok && len(allowedAud) > 0 {
			requestedAud := strings.Trim(request.URL.Path, "/")
			allowed := false
			for _, sub := range allowedAud {
				if sub == requestedAud {
					allowed = true
					break
				}
			}
			if !allowed {
				handler.Metrics.RecordForbidden(request.URL.Path)
				writer.WriteHeader(http.StatusForbidden)
				return
			}
		}

		if sub, ok := claims["sub"].(string); ok {
			log.Println("authorized request from", sub)
			request.Header.Set("X-Subject", sub)
		}

		handler.ServeHTTP(writer, request)
	})
}
