package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	httpapi "github.com/SergeiMurashev/bitrix-passport-exporter/internal/http"
	log "github.com/sirupsen/logrus"
)

func main() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetLevel(log.InfoLevel)

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.WithError(err).Fatal("invalid configuration")
	}
	mux := http.NewServeMux()
	h, err := httpapi.New(cfg)
	if err != nil {
		log.WithError(err).Fatal("failed to initialize handler")
	}
	defer h.Close()
	h.Register(mux)

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  time.Duration(cfg.HTTPReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.HTTPWriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(cfg.HTTPIdleTimeoutSeconds) * time.Second,
	}

	point, short := callerPoint(1)
	log.WithFields(log.Fields{
		"addr":        cfg.Addr,
		"point":       point,
		"short_point": short,
	}).Info("server started successfully")
	go func() {
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.WithError(serveErr).Fatal("http server stopped")
		}
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	sig := <-stopCh
	log.WithField("signal", sig.String()).Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HTTPShutdownTimeoutSecs)*time.Second)
	defer cancel()
	if shutdownErr := httpServer.Shutdown(ctx); shutdownErr != nil {
		log.WithError(shutdownErr).Error("graceful shutdown failed")
	}
	log.Info("server stopped")
}

func callerPoint(skip int) (string, string) {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "", ""
	}
	short := filepath.Base(filepath.Dir(file)) + "/" + filepath.Base(file) + ":" + strconv.Itoa(line)
	return file + ":" + strconv.Itoa(line), short
}
