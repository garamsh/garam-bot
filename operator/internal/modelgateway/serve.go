package modelgateway

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Serve serves handler over TLS 1.2 or later on listener, HTTP/2 and HTTP/1.1, until ctx ends, then
// lets the calls it holds finish for up to a minute (T-1). get answers the serving certificate, read
// again as it changes.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler,
	get func(*tls.ClientHelloInfo) (*tls.Certificate, error), log *slog.Logger) error {
	server := &http.Server{
		Handler: handler,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12, GetCertificate: get, NextProtos: []string{"h2", "http/1.1"},
		},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// The server's own errors are a client's handshakes and broken connections, which name no
		// secret and are not the gateway's to report: they are logged at debug. A call's failure is
		// the gateway's, logged where it is answered.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	log.Info("model gateway serves", "address", listener.Addr().String())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return err
	}
	return ctx.Err()
}
