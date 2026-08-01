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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/blob"
	"github.com/harpchad/td/internal/memos"
	"github.com/harpchad/td/internal/msgraph"
	"github.com/harpchad/td/internal/notify"
	"github.com/harpchad/td/internal/plugins/planner"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/server"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/web"
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
	nowFlag := fs.String("now", "", "pin the clock to an RFC3339 instant, or to @<seed file> to take the fixture's. Development only")
	baseURL := fs.String("base-url", envOr("TD_BASE_URL", ""),
		"the server's own public URL. Required: OAuth discovery, the ntfy click-through, and the resource claim all depend on knowing it")
	trustedProxies := fs.String("trusted-proxies", envOr("TD_TRUSTED_PROXIES", ""),
		"comma separated CIDRs whose X-Forwarded-For is believed. Empty trusts nothing")
	themeDir := fs.String("themes", envOr("TD_THEME_DIR", ""),
		"directory of extra theme files. Defaults to <config>/themes")
	configPath := fs.String("config", envOr("TD_CONFIG", ""),
		"path to config.toml. Defaults to <config>/config.toml. A commented default is written if none exists")
	ntfyTopic := fs.String("ntfy-topic", os.Getenv("TD_NTFY_TOPIC"),
		"ntfy topic for reminders, overriding config.toml. Empty leaves reminders off")
	blobDir := fs.String("blobs", envOr("TD_BLOB_DIR", ""),
		"directory for attachment bytes. Defaults to <db directory>/blobs")
	showVersion := fs.Bool("version", false, "print the build and API versions and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		// Two numbers, because they answer different questions. The build is
		// which image this is; the API version is what the client compares
		// against in the skew handshake.
		fmt.Printf("tdd %s (api %s)\n", version, api.Version)
		return nil
	}

	// A trailing word is a subcommand: `tdd account create`, and equally
	// `tdd -db /data/td.db account create`. Flag parsing stops at the first
	// non-flag argument, so the subcommand keeps its own flags.
	//
	// These open the database directly and never start the HTTP server, which
	// is what makes account creation impossible to reach from outside the box.
	if fs.NArg() > 0 {
		return subcommand(fs.Args(), *dbPath, *tz)
	}

	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return fmt.Errorf("timezone %q: %w", *tz, err)
	}

	pinned, err := parseClock(*nowFlag, loc)
	if err != nil {
		return err
	}
	if pinned != nil {
		loc = pinned.Location()
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

	// The server refuses to start rather than guess its own public URL:
	// OAuth discovery, the ntfy click-through, and the resource claim all
	// depend on it, and a wrong guess fails in ways that look like an
	// application bug.
	if strings.TrimSpace(*baseURL) == "" {
		return errors.New("-base-url is required. Set it to the URL this server is reached at, for example https://td.example.com")
	}
	if _, err := url.ParseRequestURI(*baseURL); err != nil {
		return fmt.Errorf("-base-url %q: %w", *baseURL, err)
	}

	networks, err := parseTrustedProxies(*trustedProxies)
	if err != nil {
		return err
	}

	srv, err := server.New(st, log)
	if err != nil {
		return err
	}
	srv.TrustedProxies = networks

	// The browser UI. Themes are files: a palette that fails the contrast
	// floor is logged and skipped rather than loaded unreadable.
	dir := *themeDir
	if dir == "" {
		dir = defaultThemeDir()
	}
	if err := srv.AttachWeb(web.Load(dir, log), dir); err != nil {
		return err
	}

	// Attachments are content-addressed files next to the database. They are
	// never served from a static handler: every download goes through the
	// same authentication as the rest of /api/v1.
	blobs, err := blob.New(blobRoot(*blobDir, *dbPath))
	if err != nil {
		return err
	}
	srv.AttachBlobs(blobs)

	// MCP at /mcp in the same binary, over the same service layer. The base
	// URL is what every discovery document is built from.
	srv.AttachMCP(*baseURL)

	// The OAuth signing keys. Two from the first start rather than one now
	// and a second at the first rotation: a rotation path that has never run
	// is a rotation path that does not work.
	if _, err := st.EnsureSigningKeys(context.Background(), time.Now()); err != nil {
		return fmt.Errorf("oauth signing keys: %w", err)
	}

	// The sync mirrors run here rather than from a laptop, so a mirror stays
	// current whether or not anybody is at a terminal. They are configured
	// from the settings page; nothing about them lives in config.toml.
	plannerRunner := &planner.Runner{Store: st, Identity: msgraph.New(), Loc: loc}
	srv.AttachPlugins(plannerRunner)

	// Config resolves flags over environment over file, and a commented
	// default is written on first start.
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	cfg, err := notify.LoadServerConfig(cfgPath)
	if err != nil {
		return err
	}
	if *ntfyTopic != "" {
		cfg.Notify.Topic = *ntfyTopic
	}
	if err := cfg.Notify.Validate(); err != nil {
		return err
	}
	if err := cfg.Memos.Validate(); err != nil {
		return err
	}

	if configured, err := st.HasAccount(context.Background()); err == nil && !configured {
		log.Warn("no account configured, every route answers 503",
			"fix", "run: tdd account create")
	}
	if len(networks) == 0 && !isLoopback(*addr) {
		// Behind a reverse proxy with nothing trusted, every request looks
		// like it came from the proxy and the per-IP login limit collapses
		// into one global limit. That fails toward locking the account holder
		// out rather than toward letting an attacker through, but it is not
		// what anyone intends.
		log.Warn("no trusted proxies configured, the login rate limit will count every request as one client",
			"fix", "set -trusted-proxies to your reverse proxy's CIDR")
	}
	if pinned != nil {
		// A pinned clock is what makes a running server agree with the case
		// files in testdata/, which all evaluate against one fixed instant.
		// It is loud on purpose: every date predicate and the whole sort order
		// depend on it, so a server left running this way would answer
		// plausible nonsense.
		at := *pinned
		srv.Now = func() time.Time { return at }
		log.Warn("clock is pinned, this server is not telling the time",
			"now", at.Format(time.RFC3339), "tz", loc.String())
	}

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

	// The scheduler is a single goroutine on a 60 second tick. No job queue,
	// no cron container. It also carries the housekeeping that has been
	// waiting for something that runs on a tick.
	scheduler := &notify.Scheduler{
		Policy: cfg.Notify, Store: st, Sender: notify.NewHTTPSender(cfg.Notify.Topic),
		BaseURL: *baseURL, Loc: loc, Log: log,
		Now:         func() time.Time { return time.Now().In(loc) },
		ActionToken: cfg.Notify.ActionToken,
		Blobs:       blobs,
		Plugins: &notify.Plugins{
			Store:   st,
			Runners: map[string]notify.Runner{plannerRunner.Name(): plannerRunner},
		},
		Journal: &notify.Journal{
			Store: st, Poster: memos.NewHTTPPoster(cfg.Memos), Config: cfg.Memos,
			BaseURL: *baseURL, Loc: loc,
		},
	}
	if cfg.Memos.Enabled() {
		log.Info("journal on", "memos", cfg.Memos.URL, "visibility", cfg.Memos.Visibility)
	}
	if pinned != nil {
		at := *pinned
		scheduler.Now = func() time.Time { return at }
	}
	if cfg.Notify.Enabled() && cfg.Notify.ActionToken == "" {
		log.Warn("reminders have no action token, so the Done and Snooze buttons are omitted",
			"fix", `tdd token create -name ntfy -scopes write, then set notify.action_token`)
	}
	go scheduler.Run(ctx)

	log.Info("serving", "addr", *addr, "db", *dbPath, "tz", loc.String(),
		"base_url", *baseURL, "config", cfgPath, "api", api.Version)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// blobRoot resolves where attachment bytes live. Next to the database by
// default, since the two are one backup unit: a dump with no blobs restores a
// list of files that are not there.
func blobRoot(configured, dbPath string) string {
	if configured != "" {
		return configured
	}
	if dbPath == ":memory:" {
		return filepath.Join(os.TempDir(), "td-blobs")
	}
	return filepath.Join(filepath.Dir(dbPath), "blobs")
}

// parseClock reads the -now flag. An empty value means the real clock. A
// value starting with @ names a seed file and takes both the instant and the
// timezone from it, so `-now @testdata/seed.json` puts the server in exactly
// the environment the case files in that directory assume. Anything else is
// an RFC3339 instant, read in loc.
//
// This exists because `make seed` loads a fixture pinned to one instant, and
// a server answering from the real wall clock returns a different order for
// the same filter than sort_cases.json specifies. Hand-checking a fixture
// against a running server should agree with the test suite.
func parseClock(value string, loc *time.Location) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "@") {
		data, err := seed.Load(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, fmt.Errorf("clock from seed file: %w", err)
		}
		at, _, err := data.Clock()
		if err != nil {
			return nil, fmt.Errorf("clock from seed file: %w", err)
		}
		return &at, nil
	}
	at, err := time.ParseInLocation(time.RFC3339, value, loc)
	if err != nil {
		return nil, fmt.Errorf("-now %q is not an RFC3339 instant: %w", value, err)
	}
	at = at.In(loc)
	return &at, nil
}

// parseTrustedProxies reads the CIDR list whose X-Forwarded-For is believed.
//
// Empty trusts nothing, which is the safe default: an untrusted forwarded
// header lets a caller put a fresh address on every attempt and walk past a
// per-IP rate limit. Behind nginx-proxy-manager this has to be set, or the
// limit counts the proxy rather than the client.
func parseTrustedProxies(list string) ([]*net.IPNet, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, nil
	}
	entries := strings.Split(list, ",")
	out := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// A bare address is a /32 or /128.
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				entry = fmt.Sprintf("%s/%d", entry, bits)
			}
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", entry, err)
		}
		out = append(out, network)
	}
	return out, nil
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

// defaultConfigPath is $XDG_CONFIG_HOME/td/config.toml.
func defaultConfigPath() string {
	if dir := configHome(); dir != "" {
		return filepath.Join(dir, "td", "config.toml")
	}
	return ""
}

func configHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// defaultThemeDir is $XDG_CONFIG_HOME/td/themes, so a palette you like is a
// file drop rather than a pull request.
func defaultThemeDir() string {
	if dir := configHome(); dir != "" {
		return filepath.Join(dir, "td", "themes")
	}
	return ""
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// subcommand dispatches the server-side commands.
//
// None of them starts the HTTP server or needs -base-url: they are operations
// on the database, run by whoever can already reach the box.
func subcommand(args []string, dbPath, tz string) error {
	name, rest := args[0], args[1:]

	openStore := func() (*store.Store, error) {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("timezone %q: %w", tz, err)
		}
		return store.Open(dbPath, store.Options{Location: loc})
	}

	switch name {
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		return nil

	case "account":
		if len(rest) == 0 {
			return errors.New("account takes create, show, or log")
		}
		st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()

		switch rest[0] {
		case "create":
			return accountCreate(context.Background(), st, rest[1:], os.Stdin, os.Stdout)
		case "show":
			return accountShow(context.Background(), st, os.Stdout)
		case "log":
			return accountLog(context.Background(), st, rest[1:], os.Stdout)
		default:
			return fmt.Errorf("unknown account command %q", rest[0])
		}

	case "token":
		st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		return tokenCommand(context.Background(), st, rest, os.Stdout)

	default:
		return fmt.Errorf("unknown command %q\n\n%s", name, usage)
	}
}
