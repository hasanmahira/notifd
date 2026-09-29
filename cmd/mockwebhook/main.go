// mockwebhook is a 50-line stand-in for webhook.site used only by
// docker-compose. It accepts POST /webhook and returns:
//
//	202 { messageId, status:"accepted", timestamp }  (happy path)
//	<status> { error:"mock-forced" }                 (when MOCK_FORCE_STATUS set)
//
// Toggling MOCK_FORCE_STATUS at runtime requires a SIGHUP; instead we set it
// via env and restart the container — simpler and obvious.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	var forceStatus atomic.Int64
	if v := os.Getenv("MOCK_FORCE_STATUS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			forceStatus.Store(int64(n))
		}
	}

	var total, forced, ok atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if fs := int(forceStatus.Load()); fs != 0 {
			forced.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(fs)
			_, _ = w.Write([]byte(`{"error":"mock-forced"}`))
			return
		}
		resp := map[string]any{
			"messageId": "mock-" + uuid.NewString(),
			"status":    "accepted",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(resp)
		ok.Add(1)
	})

	// Runtime toggles so tests can switch modes without a restart.
	mux.HandleFunc("/admin/force", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("status")
		n, err := strconv.Atoi(code)
		if err != nil {
			http.Error(w, "status query param must be int", http.StatusBadRequest)
			return
		}
		forceStatus.Store(int64(n))
		_, _ = fmt.Fprintf(w, "force_status=%d\n", n)
	})
	mux.HandleFunc("/admin/reset", func(w http.ResponseWriter, _ *http.Request) {
		forceStatus.Store(0)
		_, _ = fmt.Fprintln(w, "force_status=0")
	})
	mux.HandleFunc("/admin/stats", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{
			"total":  total.Load(),
			"forced": forced.Load(),
			"ok":     ok.Load(),
			"force":  forceStatus.Load(),
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := ":1080"
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 3 * time.Second}

	go func() {
		log.Info("mockwebhook listening", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("listen", slog.Any("err", err))
			os.Exit(1)
		}
	}()

	ctx := make(chan os.Signal, 1)
	signal.Notify(ctx, syscall.SIGINT, syscall.SIGTERM)
	<-ctx
	_ = srv.Close()
}
