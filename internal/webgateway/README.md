# Shared web gateway transport entry

This package is the opt-in transport foundation for the planned managed Web
service gateway. It is not connected to Pogo startup, does not publish ports,
and does not provide application routes, service management or access grants.

The existing Pogo browser uses HTTP and native TURN on the same external TCP
port. An HTTP-only reverse proxy would preserve signalling while breaking the
media connection. The entry therefore terminates optional TLS before probing:

- HTTP goes to HTTPListener(), for a future authenticated Host router.
- A valid initial STUN Binding/Allocate frame goes to one separate TCP tunnel
  to the administrator-configured private plaintext Pogo shared listener.
- Unknown/malformed prefixes are closed without connecting to an upstream.

TLS advertises http/1.1 and stun.turn, but byte probing also supports TURN TLS
clients without ALPN. Probed bytes are preserved. Pogo remains responsible for
TURN credentials, exact worker peer permissions, allocations and revocation;
the entry never adds a PROXY header or implements another relay authority.

Install both Entry.ConnContext and Entry.HTTPHandler on the HTTP server.
Together they retain Request.TLS through the buffered listener and keep upgrade
read-ahead bytes attached to a hijacked connection. The hijacked reader is
independent of the returned bufio.Reader so WebSocket libraries can reset it.
This does not restore TLS state after a separate HTTP reverse proxy hop: public
Host/Origin, trusted proxy handling and Office/static-preview compatibility
still require explicit integration.

Defaults are 256 total client connections, 32 concurrent probes/upstream dials,
64 queued HTTP connections, a five-second probe deadline and a three-second
upstream dial deadline. Long-lived connections remain charged to the total
budget. Close cancels pending dials, closes probing/queued/active connections,
and joins all entry workers. Temporary Accept errors retry with capped backoff.

Local checks:

~~~sh
go test -race ./internal/webgateway ./internal/browserrelay
~~~

The browserrelay package also tests a real Pion TURN allocation through this
entry, alongside HTTP/Upgrade traffic, bidirectional private UDP and propagated
Pogo revocation, with clear TCP and TLS without ALPN. These checks do not prove
Docker networking, a complete gateway, real browser media decoding, or managed
website support. Do not enable or migrate the production listener on this basis.
