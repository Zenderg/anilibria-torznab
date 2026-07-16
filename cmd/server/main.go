package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Zenderg/anilibria-torznab/internal/anilibria"
	"github.com/Zenderg/anilibria-torznab/internal/config"
	"github.com/Zenderg/anilibria-torznab/internal/httpapi"
	"github.com/Zenderg/anilibria-torznab/internal/service"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			if err := healthcheck(); err != nil {
				fmt.Fprintln(os.Stderr, "healthcheck failed")
				os.Exit(1)
			}
			return
		case "version":
			fmt.Printf("anilibria-torznab %s (commit %s, built %s)\n", version, commit, buildDate)
			return
		default:
			fmt.Fprintln(os.Stderr, "unknown command")
			os.Exit(2)
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen address is unavailable: %w", err)
	}
	defer listener.Close()

	upstream, err := anilibria.NewClient(anilibria.Config{
		BaseURL:          cfg.APIBaseURL.String(),
		Version:          strings.TrimPrefix(version, "v"),
		HTTPTimeout:      cfg.HTTPTimeout,
		RequestInterval:  cfg.RequestInterval,
		MaxConcurrency:   cfg.MaxConcurrency,
		MaxResponseBytes: cfg.MaxResponseBytes,
		Logger:           logger,
	})
	if err != nil {
		return fmt.Errorf("initialize upstream client: %w", err)
	}
	searchService, err := service.New(upstream, service.Config{
		SiteBaseURL:          cfg.SiteBaseURL.String(),
		MaxReleasesPerSearch: cfg.MaxReleasesPerSearch,
		CacheMaxEntries:      cfg.CacheMaxEntries,
		SearchCacheTTL:       cfg.SearchCacheTTL,
		TorrentsCacheTTL:     cfg.TorrentsCacheTTL,
		LatestCacheTTL:       cfg.LatestCacheTTL,
		NegativeCacheTTL:     cfg.NegativeCacheTTL,
		Logger:               logger,
	})
	if err != nil {
		return fmt.Errorf("initialize search service: %w", err)
	}
	api, err := httpapi.New(httpapi.Config{
		APIKey:         cfg.APIKey,
		RequestTimeout: cfg.RequestTimeout,
		Executor:       searchService,
		Logger:         logger,
	})
	if err != nil {
		return fmt.Errorf("initialize HTTP API: %w", err)
	}

	rootContext, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	server := &http.Server{
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		BaseContext: func(net.Listener) context.Context {
			return rootContext
		},
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	logger.Info("server started", "listen_addr", cfg.ListenAddr, "version", version)

	select {
	case serveErr := <-serveErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server failed: %w", serveErr)
		}
		return nil
	case <-signalContext.Done():
		logger.Info("server shutting down")
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		cancelRoot()
		_ = server.Close()
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	cancelRoot()
	if serveErr := <-serveErrors; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("HTTP server failed during shutdown: %w", serveErr)
	}
	logger.Info("server stopped")
	return nil
}

func healthcheck() error {
	endpoint := os.Getenv("HEALTHCHECK_URL")
	if endpoint == "" {
		address := os.Getenv("LISTEN_ADDR")
		if address == "" {
			address = ":8080"
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		endpoint = "http://" + net.JoinHostPort(host, port) + "/healthz"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected health status")
	}
	return nil
}
