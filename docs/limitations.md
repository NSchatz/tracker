# Known limitations

Moved out of `README.md`, unchanged.

## Known limitations

Things that are true today and are not hidden:

- **The Android client's offline queue is bounded, and its scheduling is not gate-proved.** As of C2 a
  fix is written to disk before delivery is attempted and stays there until the server has it, so a
  transient failure can no longer lose one. Three honest edges remain: the queue holds **5,000 fixes**
  (~3.5 days at the current cadence) and evicts the **oldest** past that, counted as `dropped`;
  WorkManager's retry backoff is indefinite but tops out around **5 hours**, so a flush after a very
  long outage can lag the reconnection (latency, not loss); and delivery is at-least-once on the wire
  - the server's `(device_id, ts)` dedup is what makes it exactly-once in the database - so the app's
  `delivered` counter can over-count replays. The crash-atomicity of the *writer* (temp → `fsync` →
  rename) is review-only; the reader half of it is tested.
- **Most of the client's behaviour is not provable in CI, and is not claimed to be.** Runtime permission
  grants, a live GPS stream, foreground-service survival with the screen off, whether WorkManager runs
  the flush when connectivity returns, and OEM battery-killer behaviour all need a real device. The gate
  covers the pure half - payload construction, coordinate and timestamp validation, the permission state
  machine, the durable queue against a real filesystem, and the flush loop against a real local server
  - and the rest is an explicit **operator device check** documented in
  [`android/README.md`](android/README.md). No test in this repo mocks the platform and then reports the
  mock's answer as evidence.
- **BOTH of the client's credentials are stored in plaintext** `SharedPreferences` until SECRET-3
  moves them behind an Android Keystore key: the device (write) token, and - since ALERT-2 - the
  **viewer (read) token** the alert surface needs to read the family's crossings and register the
  phone's push endpoint. `allowBackup="false"` limits the blast radius in the meantime. The viewer
  token is the wider of the two to lose (it reads the family's whole crossing history) and the app
  bounds what it does with it: exactly two routes, neither of which returns a coordinate, and it is
  never logged, rendered or put in a URL.
- **The client needs Google Play services.** `FusedLocationProviderClient` has no AOSP equivalent and
  there is no `LocationManager` fallback, so a fully degoogled phone cannot run it today.
- **Push delivery is best-effort, and off by default.** A crossing is delivered to registered phones
  (S6) via FCM or UnifiedPush, but delivery is **not guaranteed** - FCM's own contract - and a missed
  alert is possible; the freshest state arrives on the device's next crossing. Push is disabled unless a
  backend is configured, and freshness is still bounded by the last received fix: an offline phone's
  crossings - and their pushes - fire when its buffered fixes arrive. Whether a real push reaches a real
  handset is the owner's manual real-device check (CI proves the pipeline against a mock).

  ALERT-2 **quantifies** that rather than leaving it as a word. FCM stores **four** collapsible
  messages per device, one per collapse key, and tracker's key is per (device, Place) - so a phone off
  the network past four distinct device-and-Place pairs loses the rest. The server records that
  surplus as `beyond-collapse-bound`, records **nothing** as delivered, and the in-app crossing list
  read back from `GET /v1/geofence-events` is what makes a lost alert still findable. The
  pending-key accounting is process state: a restart forgets it and then reports `handed-over` where
  it would have said `beyond-collapse-bound`, which loses toward knowing less and never toward a
  delivery claim.
- **The client receives over FCM only, and needs a Firebase project the repo does not carry.** A
  UnifiedPush deployment will send and this client cannot receive, which the app *says* rather than
  swallowing. And `google-services.json` is deployment-specific, so it is not committed and the
  Google Services Gradle plugin is not applied: the gate builds the app with no Firebase project at
  all, and an app built that way reports it has no usable push configuration instead of failing to
  start. A deployment that wants push adds both in its own build.
- **A phone that loses its own record of its previous routing address leaves a stale endpoint.** A
  rotated FCM registration token is a new address, so the app names the address it replaces and the
  server removes exactly that one, under the same viewer. A reinstall or a restore onto another
  handset cannot name it, and nothing acts on a backend's "unregistered" report yet, so the row
  survives - wasting a send to a dead address, not double-notifying a live phone.
- **Enter/exit is debounced, so it is deliberately not instant.** A crossing must dwell 90 s before it
  is recorded - the price of not alerting on GPS jitter. And the evaluator advances a (device, Place)'s
  state *forward* in `ts`: a fix arriving out of order and older than that pair's latest recorded
  transition is kept as history but does not splice a past event into the log. In practice a crossing
  is a moving, frequently-reporting phone, and reordered fixes ahead of the last transition are placed
  exactly by `ts`.
- **The live map is a position stream, coalesced.** `GET /v1/stream` pushes each device's *current*
  position, collapsing a burst of fixes between polls to the newest. That is deliberate - a live map
  wants where everyone is now - but it means the stream is not a lossless replay of every fix; the
  full trail is `GET /v1/devices/{id}/history`. The stream polls the database on a short interval
  rather than being pushed from ingestion, which keeps it stateless across replicas at the cost of up
  to that interval of latency. The interval is one second, and half a second under the two tightest
  legal window pairs, so a time-driven transition always lands inside its announcement bound.
- **An empty family is not told when an outage ends.** After an `error` event the server re-states
  the family on its first successful read, which is what clears a map's *unconfirmed* marks. A family
  with **no devices at all** has nothing to re-state and the wire contract has no fourth event type
  to carry an "all clear", so that one page keeps its interruption notice until the viewer
  reconnects. Named rather than hidden; a fourth event type is the fix, and it is not this change.
- **A stream position update can be delayed by one fix under a rare write race.** The stream's cursor
  is `received_at`; if two fixes for a family commit out of `received_at` order within one poll
  interval, the later-committing one can be skipped until that device's *next* fix re-establishes it.
  At family scale (a few devices, seconds apart) this is vanishingly rare and self-heals on the next
  fix; the durable fix (a strictly monotonic stream sequence, or logical decoding) is a scale-phase
  concern, not a family-deployment one. The reconnect/resume path itself has no such gap.
- **The stream token travels in the URL for browsers.** `EventSource` cannot set an `Authorization`
  header, so the Leaflet page passes the viewer token as `?token=`. URLs leak into logs and referrers;
  this is an interim trade mitigated by TLS (S7) and retired by the in-app map (C5). Programmatic
  callers should use the header. With TLS now enforceable (S7), that `?token=` no longer crosses the
  wire in the clear; the log/referrer exposure remains until the in-app map (C5).
- **HTTP/2 comes with TLS.** SSE multiplexes cleanly over HTTP/2, which also lifts the browser's
  ~6-connections-per-origin cap. Go negotiates HTTP/2 automatically when the server terminates TLS
  (S7), so configuring a cert/key pair gets it; over plaintext HTTP/1.1 (`ALLOW_PLAINTEXT`, or a proxy
  that speaks HTTP/1.1 upstream), more than ~6 simultaneous tabs to the same origin can queue.
- **TLS is enforced, but you supply the certificate.** The server terminates HTTPS when
  `TRACKER_TLS_CERT_FILE` / `TRACKER_TLS_KEY_FILE` are set (TLS 1.2 floor), and it **refuses to start**
  in plaintext unless `TRACKER_ALLOW_PLAINTEXT=1` says so on purpose (dev, or a TLS-terminating proxy).
  It does not obtain or renew certificates - that is the operator's (or the proxy's) job. `tracker
  config-lint` fails on any plaintext endpoint or a checked-in secret.
- **Partitions are provisioned at start-up, with a two-month lookahead - plus an on-ingest safety
  net.** A process running longer than the lookahead would otherwise reach an unprovisioned month and
  reject every fix at the rollover: harmless before S2 (nothing ingested), silent data loss after it.
  So ingestion now provisions a fix's month **on demand** if it is missing, bounded by the timestamp
  window (`[now−90d, now+24h]`) so untrusted input cannot create partitions without limit. **S7 added
  the retention purge on a daily timer** (dropping *old* months); forward provisioning still rides the
  start-up lookahead plus this on-ingest safety net, so a dedicated forward-provisioning ticker remains
  a later refinement rather than a correctness gap.
- **Retention purging is coarse, and off by default.** With `TRACKER_RETENTION_DAYS` set, a daily job
  **drops** whole monthly `fixes` partitions older than the window - a `DROP`, not a `DELETE`, so it is
  transactional and all-or-nothing per month. Because partitions are monthly, a fix can outlive its
  retention date by up to a month. Unset (`0`) runs no purge and keeps history forever - silently
  deleting on a window nobody chose would be worse, so it is opt-in.
- **Migrations are not safe against concurrent migrators.** Every instance migrates on boot, so two
  starting at once would race. The compose stack runs one replica; whoever scales it out owns fixing this.
