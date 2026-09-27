// Package shutdown owns the process lifecycle every main.go repeats: listen,
// wait for a signal or context cancellation, drain in-flight requests, then
// close dependencies in order.
//
// Frozen verb: Run.
package shutdown

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Closer releases a dependency during shutdown: flush Kafka, close Postgres.
type Closer func(ctx context.Context) error

// Options tunes Run. Zero values are sensible.
type Options struct {
	// Drain bounds srv.Shutdown and each Closer. Default 10s.
	Drain time.Duration
	// Logger receives listen/shutdown lines. Default slog.Default().
	Logger *slog.Logger
	// Listener, when set, is served instead of srv.Addr. Tests use ":0".
	Listener net.Listener
}

// Run serves srv until ctx is cancelled or SIGINT/SIGTERM arrives, then
// drains and runs closers in the order given. Every closer runs even when an
// earlier one fails; errors are joined. A listen failure returns immediately.
func Run(ctx context.Context, srv *http.Server, closers ...Closer) error {
	return RunWith(ctx, srv, Options{}, closers...)
}

// RunWith is Run with Options.
func RunWith(ctx context.Context, srv *http.Server, opts Options, closers ...Closer) error {
	if opts.Drain <= 0 {
		opts.Drain = 10 * time.Second
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = 10 * time.Second
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		var err error
		if opts.Listener != nil {
			log.Info("listening", "addr", opts.Listener.Addr().String())
			err = srv.Serve(opts.Listener)
		} else {
			log.Info("listening", "addr", srv.Addr)
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	var errs []error
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down", "drain", opts.Drain.String())
		dctx, cancel := context.WithTimeout(context.Background(), opts.Drain)
		if err := srv.Shutdown(dctx); err != nil {
			errs = append(errs, err)
		}
		cancel()
		<-errCh
	}

	for _, c := range closers {
		cctx, cancel := context.WithTimeout(context.Background(), opts.Drain)
		if err := c(cctx); err != nil {
			errs = append(errs, err)
		}
		cancel()
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	log.Info("stopped")
	return nil
}
