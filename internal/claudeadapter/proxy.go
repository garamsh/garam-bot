package claudeadapter

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"time"
)

// proxy is a loopback-only reverse proxy of one upstream. It exists because Claude Code reads a
// credential once, or on a timer, and the interface asks for the token file at each request
// (MODEL-1, TOOL-1): the harness is pointed at the proxy, which reads the file for every request
// it forwards and sets the Authorization header itself.
//
// It forwards to the one upstream it was made for and to nothing else (T-1): a request that names
// another target is refused before anything is read or sent, and a redirect of the upstream is
// handed back to the harness, never followed.
type proxy struct {
	upstream  *url.URL
	tokenFile string
	// exact makes every request go to the upstream URL itself, as an MCP server is one URL. Else
	// the request's path is appended to the upstream's, as a model gateway's base URL is.
	exact bool
	log   *slog.Logger

	listener net.Listener
	server   *http.Server
}

// startProxy listens on a port the kernel picks, on 127.0.0.1 only, and serves until close.
func startProxy(upstream, tokenFile string, exact bool, log *slog.Logger) (*proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	p := &proxy{upstream: u, tokenFile: tokenFile, exact: exact, log: log, listener: l}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.server.Serve(l) }()

	return p, nil
}

// URL is where the harness reaches the proxy.
func (p *proxy) URL() string { return "http://" + p.listener.Addr().String() }

func (p *proxy) close() { _ = p.server.Close() }

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if reason := p.refusal(r); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	token, err := os.ReadFile(p.tokenFile)
	token = bytes.TrimSpace(token)
	if err != nil || len(token) == 0 {
		// Neither the path's content nor the error's text is sent on: the file's name is the
		// session's own, and the harness needs only to know that no token was there.
		p.log.Warn("the token file cannot be read", "upstream", p.upstream.Host)
		http.Error(w, "no token to forward", http.StatusBadGateway)
		return
	}
	// What the harness sent as its credential, whatever it is, is replaced.
	r.Header.Del("Authorization")
	r.Header.Del("X-Api-Key")
	r.Header.Set("Authorization", "Bearer "+string(token))
	(&httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(p.upstream)
			if p.exact {
				out.Out.URL.Path, out.Out.URL.RawPath = p.upstream.Path, p.upstream.RawPath
			}
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// The error names the upstream's address and nothing of the request.
			p.log.Warn("the upstream did not answer", "upstream", p.upstream.Host, "error", err)
			http.Error(w, "the upstream did not answer", http.StatusBadGateway)
		},
	}).ServeHTTP(w, r)
}

// refusal says why r is not a request for the upstream, "" when it is. Go's server takes the Host
// of a request in absolute form from its URI, and the target of a CONNECT is its path-less
// authority, so a request that names a target of its own (a proxy request) fails one of the first
// two checks. A Host that is not the proxy's own is also what a rebinding attempt sends, and a
// path that is not clean could climb out of the upstream's path.
func (p *proxy) refusal(r *http.Request) string {
	switch {
	case r.Host != p.listener.Addr().String():
		return "this proxy answers only the address it listens on"
	case !clean(r.URL.Path):
		return "the path is not clean"
	case p.exact && r.URL.Path != "/":
		return "this proxy serves one URL"
	}

	return ""
}

// clean reports whether p has no empty, "." or ".." element, a trailing slash aside.
func clean(p string) bool {
	c := path.Clean(p)

	return p != "" && (p == c || p == c+"/")
}

// transport sends to the upstream directly: the environment's HTTP_PROXY is not read, and a
// redirect is never followed, as ReverseProxy does not follow one.
var transport = &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 10 * time.Minute,
	IdleConnTimeout:       time.Minute,
	ForceAttemptHTTP2:     false,
}
