package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func main() {

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()

	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {

	logger, closeLogger, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize Logger: %v\n", err)
	}

	defer func() {
		err := closeLogger()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close Logger: %v\n", err)
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to create store: %v", err))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logger.Debug("Linko is shutting down")
	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Error(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}

type closeFunc func() error

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {

			return nil, nil, fmt.Errorf("Failed to open log file: %w", err)
		}

		bufferedFile := bufio.NewWriterSize(file, 8192)
		// multiWriter := io.MultiWriter(bufferedFile, os.Stderr)
		close := func() error {
			if err := bufferedFile.Flush(); err != nil {
				return fmt.Errorf("Failed to flush log file: %w", err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("Failed to close log file: %w", err)
			}
			return nil
		}
		debugLogger := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
		infoLogger := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		multiHandler := slog.NewMultiHandler(debugLogger, infoLogger)
		return slog.New(multiHandler), close, nil
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})), func() error { return nil }, nil

}
