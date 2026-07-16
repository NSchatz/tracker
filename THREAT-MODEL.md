# tracker — threat model

**Status:** current as of TRACKER-S7 (server hardening). This document is the honest statement of what
tracker's server protects, what it does not, and why one of those gaps is inherent rather than a bug to
be fixed later. It is deliberately plain: a family location tracker that is coy about its boundaries is
worse than one that names them.

tracker is a **self-hosted** family location tracker. An Android phone reports its location to a server
the family runs itself; family members watch each other on a live map and get alerts when someone
enters or leaves a place. The whole reason to self-host is to keep that data out of a commercial cloud.
This model is written from that stance: a **trusted operator** running the server for their own family,
**not** a multi-tenant public service.

---

## 1. What we are protecting

**The asset is a family's continuous, precise location history — often including minors.** It is
maximally sensitive: where people live, work, go to school, worship, and seek medical care are all
derivable from a location trail. A breach is not embarrassing, it is dangerous.

Everything else (tokens, the database password, TLS keys) matters only because it guards that asset.

---

## 2. Who we defend against, and how

| Adversary | Defended? | The control |
|---|---|---|
| **A network eavesdropper** (café Wi-Fi, ISP, on-path attacker) | **Yes** | TLS on every route (§3). The server refuses to start in plaintext unless the operator explicitly opts in, and `config-lint` fails any production config that would serve plaintext. |
| **Someone who reads the database** (a stolen backup, a dump, a compromised replica) | **Partly** | Tokens are stored only as SHA-256 hashes — a reader cannot replay them to impersonate a phone or a viewer. Location rows themselves are readable (see §5). |
| **A thief with an enrolled phone** | **Partly** | The device token is the credential; it can be **rotated** and given an **expiry** so a lost phone's token stops working. On the client, the token is held in encrypted storage (client track). A device token can only *write its own* fixes, never read the family. |
| **A malicious or careless family member / viewer** | **Partly** | Device vs viewer credentials are separate tables with separate privileges: a device **writes only its own** fixes, a viewer **reads only its own family**, and neither can act as the other. A viewer cannot forge another family's data — cross-family access is a 403. |
| **A third-party cloud harvesting location** (the Life360 failure mode) | **Yes** | Self-hosting is the control. tracker sends location to no one but the family's own server. |
| **A stale or leaked credential** | **Partly** | Tokens can be issued **short-lived** (`-ttl`) and **rotated**; an expired token authenticates nothing. Retention **auto-purge** (§4) bounds how much history a future breach can expose. |
| **A least-privilege escape via a narrow credential** | **Yes, within reach** | Database roles are minimal: `tracker_readonly` (SELECT only) and `tracker_writer` (DML, no DDL). A reporting or analytics connection can be given a role that cannot alter or delete a family's trail (§6). |

---

## 3. Transport security

- **TLS on all routes.** The server terminates HTTPS when `TRACKER_TLS_CERT_FILE` and
  `TRACKER_TLS_KEY_FILE` are set, with a **TLS 1.2 floor** (1.3 preferred). There is no plaintext
  endpoint and no "internal" route that skips TLS.
- **The fail-safe:** the server **refuses to start** with neither a TLS cert/key pair nor an explicit
  `TRACKER_ALLOW_PLAINTEXT=1`. Plaintext must be a decision (local dev, or a trusted reverse proxy that
  terminates TLS upstream), never the accident of an unset variable.
- **`tracker config-lint` is stricter than start-up:** it fails on *any* plaintext endpoint —
  `TRACKER_ALLOW_PLAINTEXT` does not satisfy it — so a production configuration cannot ship plaintext by
  inheriting a dev convenience. It also scans the repository for checked-in secrets.
- **Postgres TLS:** when the app and database are on separate hosts, the DSN should require TLS
  (`sslmode=verify-full`). The bundled Compose stack keeps the database on a private network with no
  published port, so it uses `sslmode=disable` there deliberately.

## 4. Data minimization & retention

- **Collect only what is used.** The ingestion schema takes `lat`/`lon`/`ts` and a few optional fields;
  absent optional fields stay absent, never fabricated.
- **Auto-purge.** With `TRACKER_RETENTION_DAYS` set, a background job **drops** whole monthly `fixes`
  partitions once their entire range is older than the window. It is a `DROP`, not a `DELETE`: expiring a
  month is instant and **all-or-nothing**, so a purge can never half-finish and leave a corrupted trail.
  Because partitions are monthly, up to a month of history survives past the exact date — the deliberate
  trade for a purge that cannot partially delete.
- **Keep-forever is opt-out, explicit.** The default (`0`) runs no purge. Silently deleting a family's
  history on a window nobody chose would be worse than keeping it, so retention is a number the operator
  sets.
- GDPR Article 5 (data minimization, storage limitation, security) informs this design. Whether GDPR
  legally *applies* to a private family deployment is jurisdiction-dependent (a "household activity"
  exemption may apply). **This is design guidance, not legal advice.**

## 5. The boundary we do NOT cross — and cannot

**tracker does not protect against a compromised server or a malicious administrator.**

A running tracker server **holds plaintext location**. It has to: it renders the live map and runs
PostGIS proximity and geofence queries, and both require the coordinates in the clear. We deliberately do
**not** column-encrypt the geometry — doing so would defeat the GiST index and every distance/containment
query the product is built on, turning a location tracker into one that cannot answer "who is near here".

The consequence, stated plainly:

- Anyone with **root on the server**, **superuser on the database**, or **the ability to read process
  memory** can read the family's location. TLS, hashed tokens, and least-privilege roles do not change
  this — they raise the bar for *other* adversaries, not this one.
- This is **not end-to-end encryption, and tracker never claims it is.** An E2E design where the server
  never sees plaintext is incompatible with server-side maps and geofencing. That trade-off is a product
  decision (roadmap §2, §7), not an oversight.
- **This gap is inherent, not a fixable bug.** No future phase closes it while the server still renders
  the map. The honest mitigations are operational and out of scope for the app: run the server on
  hardware you control, use full-disk/volume encryption so a *powered-off* disk is protected, keep
  backups encrypted, and limit who has shell and database-superuser access.

If you need a tracker where the operator physically cannot see location, tracker is the wrong tool, and
we would rather say so here than imply a guarantee we do not provide.

## 6. Database roles (least privilege)

Migration `00005` establishes two group roles the operator builds login roles from:

- **`tracker_readonly`** — `SELECT` only, including on future `fixes` partitions. The shape of a
  reporting connection or a read replica consumer: it can never change or delete a family's trail.
- **`tracker_writer`** — `SELECT` + `INSERT`/`UPDATE`/`DELETE`, but **no DDL**. It cannot create or drop
  a table, so a compromised writer cannot drop a partition (the retention mechanism) or reshape the
  schema.

`PUBLIC`'s `CREATE` on the `public` schema is revoked, so a new role gets nothing it was not granted.

**Caveat, stated honestly:** the server's *own* connection needs more than `tracker_writer` — it
provisions and drops monthly partitions (DDL) for ingestion and retention — so it connects as the schema
**owner**. `tracker_writer` is the least-privilege template for **every other** consumer (dashboards,
exports, analytics), which is exactly where an over-privileged credential would otherwise leak in.

## 7. Secrets

- **No secrets in the repository, ever.** The database password, TLS private key, and FCM service-account
  key live in the environment or a secret store and are read at start-up. The Compose password is a
  `${VAR:?}` reference with no default; the testcontainer credential is a synthetic, per-run throwaway.
- **`tracker config-lint` enforces this preventively:** it scans the tree for PEM private keys, cloud
  access-key ids, and DSNs carrying real (non-placeholder) passwords, and fails the build if it finds
  one. It is the repo's own gate, complementary to the umbrella's detective secret-scan hook.
- **Tokens are never stored, only their SHA-256 hashes are.** A device or viewer token is shown once, at
  issue or rotation, and is not recoverable. A leaked database cannot yield a usable token.

## 8. Authentication & authorization summary

- **Per-credential bearer tokens.** Device tokens authenticate writes; viewer tokens authenticate reads.
  They live in **separate tables**, so a device token presented to a read route (or vice versa) matches
  nothing and is a 401.
- **Expiry and rotation.** A token may be issued or rotated with a lifetime (`-ttl`); an expired token
  authenticates nothing, decided by the **database's** clock so a client cannot lie about the time.
  Rotation writes a new hash and expiry in one statement — the old token dies as the new one is minted.
- **Authz by construction.** A fix is stored under the *authenticated* device, never a `device_id` in the
  body, so a device cannot write another's history. Every read is scoped to the caller's family in the
  SQL itself; a cross-family read is a 403 and an empty result is `[]`, never another family's data.

---

## 9. Known residual risks

- **Server compromise / malicious admin** — inherent (§5). Mitigated only operationally.
- **Coarse retention** — up to a month of history outlives its exact expiry date, by design (§4).
- **Single-migrator assumption** — provisioning and migrations assume one writer; scaling to multiple
  replicas needs a migration lock (documented in `internal/db`). Not a confidentiality risk today.
- **Best-effort push delivery** — geofence alerts ride FCM/UnifiedPush and are not guaranteed (S6). An
  availability limit, not a confidentiality one.
