package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaiye/meeting-room/internal/api"
	"github.com/kaiye/meeting-room/internal/config"
	"github.com/kaiye/meeting-room/internal/jobs"
	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/summarize"
	"github.com/kaiye/meeting-room/internal/ws"
)

func main() {
	cfg := config.FromEnv()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	st, err := store.Connect(ctx, cfg.DatabasePath)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := st.SeedTemplates(ctx, summarize.SeedTemplates()); err != nil {
		log.Fatalf("seed templates: %v", err)
	}

	hub := ws.NewHub()
	worker := jobs.NewWorker(cfg, st, hub)
	go worker.Run(ctx)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: api.NewRouter(cfg, st, hub),
		// Uploads and long polls are fine; WriteTimeout stays generous.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	if cfg.APIToken == "" {
		log.Printf("WARNING: MEETING_ROOM_API_TOKEN is empty — API is unauthenticated (dev only)")
	}
	if cfg.TranscribeBaseURL == "" {
		log.Printf("WARNING: TRANSCRIBE_BASE_URL is empty — transcription jobs will fail until configured")
	}
	log.Printf("Meeting Room listening on %s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}
