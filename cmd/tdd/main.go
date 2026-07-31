// Command tdd is the td server: HTTP API, web UI, and SQLite. It builds for
// linux/amd64 and ships as a container image. It is never installed on a
// workstation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/server"
	"github.com/harpchad/td/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tdd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("tdd", flag.ContinueOnError)
	addr := fs.String("addr", envOr("TD_ADDR", "127.0.0.1:8080"), "listen address")
	dbPath := fs.String("db", envOr("TD_DB", "/data/td.db"), "path to the SQLite database")
	tz := fs.String("tz", envOr("TD_TIMEZONE", "America/Chicago"), "timezone for every date-only comparison")
	seedPath := fs.String("seed", "", "load a fixture dataset and exit")
	allowOpenBind := fs.Bool("allow-unauthenticated-bind", os.Getenv("TD_ALLOW_UNAUTHENTICATED_BIND") == "1",
		"bind a non-loopback address while the API is still unauthenticated; only correct behind a namespace that is not itself reachable")
	showVersion := fs.Bool("version", false, "print the API version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Println(api.Version)
		return nil
	}

	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return fmt.Errorf("timezone %q: %w", *tz, err)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.Open(*dbPath, store.Options{Location: loc})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if *seedPath != "" {
		data, err := seed.Load(*seedPath)
		if err != nil {
			return err
		}
		if err := st.Seed(context.Background(), data); err != nil {
			return err
		}
		log.Info("seeded", "path", *seedPath, "tasks", len(data.Tasks), "clock", data.Now)
		return nil
	}

	// Phase 1 has no authentication: it lands in phase 2, before anything is
	// exposed rather than after. Until then the server refuses to bind an
	// address anything but the local machine can reach.
	if err := requireLoopback(*addr, *allowOpenBind); err != nil {
		return err
	}
	if *allowOpenBind && !isLoopback(*addr) {
		log.Warn("bound a non-loopback address with no authentication in front of it",
			"addr", *addr,
			"why", "-allow-unauthenticated-bind was passed",
			"remove", "phase 2")
	}

	srv := server.New(st, log)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdown); err != nil {
			log.Error("shutdown", "err", err)
		}
	}()

	log.Info("serving", "addr", *addr, "db", *dbPath, "tz", loc.String(), "api", api.Version)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// requireLoopback refuses a publicly reachable bind while the API is still
// unauthenticated. The server is going on the internet in phase 2, and an
// open one in the meantime is exactly the accident this prevents.
//
// The container publishes its port to the host's loopback only, so it does
// need to bind 0.0.0.0 inside its own namespace. That case passes
// -allow-unauthenticated-bind, which is deliberately long to type and
// deliberately easy to grep for when phase 2 removes it.
func requireLoopback(addr string, allowed bool) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("addr %q: %w", addr, err)
	}
	if isLoopback(addr) || allowed {
		return nil
	}
	return fmt.Errorf(
		"refusing to bind %s: the API is unauthenticated until phase 2. Use a loopback address, "+
			"or pass -allow-unauthenticated-bind if something else is keeping this port private",
		addr)
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
