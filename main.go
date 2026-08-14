package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	pkgerr "github.com/pkg/errors"
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
	env := os.Getenv("ENV")
	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", env),
		slog.String("hostname", hostname),
	)
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
			ReplaceAttr: replaceAttr,
		})
		infoLogger := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			ReplaceAttr: replaceAttr,
		})
		multiHandler := slog.NewMultiHandler(debugLogger, infoLogger)
		return slog.New(multiHandler), close, nil
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})), func() error { return nil }, nil

}

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}
type multiError interface {
	error
	Unwrap() []error
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		if v, ok := a.Value.Any().(error); ok {
			if me, ok := a.Value.Any().(multiError); ok {
				var errAttrs []slog.Attr
				for i, err := range me.Unwrap() {
					er := errorAttrs(err)
					errAttrs = append(errAttrs, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), er...))
				}
				return slog.GroupAttrs("errors", errAttrs...)
			}
			return slog.GroupAttrs("error", errorAttrs(v)...)

		}
	}
	return a
}
func errorAttrs(err error) []slog.Attr {
	slogAttrs := linkoerr.Attrs(err)
	slogAttrs = append(slogAttrs, slog.Attr{
		Key:   "message",
		Value: slog.StringValue(err.Error()),
	})
	if stackErr, ok := errors.AsType[stackTracer](err); ok {

		slogAttrs = append(slogAttrs, slog.Attr{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		})
	}

	return slogAttrs
}
