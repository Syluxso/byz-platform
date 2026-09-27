package shutdown

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRunDrainsAndCloses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })}

	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, srv, Options{Listener: ln, Drain: 2 * time.Second},
			func(context.Context) error { order = append(order, "kafka"); return errors.New("flush failed") },
			func(context.Context) error { order = append(order, "pg"); return nil },
		)
	}()

	// Server is up: a request succeeds.
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "flush failed" {
			t.Fatalf("expected joined closer error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(order) != 2 || order[0] != "kafka" || order[1] != "pg" {
		t.Fatalf("closers ran %v", order)
	}
}

func TestRunListenError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	srv := &http.Server{Addr: ln.Addr().String()}
	closed := false
	err := Run(context.Background(), srv, func(context.Context) error { closed = true; return nil })
	if err == nil {
		t.Fatal("expected listen error on busy port")
	}
	if closed {
		t.Fatal("closers must not run when listen fails")
	}
}
