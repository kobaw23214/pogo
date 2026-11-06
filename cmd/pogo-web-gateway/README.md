# Shared website gateway

This command is opt-in. It does not replace Pogo's current listener by default,
does not manage Docker, and does not create a managed website by itself.

The complete gateway shares one TCP listener between HTTP and native TURN, using
the existing webgateway.Entry. HTTP routes are either the exact configured main
site or an active service-lease URL; TURN is tunneled to the fixed private Pogo
plaintext shared listener. Pogo retains media authority.

Example operator configuration, after a complete isolated deployment has passed:

~~~sh
pogo-web-gateway \
  -listen 0.0.0.0:19770 \
  -main-upstream http://pogo:7777 \
  -turn-address pogo:7777 \
  -main-hosts pogo.example.com:19770 \
  -public-origin https://pogo.example.com:19770 \
  -preview-domain preview.example.com \
  -tls-cert /run/certs/gateway.pem \
  -tls-key /run/certs/gateway-key.pem \
  -admin-socket /run/pogo-gateway/admin/control.sock \
  -state-path /run/pogo-gateway/admin/gateway-state.json
~~~

Only the gateway publishes the host port. Main-site cookies, Authorization,
Origin, streaming, and upgrades are preserved. Preview responses use a separate
sandbox policy; credentials/Set-Cookie and upstream CORS/CSP cannot override it.
An active grant authorizes application HTTP/POST/WS, never management or Pogo APIs.
All dynamic responses are no-store. Unknown hosts fail closed.

The certificate must cover the canonical main name and the explicit wildcard
preview DNS name. No automatic ACME listener on 80/443 is started. Plain HTTP
requires explicit -allow-http for isolated QA. A private -probe-address may map
self-probes to the gateway's internal listener while retaining the public
hostname/SNI and full TLS verification; -probe-ca can supply a QA CA. TLS
verification is never disabled. Loopback application upstreams are rejected
unless -allow-loopback-upstream is explicitly selected for local QA.

For a private network without preview DNS or certificates, explicitly select
`-preview-mode path -allow-http` and use a matching HTTP `-public-origin` and
`-main-hosts`. The URL is then
`http://LAN-IP:19770/_pogo/web/<48hex>/`. This mode does not need a wildcard
preview name. Applications must use relative asset/API/WebSocket paths under
that prefix; root-absolute application URLs are not rewritten. See
[managed website integration](../../docs/managed-web-services.md#preview-entry-modes).

The admin Unix socket requires an owned 0700 parent directory and mode 0600. The
admin volume belongs only to the gateway and Pogo Manager, and must be hidden
from ordinary command/Skill execution. Do not mount it into runtime containers
or browser workers. A separate Web Runner, not this command, controls Docker.

`-state-path` defaults to `gateway-state.json` beside the admin socket. Its
parent must be an owned 0700 directory; the private state file is mode 0600.
Persist it across gateway replacement, on a volume available only to the
trusted control processes. It holds the lease URL signing key and explicit
revocation versions. Losing or replacing the file changes existing lease URLs;
invalid or corrupt state fails startup rather than silently resetting access.
Keep revocation state even after the last published lease deadline: a Manager
may have persisted a renewal while the gateway was unavailable.

NewAdminClient(socket) implements webservice.Gateway. Preflight establishes a
single management session and starts one-second heartbeats. A normal Close
immediately suspends dynamic access and cancels existing application streams;
loss of the management process is detected within the default 15-second lease.
Main HTTP and raw TURN remain independent of that control lease. After a gateway
restart or management replacement, the Manager must Preflight and republish its
verified route snapshot. Old generation/revision events cannot reactivate a
revoked generation.

Current Manager records carry a durable `LeaseID`. The gateway derives their
192-bit URL token from its persisted signing key, owner/session/service lease
identity and revocation version. Repeated opens, renewals and runtime generation
changes retain the same URL. The lease deadline controls expiry; a service that
is stopped, unavailable or not yet republished does not route. Closing a Pogo
preview tab clears the local view and does not revoke the shared lease URL.
Explicit `CloseGrant` rotates it for all viewers and cancels active requests;
a later open returns the rotated address. A new service lease gets a new URL.
This access protocol is separate from operation idempotency in the Manager.

The compatibility path for records without a `LeaseID` still requires
`Record.OperationID`, uses operation-scoped grants and returns `GRANT_EXPIRED`
after their expiry or revocation. Its in-memory operation history has a
fail-closed capacity of 8192. It does not replace the durable lease protocol.

`-browser-route-verified=false` is the default administrator assertion, wired to
RouterOptions.BrowserURLVerified. Open returns browser_url null with the explicit
limitation until the deployment's actual native worker has passed page/assets,
application API and WebSocket checks at the same canonical origin and current
route configuration. Set `-browser-route-verified=true` only after recording
that evidence; a gateway self-probe or listener health is not verification of
the worker. Use one argv token for a Go bool flag (`-browser-route-verified=false`,
not a separate `false` argument). This assertion does not change the public URL,
Host, network membership, authentication, or grant permissions, and does not
prevent the client preview URL while false. Revert it to false and recheck after
changing the worker image, canonical origin or routing configuration.

Meaningful local checks live in internal/webgateway/router_test.go and
internal/webgateway/lease_access_test.go: real HTTPS
self-probes through a wildcard certificate, root/API paths, policy separation,
generation fencing, grant idempotency/quotas, WebSocket revoke, private Unix
management, heartbeat loss and durable URL revocation across gateway outages.
They do not replace Docker network policy, real
browser media decoding, Office regression or production port migration tests.
