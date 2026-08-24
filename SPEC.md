# tracker ingestion protocol — v1

The wire contract between a reporting device and the tracker server. This document is versioned:
the first-party endpoint lives under `/v1`, and a breaking change to its schema is a `/v2`, never a
silent redefinition of `/v1`.

Two ingestion surfaces exist as of S2:

1. **`POST /v1/fixes`** — the **first-party** protocol. This is the real one; the Android client
   (C-track) speaks it.
2. **`POST /owntracks`** — an **interim, deprecatable adapter** that accepts the stock OwnTracks
   Android app's payload, so a real phone can drive the server before the first-party client exists.
   It is explicitly not a product dependency and is expected to be retired (or demoted to optional
   interop) once the first-party client ships (roadmap §1, S7).

Both store into the same `fixes` table, through the same validation and the same idempotent upsert.

As of **S3**, three **read** routes serve that data back to a family — `GET /v1/positions`,
`GET /v1/devices/{id}/history`, `GET /v1/near` — under a separate **viewer** credential. See
*Reading data (S3)* below.

As of **S4**, a family's positions also stream live over **`GET /v1/stream`** (Server-Sent Events),
and a minimal Leaflet map at **`GET /map`** consumes it. This retires the poll-only map. See *The live
stream (S4)* below.

As of **S5**, tracker evaluates incoming fixes against a family's **"Places"** (geofences) and records
**enter/exit** events. A viewer reads the Places and the event log over **`GET /v1/places`** and
**`GET /v1/geofence-events`**; Places are managed by the operator CLI. See *Geofencing (S5)* below.

As of **S6**, a viewer registers its phone over **`POST /v1/push-subscriptions`** and a crossing is
delivered as a **high-priority FCM HTTP v1** message, or via **UnifiedPush/ntfy** for a degoogled
deployment. Delivery is best-effort and off unless a backend is configured. See *Push alerts (S6)* below.

As of **S0010**, every device a viewer can read carries a server-computed **presentation state** -
one of `no-position`, `live`, `recent`, `stale` - on `GET /v1/positions` and on `GET /v1/stream`,
and `GET /v1/positions` now lists **every device in the family**, including one that has never
reported. See *Presentation state* below; it is normative for both surfaces.

---

## Authentication

Every write requires a **per-device bearer token**, issued at enrollment (see *Enrollment* below).
The server stores only the **SHA-256 hash** of the token; the token itself is shown once, at
enrollment, and is not recoverable.

Present it as either:

- `Authorization: Bearer <token>` — the first-party client and any modern caller; **or**
- HTTP Basic auth with the **token as the password** (any username) — how the stock OwnTracks app
  authenticates.

A missing, malformed, empty, or unknown token is answered with **`401 Unauthorized`** and a
`WWW-Authenticate: Bearer` header. No write is attempted.

**Authorization is by construction:** the fix is stored under the *authenticated* device. Neither
payload carries a `device_id` field, so a device can only ever write its own fixes (§7). A
`device_id` in a first-party body is an unknown field and is rejected (see *Strictness*).

> **Transport.** Tokens are bearer credentials and must travel over TLS in any real deployment. S2
> terminates plaintext HTTP; TLS enforcement is S7. Do not deploy this on an untrusted network yet.

---

## `POST /v1/fixes` — first-party report

`Content-Type: application/json`. One fix per request.

| field      | type    | required | meaning |
|------------|---------|:--------:|---------|
| `lat`      | number  | **yes**  | latitude, degrees, WGS84 (−90..90) |
| `lon`      | number  | **yes**  | longitude, degrees, WGS84 (−180..180) |
| `ts`       | integer | **yes**  | epoch **seconds** of the fix, on the **device** clock |
| `accuracy` | number  | no       | horizontal accuracy, metres (≥ 0) |
| `battery`  | integer | no       | battery level, percent (0..100) |
| `speed`    | number  | no       | ground speed, **metres per second** (≥ 0) |
| `trigger`  | string  | no       | what prompted the report (free-form) |
| `msg_id`   | string  | no       | client-generated correlator, for idempotency/debugging |

Everything but `lat`/`lon`/`ts` is optional and **absence is preserved** — a missing field is stored
as SQL `NULL`, never fabricated as a zero.

### Success

- **`201 Created`** — the fix was stored. Body: `{"status":"stored","deduped":false}`.
- **`200 OK`** — the fix was a replay of one already stored (same `device_id` + `ts`); nothing
  changed. Body: `{"status":"duplicate","deduped":true}`.

### Idempotency

The identity of a fix is **`(device_id, ts)`**. Re-POSTing the same instant is a **no-op**: the
first report for an instant wins, later ones are absorbed and return `200`. `msg_id` is a secondary
correlator for logs and client bookkeeping; it does not change dedup. This lets a client retry a
report whose response it never saw, without ever creating a duplicate.

The server stamps its own **receive-time** (`received_at`) separately from the device's `ts`. `ts`
orders and dedups the trail; `received_at` measures how stale it is (an offline phone reports old
`ts` with a fresh `received_at`).

### The timestamp window

`ts` must fall within **[now − 90 days, now + 24 hours]** of the server clock. Outside that:
**`400`**, stored nowhere. The forward bound rejects a bad device clock; the backward bound is
finite because ingestion provisions the month's storage partition on demand from `ts` (see
*Operational note*), and an unbounded past would let one device create partitions without limit.

---

## `POST /owntracks` — interim OwnTracks adapter *(deprecatable)*

Accepts the OwnTracks HTTP-mode JSON payload. Only `_type:"location"` messages carry a fix; any
other `_type` (transition, waypoint, lwt, …) is **acknowledged and dropped**.

Mapped fields (OwnTracks → tracker): `lat`→`lat`, `lon`→`lon`, `tst`→`ts`, `acc`→`accuracy`,
`batt`→`battery`, `t`→`trigger`, and **`vel` (km/h) → `speed` (m/s)**, dividing by 3.6. All other
OwnTracks fields are tolerated and ignored. A location message is validated and stored through the
exact same path as `/v1/fixes`.

### Response — the OwnTracks contract

The OwnTracks app expects a **`2xx` with a JSON array body**. This adapter always returns **`200`**
with **`[]`** ("nothing to send back") on a stored fix and on an ignored non-location message. A
malformed location (bad JSON, or missing `lat`/`lon`/`tst`) is a **`400`** and stores nothing; the
app will retry it, which is its own concern.

Unlike `/v1/fixes`, the adapter is **lenient** about unknown fields, because a real OwnTracks
payload carries a dozen the server does not model.

---

## Reading data (S3) — the read API

Three read routes serve a family's location back. They are the counterpart to ingestion: writes come
in under a **device** token, reads go out under a **viewer** token, and the two never cross (§7).

### Authentication & authorization

Every read route requires a **viewer** bearer token (`Authorization: Bearer <token>`), issued by the
operator (see *Enrollment*). A viewer belongs to exactly one **family**, and **every route returns
only that family's data**:

- A missing, malformed, or unknown token → **`401`**. A **device** token is not a viewer, so it is
  `401` here too — a write credential cannot read.
- A request that names a resource in **another family** → **`403`**, never that family's data.
- An empty result is an empty JSON array **`[]`**, never `null` and never a fallback to a wider scope.

Timestamps on the wire are **epoch seconds**, the same convention as ingestion's `ts`.

### `GET /v1/positions`

**Every device in the caller's family**, in the total order below, each carrying exactly one
presentation value. A device holding at least one fix is a **located entry** and carries its latest
fix by event-`ts`; a device tracker holds no fix for is an **unlocated entry** and carries three keys
and nothing else. A family with **no devices** is `[]`.

```json
[
  {"device_id":"…","device_name":"Alice's phone","lat":41.9028,"lon":12.4964,"ts":1752566400,"received_at":1752566402,"presentation":"live","last_contact_at":1752566402},
  {"device_id":"…","device_name":"Bobs new phone","presentation":"no-position"}
]
```

> **This changed in S0010.** A device that had never reported used to be *absent* here. It is now
> listed as an unlocated entry, because "this phone has never checked in" is a setup problem and
> "this phone stopped checking in an hour ago" is a liveness problem, and rendering both as an
> absence made them indistinguishable. **A family with devices but no fixes is NOT `[]`** - it is one
> unlocated entry per device.

The order is a **total** order over located and unlocated entries together (never located first):
device name compared case-insensitively under Unicode simple lowercase mapping, then, for equal
folded names, the raw name in UTF-8 byte order, then `device_id` ascending by byte order. It is
computed in the server, not by the database's collation, so two consecutive requests are
byte-identical and two deployments serve one family in one order.

### `GET /v1/devices/{id}/history`

One device's fixes, **newest first**, within an optional time window, paginated. `{id}` must be a
UUID (else `400`); it must name a device in the caller's family (another family → `403`, no such
device → `404`).

| query param | type | default | meaning |
|---|---|---|---|
| `from` | epoch seconds | unbounded | lower bound, **inclusive** |
| `to`   | epoch seconds | unbounded | upper bound, **exclusive** |
| `limit`  | integer | `100` | page size, clamped to `1..1000` |
| `offset` | integer | `0` | rows to skip |

The window is half-open `[from, to)` so adjacent windows tile without double-counting. A
present-but-unparseable param is a `400`, never a silently ignored default.

```json
[
  {"ts":1752566400,"lat":41.9028,"lon":12.4964,"accuracy":5.0,"battery":88,"speed":1.4,"trigger":"u","received_at":1752566402}
]
```

Optional metrics (`accuracy`, `battery`, `speed`, `trigger`) are **omitted when absent**, never
fabricated as zero.

### `GET /v1/near?lat=&lon=&m=`

The caller's family's fixes within `m` metres of (`lat`, `lon`), **nearest first**, with the distance
that ranked each. Proximity is computed on `geography`, so `m` is **metres**. `lat`, `lon` and `m`
are required; a missing, non-numeric, out-of-range, or negative value is a `400`.

```json
[
  {"device_id":"…","device_name":"Alice's phone","lat":41.9028,"lon":12.4964,"ts":1752566400,"distance_m":12.4}
]
```

---

## Presentation state

Every device a viewer can read carries exactly one **presentation value**, computed by the **server**
at the moment the response or event is produced and transmitted on the wire. **No client re-derives
it.** A browser whose clock disagrees with the server's by hours must still render what the server
sent, which is the whole reason this is a server-side computation and not a timestamp comparison in
a page.

### The four values

`presentation` is a JSON **string** key on every device entry and every presentation-carrying event.
Its value is exactly one of these four lowercase ASCII tokens. Note the hyphen; no other spelling,
casing or synonym is ever emitted, and none is ever accepted as equivalent.

| token | meaning |
|---|---|
| `no-position` | tracker holds **no fix at all** for this device. It has never reported, or every fix it ever sent has since been purged. |
| `live` | the device reached the server within the live window. |
| `recent` | past the live window, still inside the staleness window. |
| `stale` | past the staleness window. Something is wrong with this phone. |

### Last contact, and the fact it is NOT

Two receive-times ride the wire and they are **different facts**:

| key | what it is | unit |
|---|---|---|
| `received_at` | the **current position row's own** arrival - the receive-time of the fix being displayed. Unchanged from S3 in name, type, unit and meaning. | epoch **seconds** |
| `last_contact_at` | **`max(received_at)` over ALL of the device's fixes** - the largest server receive-time, an aggregate over the whole set. | epoch **seconds** |

The SSE `id:` line carries the same kind of receive instant at **microsecond** resolution; that is
unchanged too. Every cross-field comparison between these is an equality of **instant**, never of
integer: convert to one unit before comparing.

**Age is `now - last_contact_at`, never `now - received_at`.** They differ exactly when a fix arrived
that did not become the current position, and in both real cases the device is plainly alive while
the row on display is old:

- a phone flushing an **offline backlog** reports old `ts` values with a fresh arrival, so the newest
  arrival is not the newest `ts`;
- a phone with a **fast clock** reports a `ts` in the future, which pins the current position while
  every later fix keeps arriving.

Measuring age off the displayed row's `received_at` would call both of those `stale`. Age is a fact
about the **device**, not about the row on display - which is why it rides the wire under its own
key. `last_contact_at` is **present on every located entry** and **omitted entirely** from an
unlocated one.

### The ordered test, and the precedence ruling

The four values are **mutually exclusive** and **total**: every device in the caller's family carries
exactly one, never two and never none. They are assigned by this ordered test, **first match wins**:

1. **`no-position`** - tracker holds no fix for this device.
2. **`live`** - `age <= W_live`.
3. **`recent`** - `W_live < age <= W_stale`.
4. **`stale`** - `age > W_stale`.

**A device that has never reported is never `stale`, however long ago it was enrolled.** Step 1
terminates the test, so no age comparison is ever performed for a device with no fix; symmetrically,
a device holding a fix is never `no-position`.

Step 1 reads the fix set **as it stands at the evaluation instant**, so a device whose last fix is
deleted - **retention purging**, which keeps running - becomes `no-position` from that instant
onward. It does not age into `stale`, and its coordinates stop being served on both surfaces at
once.

**Resolution and boundaries.** Age is a whole number of seconds: the *difference* between the two
instants, truncated toward zero and floored at 0. A device last heard from 120.4 seconds ago has age
120. A **negative** age (the server clock behind the device's last contact - clock skew) is **zero**,
so such a device is `live` and no negative age ever reaches the wire. Boundaries are **half-open
toward freshness**: age exactly `W_live` is `live`, age exactly `W_stale` is `recent`.

Under the defaults, a device whose last contact precedes the evaluation instant by 0, 120, 121, 900
and 901 seconds is `live`, `live`, `recent`, `recent` and `stale` respectively.

### The unlocated entry

An unlocated entry is **exactly three keys and nothing else**:

```json
{"device_id":"…","device_name":"Bobs new phone","presentation":"no-position"}
```

No `lat`, no `lon`, no `ts`, no `received_at`, no `last_contact_at` - and **above all no zeros**.
`0,0` is Null Island, a real place off the coast of Ghana; a `no-position` device must be
*unplottable*, not merely un-plotted. `GET /v1/positions` and the stream emit the **identical**
object for the same device in the same evaluation window.

A **located** entry keeps every key it carried before S0010 - `device_id`, `device_name`, `lat`,
`lon`, `ts`, `received_at` - with unchanged name, type, unit and meaning, and **adds** `presentation`
and `last_contact_at`. The change is purely additive: a decoder that requires those six keys and
tolerates unknown ones still decodes it. A decoder that *rejects* unknown fields will fail, and the
repair is the decoder - tracker does not withhold a key to accommodate one.

### Configuring the windows

Two environment variables, following the same env-only, `TRACKER_`-prefixed,
refuse-to-start-on-invalid convention as the rest of the server's configuration:

| variable | default | meaning |
|---|---|---|
| `TRACKER_LIVE_WINDOW_SECONDS` | `120` | `W_live`: at or under this age, a device is `live`. |
| `TRACKER_STALE_WINDOW_SECONDS` | `900` | `W_stale`: past this age, a device is `stale`. |

**The value grammar is a base-10 whole number of SECONDS** - no unit suffix, no fractional part -
after surrounding whitespace is trimmed, within the representable range **1 to 2^63-1 seconds
inclusive**. Every number here, in the start-up log and in every error is seconds.

- **Emptiness is measured after that trim.** A variable that is unset, set to the empty string, or
  set to whitespace only is **not configured**, and not configured means **the default** - never an
  invalid value and never a refused start. A rendered compose file with an unset shell default hands
  the process an empty string, and refusing to boot over a value nobody typed would be the wrong
  answer to it.
- **A present but non-conforming value refuses the start**, naming the offending variable and the
  constraint. `2m`, `120s` (a unit suffix is not this grammar - there is no duration-string form),
  `1.5` (not whole), `abc` (not a number), `0` and `-5` (not positive) and `99999999999999999999`
  (outside the representable range, so unparseable rather than silently truncated) all refuse.
  `120`, `  120  `, the empty string and a whitespace-only value all start.
- **Each variable defaults independently**, and the ordering rule is checked on the resulting
  **effective** pair. Setting only `TRACKER_STALE_WINDOW_SECONDS=60` yields the effective pair
  (120, 60), and that **refuses to start**: with `W_live >= W_stale` no device could ever be
  `recent`, so a whole documented state would be silently unreachable. The refusal reports **both**
  effective values in seconds.
- **A successful start logs both effective values**, in seconds and under their variable names, so
  an operator can tell from a running container's log which windows it applies - including when it
  applies the defaults.

---

## The live stream (S4, extended by S0010) — `GET /v1/stream`

A **Server-Sent Events** feed of the caller's family's **position updates and presentation state**: a
long-lived connection that pushes each device's current position as it changes, and each device's
presentation value as it changes, so a map need not poll and need not derive liveness from its own
clock. It retires the poll-only map (S3 remains for one-shot reads).

If the server can no longer read the data behind an open stream it either **closes the connection**
or emits an explicit **`error` event** - it never keeps emitting state derived from data it cannot
read, and never holds the last known states open while presenting them as current.

### Transport & authentication

`Content-Type: text/event-stream`. The connection stays open; the server writes events as they occur
and a `: keep-alive` comment periodically so idle connections and dead clients are detected.

Authentication is a **viewer** token, exactly as the S3 read routes require — a **device** (write)
token is a `401` here too. It may be presented **either** way:

- `Authorization: Bearer <token>` — a programmatic caller; **or**
- **`?token=<token>`** in the query string — the browser `EventSource` API, which **cannot** set an
  `Authorization` header. This is the only reason the query form exists.

> **A viewer token in a URL is a real trade-off.** URLs land in logs and referrers, so a query-string
> credential is weaker than a header one. It is accepted here because `EventSource` leaves no
> alternative for the interim web map; it must travel over **TLS** (S7), and the in-app client map
> (C5), which *can* set headers, supersedes it. Prefer the header wherever the client allows it.

Every route stays **family-scoped**: a watcher only ever receives its own family's positions.

### Event format — there are exactly three event types

| `event:` | when it is sent | carries `id:` | `data:` |
|---|---|:---:|---|
| `position` | a device's **current position changes**, including that device's first ever fix | **yes** | a **located entry** |
| `presentation` | a device's **presentation value changes with no change to its current position**; every `no-position` device in a fresh snapshot; and every device the resume sweep names | **no** | a presentation update, or an **unlocated entry** |
| `error` | the server **can no longer read** the data behind this stream | no | `{"error":"…","message":"…"}` |

There is no fourth type, and none of the three marks a snapshot complete.

```
id: 1752566402000000
event: position
data: {"device_id":"…","device_name":"Alice's phone","lat":41.9028,"lon":12.4964,"ts":1752566400,"received_at":1752566402,"presentation":"live","last_contact_at":1752566402}

event: presentation
data: {"device_id":"…","device_name":"Alice's phone","presentation":"recent","last_contact_at":1752566402}

event: presentation
data: {"device_id":"…","device_name":"Bobs new phone","presentation":"no-position"}

event: error
data: {"error":"unavailable","message":"tracker cannot currently read this family's data; the states shown are no longer confirmed"}

```

- **`id`** is the fix's **`received_at` in microseconds** - a monotonic, resumable cursor (see below).
  **Only a `position` event carries one.**
- A `position` event's `data` is the same JSON shape as one located `GET /v1/positions` entry, and its
  `presentation` is evaluated at **delivery** time.
- A `presentation` event's `data` is one of two shapes. For a device that **still holds a position**
  it is a four-key **partial update** - `device_id`, `device_name`, `presentation`,
  `last_contact_at` - deliberately carrying **no coordinates**, because the device has not moved and
  this must never be mistaken for a new fix. For a device tracker now holds **no fix** for it is the
  three-key **unlocated entry**, identical to the one `GET /v1/positions` serves.

#### A `presentation` event deliberately does not advance `Last-Event-ID`

It carries **no `id:` line at all**, so it cannot move the cursor a client echoes back and cannot
corrupt a resume. That is a deliberate compatibility decision with a consequence worth stating
plainly:

> **A consumer that handles only `position` events sees EXACTLY the event set it saw before S0010**,
> with unchanged ids and unchanged resume behaviour. It therefore learns of a **time-driven**
> transition (a device ageing from `live` to `recent` with no new fix) only at that device's next
> `position` event or on a fresh connection. That is a documented degradation, not a defect: the
> alternative was moving the cursor on an event that corresponds to no stored row, which would
> break resume for every existing consumer.

#### What causes a `presentation` event

A device's presentation value changes without its position changing, for any of these reasons, and
each is announced to **every open stream for that family**, carrying the value evaluated at
**delivery** time and not at the crossing:

- **time passed** and it crossed an age boundary (`live` to `recent`, `recent` to `stale`);
- **a fix arrived that refreshed last contact without becoming the current position** - an offline
  backlog gap-fill - including the freshening direction, `stale` back to `live`;
- **retention purging deleted fixes**, including the transition to `no-position` when the last
  remaining fix is purged. That event carries the three-key unlocated entry, so a map takes the
  marker down rather than leaving it at coordinates the server no longer holds.

The announcement is bounded: it arrives no later than **B** seconds after the change, where
`B = max(1, floor(min(W_live, W_stale - W_live) / 2))` over the effective windows - 60 seconds under
the defaults, 2 under the legal pair (5, 10). The `min` is what keeps the bound honest under a narrow
`recent` band, which a coarser sweep could otherwise step straight over.

> **How the bound is met.** An open stream re-evaluates on a timer, so a change lands at an arbitrary
> point *inside* a period and waits out the remainder of it. The period is therefore never more than
> **half of B**: one second under every wide pair (the defaults give `B = 60`), and twice a second
> under the two tightest legal pairs, `(1, 2)` and `(2, 3)`, where `B` is 1. A period equal to `B`
> would already be over the bound by whatever the query cost is.

#### When the server cannot read: `error`, and getting better again

If the datastore becomes unreadable while a stream is open, the watcher is told **once**, explicitly,
with an `error` event - not one per poll - and the connection is deliberately held **open** with the
cursor where it is, so a transient blip resumes exactly where it stopped rather than tearing down
every map in the household. **Nothing derived from data the server can no longer read follows it**:
no `presentation` and no `position` event goes out while the outage lasts. Silently holding the last
known states open while presenting them as current is the failure this prevents.

**When a poll succeeds again, the whole family is re-stated**: one `presentation` event per device,
carrying its delivery-time value, whether or not that value moved during the outage. This is the
"all clear" a client needs, and it needs one: because the `error` branch never drops the connection,
nothing is ever *re-established*, so a family that happened not to change during the outage would
otherwise produce no events at all and leave a map marked "no longer confirmed" against a healthy
server. Nothing new goes on the wire for it - these are ordinary `presentation` events with **no
`id:`**, so a `position`-only consumer sees nothing and no cursor moves. **The one residual, stated
rather than hidden:** a family with **no devices at all** has nothing to re-state, so a client
watching an empty family learns of the recovery only when it reconnects.

### Snapshot, then live

A **fresh** connection (no `Last-Event-ID`) first receives a snapshot describing **every device in
the family**, each carrying the value computed at delivery time:

- one **`position`** event per device holding a fix, and
- one **`presentation`** event carrying the three-key unlocated entry per device holding none.

Then live updates as they arrive. Nothing marks the snapshot complete; a client that needs to know
the device set is complete reads `GET /v1/positions`, whose empty array is how "no devices in this
family" is distinguishable from "still connecting".

### Resume — `Last-Event-ID`, no gaps, and the sweep

Because `id` is `received_at`-in-microseconds, it is a **monotonic cursor** and an **instant**, not
an opaque handle. On a dropped connection the browser reconnects automatically and re-sends the last
id it saw as **`Last-Event-ID`**; the server then delivers **every position that arrived strictly
after it** - nothing that happened while disconnected is skipped, and the boundary row is not
re-delivered. That guarantee is unchanged.

A resume then does one further thing, because a client disconnected for an hour must be told what is
true **now** rather than only what moved:

**The sweep.** The server enumerates the family's **device set** - not the replay buffer - and for
each device compares its **delivery-time value** against its **cursor-instant value**, emitting a
`presentation` event where they differ.

- The **cursor-instant value** of a device is what the same ordered test yields with the evaluation
  instant set to **the instant the cursor encodes**, over only that device's fixes whose receive time
  is **at or before** that instant.
- A device holding **no fix at or before that instant** - enrolled during the outage, or reporting
  for the first time during it - **has no cursor-instant value**. That absence is not `no-position`
  and never compares equal to anything, so such a device is **always** told about and never silently
  omitted. Its event carries whatever it is now, which for a device that still has not reported is
  the three-key unlocated entry.
- A device whose **position was just replayed** gets no `presentation` event: the replayed `position`
  event already carries its delivery-time value, so a second event would say the same thing twice.
- Both sides are recomputed from the **stored fixes** on every resume. Nothing about any client is
  remembered, so **two resumes on the same cursor emit the same set** and re-delivery is idempotent.

**Silence on a resumed stream is meaningful.** If nothing was replayed and no device qualifies, the
server sends **no event** and holds the connection open: that means "every device was evaluated and
nothing changed", and the client's already-rendered states remain current. It does not mean "nothing
was checked".

The sweep uses `presentation` events, which carry no `id:`, so it is invisible to a `position`-only
consumer and cannot disturb the cursor.

A **garbled or absent** `Last-Event-ID` - one that is not a number, so it cannot be anchored to an
instant - is **not a resume**. It falls back to the full fresh snapshot, failing **safe** toward
showing more, never toward a silent skip. Note that the cursor is anchored by its **value**: the
sweep never looks the row up, so an hour-old cursor still works after retention purging has deleted
exactly the aged row it names.

**A device enrolled after a connection opened**, and not yet reporting, is **not** announced on that
already-open connection - the set of devices announced as unlocated is fixed at snapshot time. It
appears as `no-position` on the next fresh connection's snapshot, on the next resume sweep (which
holds no cursor-instant value for it), and on `GET /v1/positions` immediately. This is bounded by the
position guarantee above: **a fix from any family device, enrolled before or after the snapshot, is
always delivered as a `position` event** on that open connection.

### Position-stream semantics (not a breadcrumb replay)

The stream pushes a device's **current** position — the latest fix by event-`ts` — keyed on when the
server received it. A burst of fixes between polls **coalesces** to the newest: a live map wants where
everyone is now, not a re-run of every fix. A backlogged older-`ts` fix arriving later does **not**
re-emit an unchanged current position.

> **HTTP/2.** SSE benefits from HTTP/2, which multiplexes many streams over one connection and lifts
> the browser's ~6-connections-per-origin cap. tracker serves the stream over whatever HTTP version
> terminates in front of it; it upgrades to HTTP/2 automatically once TLS is in place (**S7**). Until
> then, over plaintext HTTP/1.1, more than ~6 simultaneous browser tabs to the same origin can queue.

---

## Geofencing (S5) — Places and enter/exit events

A **Place** is a family-scoped polygon (`geofences`). tracker evaluates every incoming fix against the
device's family's Places and appends an **enter** or **exit** to the append-only `geofence_events` log
when the device crosses one. The events are **recorded, not delivered** — push (FCM/UnifiedPush) is S6.

### Evaluation semantics

- **Containment is `ST_Covers`, inclusive of the boundary.** A fix exactly on a Place's edge counts as
  inside. (On `geography` a polygon's edges are geodesics, so a grid-drawn "square" is not exactly the
  region its corners imply — see the README.)
- **Debounced against GPS jitter.** A change of containment is recorded only once it has **dwelled** for
  **90 seconds**; a fix or two flapping across the boundary is not a crossing and fires nothing.
- **Derived from `ts` order, not arrival.** The log is a deterministic projection of the stored fixes.
  A **replayed** fix produces **no duplicate** event — each transition is identified by the crossing
  fix's `(device, place, ts)`. Out-of-order fixes are placed by their `ts`.
- **A missed fix delays but never fabricates.** An enter is confirmed only once later fixes prove the
  device stayed inside, so a missing boundary-crossing fix delays the event (it is attributed to a
  later fix), never invents one. A device only ever seen inside a Place gets no phantom enter. The
  evaluator advances a (device, place)'s state forward in `ts`; a fix older than that pair's latest
  recorded transition is history, not a spliced-in past event.

### `GET /v1/places`

The caller's family's Places, ordered by name. **Viewer** token; family-scoped; empty is `[]`.

```json
[
  {"id":"…","name":"Home","area":{"type":"Polygon","coordinates":[[[12,41],[13,41],[13,42],[12,42],[12,41]]]}}
]
```

`area` is a GeoJSON geometry (longitude-first coordinates, as GeoJSON requires).

### `GET /v1/geofence-events`

The caller's family's crossings, **newest first**. **Viewer** token; family-scoped; empty is `[]`.
`?limit=` sets the page size (default 100, clamped to 1..1000). A present-but-unparseable `limit` is a
`400`.

```json
[
  {"device_id":"…","place_id":"…","place_name":"Home","transition":"enter","ts":1752566400}
]
```

`transition` is `"enter"` or `"exit"`; `ts` is the crossing fix's device event-time, in epoch seconds.

### Managing Places (operator CLI)

Creating and editing Places is a privileged act, issued by the operator out of band — the same reason
enrollment is (there is no admin login yet), so a **viewer** token reads Places but does not write them.

```bash
tracker add-place -family <family-id> -name "Home" \
  -point 12.0,41.0 -point 13.0,41.0 -point 13.0,42.0 -point 12.0,42.0
#   → place created; prints the Place id. Longitude FIRST in every -point; ≥ 3 points; ring auto-closed.
tracker list-places  -family <family-id>
tracker remove-place -family <family-id> -id <place-id>
```

A self-intersecting ring (a "bowtie") or an out-of-range vertex is refused — such a ring encloses no
area and would be a Place that never fires.

---

## Push alerts (S6) — `POST /v1/push-subscriptions`

A **viewer** registers the push endpoint of its phone so a family's geofence crossings are delivered to
it. Registration is a **viewer** write — the viewer it registers under is the authenticated caller, never
a field in the body — so a viewer can only register an endpoint under itself.

**Request** (viewer token; strict decode — unknown fields and trailing data are rejected):

```json
{"provider": "fcm", "token": "the-endpoint-address"}
```

- **`provider`** — `"fcm"` or `"unifiedpush"` (required). Any other value is a `400`.
- **`token`** — the routing address (required, non-empty). Its meaning depends on `provider`:
  - **`fcm`** — the app's FCM **registration token** (the opaque device id from Firebase).
  - **`unifiedpush`** — the distributor-issued **endpoint URL** the server POSTs to.

**Success** — `201` with the stored subscription's id and provider:

```json
{"id": "…", "provider": "fcm"}
```

Registration is **idempotent** on `(provider, token)`: the same phone re-registering (a fresh launch, a
rotated FCM token re-sent) refreshes the existing row and moves it to the presenting viewer — it never
creates a duplicate that would push the same phone twice per crossing.

### What a push contains

On an enter/exit, every registered endpoint in the crossing device's family receives one message.
Whatever the backend, the alert carries the same content:

- **title** — the device's name (e.g. `"Alice's phone"`).
- **body** — the crossing in the family's own words (e.g. `"Alice's phone arrived at School"`).
- **high priority** — FCM sends `android.priority: "high"` (the only priority that wakes a closed app on
  an idle device); UnifiedPush carries the same intent.
- **collapse key** — per **(device, Place)**, so a rapid enter→exit collapses to the latest state.
- **data** — a small structured map: `type=geofence`, `device_id`, `place_id`, `place_name`,
  `transition`, `ts`. **No coordinate, accuracy, or raw fix datum is ever included** — a push carries
  only the family's own labels (§5.3).

### Delivery guarantees (there are none, by design)

Delivery is **best-effort** — FCM does not guarantee it and caps pending messages, so a missed alert is
possible; the freshest state arrives on the device's next crossing. A push **never blocks or fails
ingestion**: it runs on a bounded, retrying background worker, a persistent failure is logged (not
crashed), and a backlog past the pending cap is dropped. Push is **disabled** unless the deployment
configures a backend (`TRACKER_PUSH_PROVIDER`); see the README's *Configuration*.

---

## Strictness & errors

`/v1/fixes` is **strict**: unknown fields, trailing data after the JSON object, and bodies over
64 KiB are rejected. A payload the server cannot fully account for is refused rather than partially
believed — this is the fail-safe stance, so a client typo (`"latitude"` for `"lat"`) is a loud
`400`, not a silently dropped field.

Coordinates are validated **in Go, before any SQL** (`store.ValidateLonLat`): PostGIS silently
*coerces* an out-of-range coordinate into range and stores the wrong place, so the check cannot live
in the database. A swapped-axis fix whose latitude lands outside ±90 is a `400`, stored nowhere.

Error bodies are `{"error":"<code>","message":"<human sentence>"}`, with `<code>` one of
`unauthorized`, `malformed`, `invalid_fix`, `internal`.

---

## Enrollment

Tokens are issued **out of band, by the operator**, not over HTTP — there is no admin authentication
yet, so an enrollment endpoint would be either unauthenticated or unbuildable here. The server binary
carries the commands:

```bash
tracker create-family -name "The Schatz family"
#   → family created; prints the family id

tracker enroll -family <family-id> -name "Alice's phone"
#   → device enrolled; prints the DEVICE (write) bearer token ONCE (store it now — not recoverable)

tracker add-viewer -family <family-id> -email alice@example.com -name "Alice"
#   → viewer added; prints the VIEWER (read) bearer token ONCE (store it now — not recoverable)
```

A **device** token authenticates the write routes; a **viewer** token authenticates the read routes.
They live in separate tables and are not interchangeable (§7). All three commands read the same
`TRACKER_DATABASE_URL` the server does, and every token is printed to stdout only, never logged.

---

## Deprecation note

The `/owntracks` adapter is **interim scaffolding**. It exists so a battery-tuned, shipping app can
validate the ingestion → storage → (later) map → alert pipeline against a real phone now. It is a
candidate for retirement once the first-party client is proven (roadmap S7), and it must never
become something the product depends on.
