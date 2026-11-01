# Browser media relay

`New(boundListener, Config)` shares an existing TCP listener between HTTP and
TURN/TCP. It takes listener ownership on success. It does not start a public UDP
listener, decode video, change codecs, or create a second browser implementation.
The relay's UDP allocations communicate only with the configured W2 worker.

Serve HTTP on `HTTPListener()` with the normal `http.Server`. If the supplied
listener is TLS wrapped, also use `ConnContext` and `HTTPHandler` to retain
`Request.TLS`. TLS termination must occur on this shared listener; an HTTP-only
reverse proxy cannot forward raw TURN/TCP frames.

After verifying the owning live WebSocket, page identity, generation, and ticket,
call `IssuePeers(viewHash + ":" + mediaEpoch, exactWorkerPeers)`. The client receives
an opaque random username/password, not a reusable Pogo token. Initial allocation
authentication expires after one minute. Only successful MESSAGE-INTEGRITY
verification pins it to one accepted TCP connection. That connection may refresh
its allocation for the lifetime of the active view, capped at six hours from the
view's first issuance. A media epoch cannot reset that deadline.

The caller must serialize `IssuePeers` with its live view's close transition, and
call `RevokeScope(viewHash)` after marking the view closed. Revocation closes both
the TCP connection and internal UDP allocation immediately. Scope prefixes match
only at a colon delimiter, so `view` cannot revoke `view-other`. View start records
remain until explicit revocation and share the bounded credential registry.

`UpdateAllowedPeerIPs` accepts a freshly resolved exact IPv4 address list for the
configured worker. An unchanged address set preserves current allocations. A
removed address fences affected credentials and closes their sockets. Permission
checks and every UDP datagram enforce the issued exact IP/port pairs; TURN's
IP-only permission abstraction cannot authorize another port. Public users cannot
nominate peers. Production configuration rejects loopback worker addresses.

HTTP bytes, keepalive requests, and buffered WebSocket upgrade payloads remain
unchanged. Initial probes, connection counts, queue sizes, credentials, frame
sizes, and I/O waits are bounded. `Close` releases owned listeners, TCP and UDP
sockets and joins the package's accept/probe/expiry workers. `Drain(ctx)` stops new
allocations, waits for current allocations until the context deadline, then
forces close. Coordinate it with HTTP server shutdown.

Run isolated package checks with `go test -race -count=1 ./internal/browserrelay`.
Tests use private loopback fixtures and real Pion allocations; the loopback
exception is unexported and unavailable to production callers. These checks do
not replace browser decoding, input, image quality, or deployment acceptance.
