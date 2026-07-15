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

The **latest fix per device** in the caller's family, ordered by device name. A device that has
never reported does not appear; a family with no fixes is `[]`.

```json
[
  {"device_id":"…","device_name":"Alice's phone","lat":41.9028,"lon":12.4964,"ts":1752566400,"received_at":1752566402}
]
```

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
