package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/config"
	"winnow/internal/digest"
	"winnow/internal/jmap"
	"winnow/internal/schedule"
	"winnow/internal/sieve"
	"winnow/internal/store"
	"winnow/internal/unsubscribe"
	"winnow/internal/web"
)

// app bundles the constructed dependencies.
type app struct {
	cfg       *config.Config
	store     *store.Store
	jmap      *jmap.Client
	scheduler *schedule.Scheduler
	web       http.Handler
	log       *slog.Logger
}

// build constructs the application graph from configuration.
func build() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := st.SeedSettings(cfg.Defaults); err != nil {
		return nil, err
	}
	if err := st.SeedCategories(); err != nil {
		return nil, err
	}

	jc := jmap.New(cfg.FastmailToken)
	cl := classify.New(classify.NewAnthropic(cfg.AnthropicAPIKey), st)
	ap := actions.NewApplier(jc)
	dg := digest.New(st, jc)

	sched := schedule.New(schedule.Deps{
		Store:      st,
		Mail:       jc,
		Classifier: cl,
		Applier:    ap,
		Digester:   dg,
		Defaults:   cfg.Defaults,
		Logger:     logger,
	})

	sg := sieve.New(jc, st)
	ux := unsubscribe.NewExecutor(jc)
	dash, err := web.New(web.Deps{
		Store:         st,
		Scheduler:     sched,
		Sieve:         sg,
		Unsub:         ux,
		JMAP:          jc,
		FastmailPing:  jc,
		AnthropicPing: classify.NewAnthropic(cfg.AnthropicAPIKey),
		Config:        cfg,
	})
	if err != nil {
		return nil, fmt.Errorf("init dashboard: %w", err)
	}

	return &app{cfg: cfg, store: st, jmap: jc, scheduler: sched, web: dash.Handler(), log: logger}, nil
}

// runService runs the long-running service (scheduler + HTTP server) or, when
// invoked as `winnow sweep`, runs the one-time initial sweep.
func runService(args []string) error {
	if len(args) > 0 && args[0] == "sweep" {
		return runSweep(args[1:])
	}

	a, err := build()
	if err != nil {
		return err
	}
	defer a.store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: a.cfg.Listen, Handler: a.web, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		a.log.Info("http server listening", "addr", a.cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("http server error", "err", err)
			stop()
		}
	}()

	// Scheduler runs until the context is cancelled.
	done := make(chan struct{})
	go func() {
		a.scheduler.Run(ctx)
		close(done)
	}()

	<-ctx.Done()
	a.log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	<-done // let the current cycle finish (graceful)
	return nil
}

func runSweep(args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "apply moves (default: dry-run preview)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, err := build()
	if err != nil {
		return err
	}
	defer a.store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := a.scheduler.Sweep(ctx, *apply)
	if err != nil {
		return err
	}
	mode := "preview (dry-run)"
	if *apply {
		mode = "applied"
	}
	fmt.Printf("sweep %s: considered %d, processed %d\n", mode, res.Considered, res.Processed)
	return nil
}
