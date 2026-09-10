# API reference

Reporting and reading fixes, the live map, geofencing and push alerts.
Moved out of `README.md`, unchanged.

## Reporting fixes (S2)

A phone reports its location by POSTing a fix with a **per-device bearer token**. The full wire
contract - schema, auth, idempotency, errors - is [`SPEC.md`](SPEC.md); the essentials:

- **`POST /v1/fixes`** - the first-party JSON schema (`lat`, `lon`, `ts` required; `accuracy`,
  `battery`, `speed`, `trigger`, `msg_id` optional). The Android client speaks this, and since **C2**
  it buffers these exact bytes on disk until the server confirms them.
- **`POST /owntracks`** - an **interim, deprecatable** adapter for the stock OwnTracks Android app,
  so a real phone can drive the server before the first-party client exists. It is not a product
  dependency and is a candidate for retirement in S7.

Three properties are load-bearing and each is pinned by a test:

- **Idempotent on `(device_id, ts)`.** A replayed report is a silent no-op (`200`, `deduped:true`),
  never a duplicate row - so a client can retry a report whose response it never saw. The server
  stamps its own `received_at` separately from the device's `ts`.
- **Malformed input is a typed `400`, stored nowhere.** Coordinates are validated **in Go, before
  the SQL** - PostGIS *coerces* a bad coordinate rather than rejecting it, so the check cannot live
  in the database. `/v1/fixes` also rejects unknown fields, so a client typo is loud, not silent.
- **A device writes only its own fixes.** The fix is stored under the *authenticated* device; there
  is no `device_id` in either payload for a caller to forge (§7 authz, by construction).

### Enrolling a device (and a viewer)

Tokens are issued **by the operator, out of band** - there is no admin login yet, so enrollment is a
command against the database rather than an HTTP endpoint (whoever can enroll can write or read a
family's history; the credential for that is shell access to the deployment, not a network call).

```bash
docker compose exec tracker tracker create-family -name "The Schatz family"
#   → prints the family id
docker compose exec tracker tracker enroll -family <family-id> -name "Alice's phone"
#   → prints the DEVICE (write) token ONCE - store it now, it is not recoverable
docker compose exec tracker tracker add-viewer -family <family-id> -email alice@example.com -name "Alice"
#   → prints the VIEWER (read) token ONCE - store it now, it is not recoverable
```

The server stores only the **SHA-256** of each token. The token is 43 characters (base64url of 32
random bytes), never 32 bytes - which is what makes `token_hash`'s length check a real guard against
a raw token being stored where its digest belongs, rather than a coincidence.

> **TLS is S7.** Bearer tokens must travel over TLS in a real deployment; today the server
> terminates plaintext HTTP. Do not expose this to an untrusted network yet.

## Reading fixes (S3)

A **viewer** reads a family's location back over three poll-able routes, each requiring a viewer
bearer token and each scoped to that viewer's own family. The full contract is [`SPEC.md`](SPEC.md);
the essentials:

- **`GET /v1/positions`** - **every device** in the family, in one total order, each carrying exactly
  one **presentation state** (`no-position` | `live` | `recent` | `stale`). A device holding a fix
  carries its latest one plus `presentation` and `last_contact_at`; a device that has never reported
  (or whose fixes have all been purged) is a three-key entry with **no coordinate at all**.
- **`GET /v1/devices/{id}/history`** - one device's fixes, newest first, within an optional
  `from`/`to` window, paginated with `limit`/`offset`.
- **`GET /v1/near?lat=&lon=&m=`** - the family's fixes within `m` **metres** of a point, nearest
  first (the §5.1 `ST_DWithin`-then-`ST_Distance` proximity template, over the GiST index).

Two properties are load-bearing and each is pinned by the authz-matrix tests:

- **Reads and writes use separate credentials.** A **device** token writes its own fixes and only its
  own; a **viewer** token reads its family and only its family. Neither works on the other's routes
  (§7) - a device token on a read route is a `401`, a viewer token on a write route is a `401`.
- **A viewer sees only its own family.** Every route is scoped in its SQL, not by the caller. A read
  that names a device in another family is a `403`, and an empty result is `[]` - **never** a leak of
  another family's data.

> These routes answer a one-shot request. For a **live** map that pushes updates without polling,
> see the SSE stream below (S4).

### Is this phone off, or has it never been set up? (presentation state)

A viewer could not previously tell *"this phone has never checked in"* - a setup problem - from
*"this phone stopped checking in an hour ago"* - a liveness problem, and the only one worth worrying
about. Both rendered as an absence: the read API omitted a device holding no fix, and the map drew no
marker for it. So every device now carries one **server-computed** presentation value on every
surface a viewer reads.

Four properties are load-bearing, and each is pinned by a test:

- **The server computes it; no client re-derives it.** A browser whose clock is hours off must still
  render what the server sent. That is why the value rides the wire instead of a timestamp a page
  compares against `Date.now()`.
- **Age is `now - max(received_at)` across ALL the device's fixes, never the current position's own
  arrival.** They differ exactly when a fix arrived that did not become the current position - an
  offline backlog flush, or a phone whose clock runs fast - and in both cases the device is plainly
  alive while the row on display is old. The aggregate rides the wire as `last_contact_at`, beside
  and distinct from the unchanged `received_at`.
- **A device that has never reported is never `stale`.** The ordered test terminates at "holds no
  fix", so no age comparison is ever made for it, however long ago it was enrolled. The same step is
  what makes a device whose last fix retention purging deleted become `no-position` from that
  instant - on both surfaces at once, so an open map takes the marker down rather than leaving it
  where the server no longer has anything.
- **`0,0` is unrepresentable for an unlocated device, not merely unused.** Its entry is exactly
  `{device_id, device_name, presentation}` - no `lat`, `lon`, `ts`, `received_at` or
  `last_contact_at`, and no zeros. Null Island is a real place off the coast of Ghana and a family
  map must not be able to plot a phone there because a struct field defaulted.

The windows are `TRACKER_LIVE_WINDOW_SECONDS` / `TRACKER_STALE_WINDOW_SECONDS` (below). The full
contract - token spellings, the ordered test, boundary inclusivity, the wire shapes and the stream's
three event types - is [`SPEC.md`](SPEC.md).

## Watching the live map (S4)

A **viewer** watches the family move in real time over one long-lived connection, instead of polling.

- **`GET /v1/stream`** - a **Server-Sent Events** feed of the family's **position updates and
  presentation state**. On connect it describes every device in the family (a `position` event for
  each one holding a fix, a `presentation` event carrying the three-key unlocated entry for each one
  holding none), then pushes changes as they happen. There are exactly **three** event types -
  `position`, `presentation`, `error` - and **only `position` carries an `id`**, so a consumer that
  handles only `position` events sees exactly the event set it saw before presentation state existed,
  with unchanged ids and unchanged resume.
- **`GET /map`** - a minimal, server-served **Leaflet** page that consumes the stream: paste a viewer
  token and watch markers move. It loads Leaflet **from tracker itself** (vendored under `/static`),
  never a third-party CDN - a self-hosted privacy product should not tell someone else's server who is
  watching. (Map *tiles* still come from OpenStreetMap; markers render and move without them.) It
  labels every device with the state word the server sent, lists the ones that are present but not
  located, and reports connection health as a **separate axis** from device state: a dropped stream
  or an `error` event marks every state *unconfirmed* rather than freezing a green map, and the
  server **re-states the whole family** on the first successful read after an outage, so health can
  get better again and not only worse. What the page says about a device is written for a person
  rather than for the wire - a device the server holds no position for reads **"no position
  recorded"**, not a token or a zero - and the words are explained at
  [`/static/map-explained.html`](internal/server/static/map-explained.html), which the page links to
  once per region and the server serves.

  **What a browser actually renders is graded by a browser** (`make verify-ui`, below). The map's
  page is served with a **Content-Security-Policy** admitting its inline style and script by a
  per-response nonce, naming exactly one third-party host (the OpenStreetMap tile imagery) and no
  reporting endpoint, alongside `Referrer-Policy: no-referrer` - the map URL carries a live viewer
  token, so anything that can carry a URL off-origin would leak a read credential.
  [`MAP-VERIFICATION.md`](MAP-VERIFICATION.md) is the earlier manual procedure and the record of what
  was once seen by eye; it is **superseded as evidence** for every clause the browser route now
  grades, and it says so at the top.

Two properties are load-bearing, each pinned by a test:

- **A watcher only ever receives its own family's stream.** The cursor query is family-scoped in its
  SQL, so there is no path that could push another family's position onto a connection - the worst
  failure this product has (§4), now guarded across every open watcher.
- **A dropped connection resumes with no gaps.** The event `id` is the fix's `received_at`, a
  monotonic cursor; on reconnect the browser re-sends it as `Last-Event-ID` and the server replays
  every position that arrived while the watcher was away - and not the one it already had.

The stream authenticates a **viewer** token (a device/write token is a `401`), presented in the
`Authorization` header **or**, because the browser `EventSource` API cannot set headers, as
`?token=<token>` in the URL. See [`SPEC.md`](SPEC.md) for the full contract and the honest trade-offs
(a token in a URL; HTTP/2 vs plaintext HTTP/1.1).

> **The map is a POSITION stream, not a breadcrumb replay.** It shows where everyone is *now*,
> coalescing a burst of fixes to the newest - a live map wants the current position, not a re-run of
> every fix. The full history is still `GET /v1/devices/{id}/history`.

## Geofencing - "Places" and enter/exit events (S5)

A **Place** is a `geofences` polygon a family cares about - home, school, work. tracker evaluates every
incoming fix against a device's family's Places and records an **enter** or **exit** when the device
crosses one, into the append-only `geofence_events` log. The events are **logged, not delivered** yet
(push is S6); a viewer can already read them back.

- **`GET /v1/places`** - the family's Places, each with its ring as GeoJSON (viewer token).
- **`GET /v1/geofence-events`** - the family's crossings, newest first (viewer token). Each is
  `{device_id, place_id, place_name, transition, ts}`, `ts` being the crossing fix's device event-time.

Managing Places is an **operator** act (like device enrollment - there is no admin login yet), a
command against the database rather than an HTTP write:

```bash
tracker add-place -family <family-id> -name "Home" \
  -point 12.0,41.0 -point 13.0,41.0 -point 13.0,42.0 -point 12.0,42.0
#   → prints the Place id (longitude FIRST in every -point; the ring is closed for you)
tracker list-places   -family <family-id>
tracker remove-place  -family <family-id> -id <place-id>
```

Four properties are load-bearing, and each is pinned by a test:

- **Containment is `ST_Covers` - inclusive of the boundary.** A fix exactly on the edge of a Place
  counts as inside (§5.3). Remember a geography polygon's edges are *geodesics*, so a "square" drawn on
  the grid is not quite the region its corners imply (see above).
- **Transitions are debounced against GPS jitter.** A crossing is recorded only once the new state has
  **dwelled** for 90 s, so a fix or two flapping across the boundary - a stationary phone at the edge of
  "home" - never fires a spurious enter/exit.
- **Events derive from `ts` order, not arrival.** The log is a deterministic projection of the fix
  history: a replayed fix produces **no duplicate** event (each crossing is keyed by the fix that caused
  it), and out-of-order fixes are placed by their `ts`, not by when they landed.
- **A missed fix delays but never fabricates.** An enter is not recorded until later fixes prove the
  device stayed inside; if the boundary-crossing fix never arrives, the event is attributed to a later
  fix - it is never invented, and a device tracker only ever *saw* inside a Place gets no phantom enter.

## Push alerts (S6)

A crossing that S5 records is delivered to the family's **watchers** as a push. A watcher (a **viewer**)
registers the push endpoint of its phone, and every enter/exit for that family is sent to it.

- **`POST /v1/push-subscriptions`** - register (or refresh) an endpoint (viewer token). Body:
  `{"provider": "fcm" | "unifiedpush", "token": "…"}`. For **fcm**, `token` is the app's FCM
  registration token; for **unifiedpush**, it is the distributor-issued endpoint URL. Re-registering the
  same endpoint is idempotent - it never duplicates. See [`SPEC.md`](SPEC.md) for the exact shapes.

Two backends, one contract (roadmap §1 - FCM by default, UnifiedPush/ntfy for a degoogled deployment).
A family gets the **same alert** either way: title, body, high priority, a collapse key, and a small
structured `data` map. Which one a deployment uses is a config choice (below); push is **off** until one
is set.

The properties that matter, each pinned by a test:

- **High priority, always.** The FCM message sets `android.priority: "high"` - only a high-priority push
  wakes a closed app on an idle (Doze) device, which is the entire point of an arrival alert.
- **Collapsible "latest state."** The collapse key is per **(device, Place)**, so a rapid enter→exit
  collapses to the newest state rather than buzzing a phone twice for a crossing it can no longer act on.
- **A push NEVER blocks or fails ingestion.** Delivery runs on a bounded, retrying background worker: a
  slow or dead push backend cannot slow a fix's ingest, a send that fails is retried within limits and
  then **logged, not crashed**, and a backlog past the **pending cap** is dropped rather than growing
  without limit. Delivery is best-effort - FCM's own contract - and tracker does not pretend to more.
- **No location in the body.** A notification carries only the family's **own labels** - the device's
  name, the Place's name, the direction - and never a coordinate, accuracy, or raw fix datum (§5.3). The
  family named the device and the Place; a latitude is not something they opted to broadcast.

> **The owner-side real-device check is deferred to the owner.** CI proves the pipeline against a mock
> FCM (request shape, high priority, collapse key, failure handling) and a UnifiedPush parity test; that
> a real push lands on a real handset is a manual check the deployment owner runs with their own Firebase
> project - it confirms, it does not gate.

### The alert reaches a person (ALERT-2)

Until this phase the S6 path ended at the outbound send: the server handed a crossing to a push
backend and nothing in this repo could receive one. Three things closed that.

- **A first-party receive path**, in the Android client, for **FCM**. A crossing arrives as a
  notification naming the device, the Place and the direction. A push that does not carry a complete
  crossing renders **nothing** and is counted as a discard - never a notification with an invented
  part. A deployment configured for UnifiedPush is *told* so by the app rather than left silent; that
  receive path is not built here.
- **The in-app crossing list**, read back from `GET /v1/geofence-events`, which now carries
  `device_name` on each row. It is the **fail-safe for the push, not a duplicate of it**: FCM stores
  four collapsible messages per device, one per collapse key, and tracker's key is per (device,
  Place), so a phone off the network past four distinct pairs has lost the rest permanently. An
  unauthorized read, an unreachable server and a genuinely empty family are three different answers
  and none of them is shown as the others.
- **Honest delivery accounting**, described in [`SPEC.md`](SPEC.md): three outcomes per crossing per
  endpoint (`handed-over`, `beyond-collapse-bound`, `dropped`) and **no fourth that asserts delivery**,
  because a backend accepting a message is a fact about its queue and not about a person. Read them
  with `docker compose logs tracker | grep push.delivery.outcome`. Each record names **which phone**
  it is about, as a digest of the endpoint rather than the routing address itself; `SPEC.md` has the
  one-line query that maps a digest back to a subscription.

Two `/v1` additions serve this and are **additive only**: `configured_provider` on an accepted
registration (so an app can tell "registered" from "registered into a deployment that will never
send"), and an optional `replaces_token` on the registration request (so a rotated FCM registration
token supersedes its predecessor instead of leaving a second deliverable row that would notify the
phone twice). Both are in [`SPEC.md`](SPEC.md).
