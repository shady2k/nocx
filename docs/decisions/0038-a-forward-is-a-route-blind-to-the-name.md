# ADR-0038: a forward is a route blind to the name, and a cluster is the connection's neighbour

- **Status:** proposed (2026-08-25)
- **Related:** `AD-7`, `AD-8`,
  [ADR-0015](0015-ssh-g-as-the-ssh-config-oracle.md),
  [ADR-0023](0023-a-jump-route-is-its-own-host-key-identity.md),
  `.internal/specs/2026-08-21-api-testing-design.md` §4.5, §6.5, §7.1, §7.3,
  `internal/apisend/routes.go`, `internal/httppolicy/`,
  `internal/tunnel/dynamic.go`, `internal/ssh/pool.go`, `nocx-spixp`
- **Context:** an environment declares a route, and today there are exactly two
  kinds. `direct` goes out of this machine and resolves the name here;
  `connection` opens a direct-tcpip channel on a named SSH profile and the far
  side resolves the name there. The owner asked whether a service reachable
  only through `kubectl port-forward` can be sent to. It can, today, by hand:
  run the forward in a tab and write `http://127.0.0.1:8080` over the `direct`
  route — `httppolicy` permits http:// to loopback, and that is its rule
  working, not a hole in it.

  That workaround is the problem this record exists to refuse. `localhost:8080`
  identifies no destination: the same collection hits staging or production
  depending on which forward happens to be running, the ephemeral port leaks
  into a file meant to travel to a colleague, and `APIRunRoute` — today
  `{Kind, ProfileID, InsecureTLS}` — cannot say afterwards which cluster
  answered. That is the exact accident §6.5 puts the route on the environment
  to prevent, arriving through loopback instead of through a bastion.

  And the in-cluster case that pushes hardest is the one already refused:
  `ErrNameResolvedRemotely` forbids an http:// URL naming a HOST through a
  connection route, which is most in-cluster services, addressed by service
  name over plaintext. The refusal is correct — this side cannot prove what
  the far side will resolve — so the answer must be a route whose destination
  is explicit, not an escape from the rule.

## Decision

**1. A third route kind, `forward`, which is blind to the name.** The
environment names a machine-local forward by opaque id and carries nothing
else — no context, no namespace, no resource, no port. The forward's own
definition fixes the destination, and the hostname in the request URL
contributes exactly two things: the `Host` header and TLS SNI. The URL stays
logical (`https://payments.internal/api`) and is never rewritten to loopback.

**2. A cluster is a machine-local entity beside a connection profile, not a
kind of one, and it may reach its API server through a connection.** A cluster
carries which kubectl context it is and, optionally, which connection the API
server is behind; a forward is taken from a cluster and carries
namespace/resource/port; an environment names the forward. Only the forward id
crosses into the collection file.

**3. kubectl is both the oracle and the carrier.** The real binary is run —
`kubectl --context … --namespace … port-forward --address=127.0.0.1 <resource>
:<remotePort>` — and it is also asked who the cluster is
(`kubectl config view --minify -o json`). We do not read kubeconfig ourselves
and we do not implement the port-forward stream.

## Rationale

The whole of decision 1 is one property, and every consequence below is that
property restated. Sorted by what the transport does with the name:

| route        | the name is                        | resolved      |
| ------------ | ---------------------------------- | ------------- |
| `direct`     | carried, and it is the destination | here          |
| `connection` | carried to the far side            | there         |
| `forward`    | **not carried at all**             | never — fixed |

A name-blind route is new to this system, and it is a lie surface: the URL
says `payments.internal` and the bytes go wherever the forward points, with
nothing in the URL able to contradict it. The defence is therefore not in the
URL. It is that the forward is named, its definition is visible, and the run
record carries the resolved identity. Those are not diagnostics; they are the
only thing separating this feature from the accident in the Context.

**Why not automate the workaround (nocx starts the port-forward, the URL stays
localhost).** It keeps every defect of the manual version and adds a process to
own. The port is still in the file, the destination is still unidentified.

**Why the route does not carry `{context, namespace, resource, port}`.** It
would bind a portable collection to one workstation's kubeconfig names, which
differ between developers, CI and a renamed context — and it would teach
`apisend` what a Deployment is. This is the distinction the SSH route already
makes: an environment names a **profile**, never a resolved host with its
credentials and jump chain. The forward id is the same move.

**Why a cluster is a neighbour and not a subtype of connection.** A connection
in this product is a host you open a tab on: the profile carries
host/port/user/credentials/jump/forwards and its verb is Connect. A cluster
shares none of that except its nature — machine-local, named, credentialed, a
way into a network. Making it a profile would put rows in the connection
manager that cannot be connected to. What it does share is composition, and
that is not a new idea here: `internal/ssh/pool.go` already keeps `jumpRoute`
**inside the pool key**, so a target through a bastion and the same target
direct are different entries with different identities (ADR-0023). A cluster
behind a connection is that figure applied once more.

**Why kubectl rather than client-go.** The port-forward stream is
reimplementable — it is an HTTP upgrade against the API server. Exec credential
plugins are not: EKS, GKE, AKS and OIDC obtain credentials by running another
binary under rules written in kubeconfig, so a library still shells out, just
through our own worse copy. This is ADR-0015's decision arriving a second time:
`ssh -G` is the ssh config oracle because reimplementing config resolution
makes us a second ssh. kubectl is the Kubernetes oracle for the same reason,
and the principle extends to identity — the cluster's server URL and CA
fingerprint are asked of kubectl, not parsed out of kubeconfig, or decision 3
would contradict itself.

Three further things fall out of running it as a process rather than linking a
library, and they are why this is not merely the cheaper option. **We own its
environment**, which is how composition happens: a cluster behind a connection
is served by bringing up the connection's dynamic forward — `internal/tunnel/
dynamic.go` is a complete SOCKS5 `-D` — and spawning kubectl with
`HTTPS_PROXY=socks5://127.0.0.1:<port>`. Because port-forward data streams
through the same API-server connection as its control, one proxy setting covers
both. **A missing binary already has a shape here**: `nativeports.ErrToolMissing`
degrades a provider to unavailable, visible in the product. And **it is
testable without a cluster**: a fake `kubectl` on PATH that prints the readiness
line and proxies to a local test server carries the whole happy path, and the
same fake makes the failure paths — output drift, exit after readiness, a
credential plugin trying to prompt — ordinary tests.

The cost that buys: a loopback listener exists for the forward's lifetime, which
an in-process stream would not have. That is the real trade, and it is priced in
Consequences rather than waved away.

**Why `ErrNameResolvedRemotely` gets no escape.** A generic "trust the far
side's resolver" turns an invariant into a checkbox and permits plaintext to an
address this end cannot see. The supported answers are https by name, an address
literal, `localhost` on the SSH host itself, and — for the case none of those
reach — a declared forward whose destination is explicit. That is what decision 1
is for.

## Consequences

- **`httppolicy` is amended, not satisfied by accident.** A forward route
  answers `LookupIP` with loopback truthfully and meaninglessly: no name was
  carried, so nothing was validated. Loopback is the first transport hop, not
  proof that the destination is local — the bytes end in a cluster. A route must
  therefore declare its **terminal** semantics: the socket actually dialled, the
  logical endpoint the forward is authorised to reach, and whether plaintext is
  protected in transit to that point. Permission must not be smuggled through
  `LookupIP([]127.0.0.1)` with the claim that the existing rule covered it.
- **A redirect that changes authority is refused on a forward route.** The
  transport ignores the new host, so following it would send the new `Host` and
  SNI to the old resource and never reach the named authority — a fiction, not a
  hop. Stripping the credential is not sufficient. `direct` and `connection` are
  unaffected: they carry the name, so their redirects are real.
- **The client cache key is the forward's identity and generation, never the
  URL authority.** Two clusters produce identical logical URLs; sharing a cookie
  jar or a pooled connection between them would be a cross-cluster leak. When a
  forward dies and comes back on another local port, the previous generation's
  idle connections are discarded.
- **Ownership needs no new mechanism.** `AD-7` already says a lease is a
  reference to a pooled resource and never an owned one. The forward belongs to
  the route, not to a run: two environments naming one forward are one route and
  share it, and a cancelled run tears down nothing. Concurrent first sends need
  single-flight on the start, which is not a model of ownership.
- **Lifetime is the security property, so the forward has an idle timeout.** The
  loopback port is reachable by every process on the machine, unauthenticated,
  into production. kubectl's own forward dies at Ctrl-C; ours would otherwise
  live as long as the route, meaning all day. It binds IPv4 loopback only, never
  a broader address, and dies with the app.
- **Four nested lifetimes, so an error names the lowest thing that broke.** SSH
  connection → dynamic forward → kubectl's TLS to the API server → the
  port-forward. `internal/tunnel`'s `watchLoss` exists for this, but "the forward
  was lost" when the bastion dropped sends a person to repair the wrong layer.
  Same principle that made `ErrNoConnection` and `ErrSSHDialTimeout` separate
  sentences.
- **`APIRunRoute` grows.** Forward id, provider, context name, the cluster's
  server identity, namespace, resource kind and name, remote port, the selected
  pod when observable, the generation, and whether the route was re-established
  before this run. Context name alone is not identity — two machines use one
  name for different clusters, and a context can be repointed. The local port is
  diagnostic and is never identity. No kubeconfig contents, tokens or plugin
  environment are persisted.
- **kubectl absent, or a context that no longer exists, is a visible unavailable
  provider — and a send through that route fails closed.** It never becomes a
  direct send, for the reason `routes.go` already refuses to fall back.
- **A request is never silently replayed after a forward dies.** It may be
  non-idempotent and the server may have processed it. The next user send
  re-establishes; retry stays the sender's existing policy.

## Deliberately out of the first cut

Strictness of pod-versus-service identity (a Service legitimately picks another
replica across generations; a Pod route should fail on replacement); named
service ports; IPv6; and any reconnect policy richer than "fail closed, the next
send re-establishes". Adopting a forward the user started by hand is out — the
identity would be scraped from a command line and could not be trusted — though
`nativeports` plus `procwatch` make a fine on-ramp for offering to define one.

## What would move this to accepted

One measurement, and it can shrink the work substantially: **whether the target
services are reachable from the bastion's own network.** A bastion inside the
same VPC as the cluster is the common arrangement, and where it holds, a
ClusterIP service over https by name already works today on the `connection`
route with nothing built. In that case the only real gap is plaintext http by
name, which this record deliberately does not open. kubectl is required where
there is no network path to the service except the API server itself.
