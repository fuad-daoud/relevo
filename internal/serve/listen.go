package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

var ErrNoTLS = errors.New("no certificate; run relevo serve init or pass --insecure-http")

type ListenConfig struct {
	Addr         string           // e.g. ":7777"
	TLS          *tls.Certificate // nil only with InsecureHTTP
	InsecureHTTP bool
}

func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// ListenAndServe starts the HTTP/HTTPS server and the daemon tick loop. It
// requires a TLS certificate unless InsecureHTTP is set, and on cancel drains:
// it shuts the HTTP server down with a 30-second budget, waits for Run's
// in-flight tick, and only then returns.
func (s *Server) ListenAndServe(ctx context.Context, lc ListenConfig) error {
	if lc.TLS == nil && !lc.InsecureHTTP {
		return ErrNoTLS
	}

	addr := lc.Addr
	if addr == "" {
		addr = ":7777"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.addr = ln.Addr()
	s.insecureHTTP = lc.InsecureHTTP
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.addr = nil
		s.mu.Unlock()
	}()

	listener := net.Listener(ln)
	if lc.TLS != nil {
		tlsConfig := &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{*lc.TLS},
		}
		listener = tls.NewListener(ln, tlsConfig)
	} else if lc.InsecureHTTP {
		slog.Warn("serving plain HTTP; every client request is readable on the network")
	}

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = s.Run(ctx)
	}()

	// The drain: on cancel, stop accepting and let in-flight requests finish
	// within Shutdown's budget, then wait for Run's in-flight tick and the
	// best-effort node stops beside it. Shutdown's own error (the budget ran
	// out) is a Warn; the waits happen either way.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		s.drainServer(srv, runDone)
	}()

	serveErr := srv.Serve(listener)
	if ctx.Err() != nil {
		<-drained
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}

// drainServer stops accepting and lets in-flight requests finish within the
// shutdown budget, then waits for Run's in-flight tick and the best-effort
// node stops beside it. A node stop never holds a lock and each carries its
// own timeout, but the wait still stays inside the same budget instead of
// outliving the server.
func (s *Server) drainServer(srv *http.Server, runDone chan struct{}) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("graceful shutdown did not finish", "err", err)
	}
	<-runDone
	fanoutDone := make(chan struct{})
	go func() {
		defer close(fanoutDone)
		s.fanout.Wait()
	}()
	select {
	case <-fanoutDone:
	case <-shutdownCtx.Done():
	}
}
