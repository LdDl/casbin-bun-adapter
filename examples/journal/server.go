package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	casbinbunadapter "github.com/LdDl/casbin-bun-adapter"
)

func serve(ctx context.Context, reader *casbinbunadapter.JournalReader, address string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("/check", checkAccess(reader))
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	workerDone := make(chan error, 1)
	go func() {
		err := reader.Run(ctx)
		workerDone <- err
		cancel()
	}()
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		shutdownDone <- err
	}()
	err := server.ListenAndServe()
	cancel()
	shutdownErr := <-shutdownDone
	workerErr := <-workerDone
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if errors.Is(workerErr, context.Canceled) {
		workerErr = nil
	}
	return errors.Join(err, shutdownErr, workerErr)
}

func checkAccess(reader *casbinbunadapter.JournalReader) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := request.URL.Query()
		subject, object, action := query.Get("sub"), query.Get("obj"), query.Get("act")
		if subject == "" || object == "" || action == "" {
			http.Error(writer, "sub, obj and act are required", http.StatusBadRequest)
			return
		}
		allowed, err := reader.EnforceContext(request.Context(), subject, object, action)
		if err != nil {
			log.Printf("authorization check: %v", err)
			http.Error(writer, "authorization unavailable", http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.Error(writer, "denied", http.StatusForbidden)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("allowed\n"))
	}
}
