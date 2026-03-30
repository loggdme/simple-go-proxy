package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// hop-by-hop headers that must not be forwarded
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"TE", "Trailers", "Transfer-Encoding", "Upgrade", "Proxy-Connection",
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	port := envOr("PROXY_PORT", "8888")
	auth := os.Getenv("PROXY_AUTH") // "username:password"
	certB64 := os.Getenv("PROXY_TLS_CERT_B64")
	keyB64 := os.Getenv("PROXY_TLS_KEY_B64")

	dialTimeout := parseDuration(os.Getenv("PROXY_DIAL_TIMEOUT"), 30*time.Second)

	proxy := &proxyHandler{
		auth:        auth,
		dialTimeout: dialTimeout,
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      proxy,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0, // tunnels are long-lived; rely on dial timeout upstream
		IdleTimeout:  120 * time.Second,
		// Disable HTTP/2 — proxies rely on HTTP/1.1 hijacking
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	if certB64 != "" && keyB64 != "" {
		certPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(certB64))
		if err != nil {
			slog.Error("failed to decode PROXY_TLS_CERT_B64", "err", err)
			os.Exit(1)
		}
		keyPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
		if err != nil {
			slog.Error("failed to decode PROXY_TLS_KEY_B64", "err", err)
			os.Exit(1)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			slog.Error("failed to load TLS keypair", "err", err)
			os.Exit(1)
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		slog.Info("starting HTTPS proxy", "addr", server.Addr)
		go func() {
			if err := server.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
				slog.Error("server error", "err", err)
				os.Exit(1)
			}
		}()
	} else {
		slog.Info("starting HTTP proxy", "addr", server.Addr)
		go func() {
			if err := server.ListenAndServe(); err != http.ErrServerClosed {
				slog.Error("server error", "err", err)
				os.Exit(1)
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

type proxyHandler struct {
	auth        string // "username:password"
	dialTimeout time.Duration
}

func (p *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.auth != "" && !p.checkAuth(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
		http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.handleTunnel(w, r)
	} else {
		p.handleHTTP(w, r)
	}
}

// handleTunnel handles HTTPS CONNECT tunneling.
func (p *proxyHandler) handleTunnel(w http.ResponseWriter, r *http.Request) {
	dest, err := net.DialTimeout("tcp", r.Host, p.dialTimeout)
	if err != nil {
		slog.Error("tunnel dial failed", "host", r.Host, "err", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusOK)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		dest.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		dest.Close()
		slog.Error("hijack failed", "err", err)
		return
	}

	slog.Info("tunnel", "host", r.Host, "remote", r.RemoteAddr)
	tunnel(dest, client)
}

// handleHTTP forwards plain HTTP requests.
func (p *proxyHandler) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme == "" {
		r.URL.Scheme = "http"
	}
	if r.URL.Host == "" {
		r.URL.Host = r.Host
	}

	out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	copyHeader(out.Header, r.Header)
	removeHopByHop(out.Header)

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   p.dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	resp, err := transport.RoundTrip(out)
	if err != nil {
		slog.Error("http forward failed", "url", r.URL, "err", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	slog.Info("http", "method", r.Method, "url", r.URL, "status", resp.StatusCode)

	removeHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *proxyHandler) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Proxy-Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len("Basic "):])
	if err != nil {
		return false
	}
	return string(decoded) == p.auth
}

// tunnel bidirectionally copies between two connections.
// Closing either side causes the other goroutine to exit.
func tunnel(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func removeHopByHop(h http.Header) {
	for _, k := range hopByHopHeaders {
		h.Del(k)
	}
	// also remove headers listed in the Connection header
	for _, v := range h["Connection"] {
		h.Del(v)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
