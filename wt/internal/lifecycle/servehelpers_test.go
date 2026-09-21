package lifecycle

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveOn serves h on the already-bound listener l. httptest.Server.Close
// closes l itself before returning, so unlike http.Server.Close there is no
// window in which a Close that races the Serve goroutine leaves the listener
// open — the window the stop tests need to be closed, because they assert the
// port is refused right after Close.
func serveOn(t *testing.T, l net.Listener, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	_ = srv.Listener.Close() // discard the listener NewUnstartedServer bound
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// serveFree binds an ephemeral loopback port and serves h on that same
// listener, returning its address. Use it when the address is not needed before
// the server exists; freeAddr followed by serveAt leaves a gap in which another
// process can take the port.
func serveFree(t *testing.T, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serveOn(t, l, h), l.Addr().String()
}