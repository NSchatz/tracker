# Comment density record

The measurement `make prose-check` enforces, and the arithmetic its thresholds
were derived from. The gate is `cmd/prosecheck`; the counting is
`internal/prosegate`; the fixtures that prove it still bites are under
`internal/prosegate/testdata/refusals`.

This file states measurements. It is regenerated with
`go run ./cmd/prosecheck -table`.

## What is counted

- **Prose** is a comment whose content is English aimed at a reader.
- **Directives are code, never prose**, because deleting one changes what
  compiles, what is generated or what a linter does: build constraints in both
  forms, any `//go:` directive, `//line`, linter directives such as `//nolint`,
  and the generated-file marker.
- **A generated file leaves the measurement entirely.** Its comments are not a
  human's to trim.
- **A leading licence or copyright notice is in neither count.** It is prose by
  any reading and it is not deletable, so counting it either way distorts the
  ratio.
- **A line carrying a token is a code line** whatever is appended to it, so a
  trailing note does not delete a line of code out of the denominator. A line
  whose only content is comment text is a prose line.
- **Ratio** is prose lines divided by prose lines plus code lines. Blank lines
  are in neither.
- **Eligible file**: a Go file tracked by git, outside any `testdata` directory
  and any vendored tree, not generated, and with at least the minimum counted
  lines below. Below that minimum a file is reported in the table and carries no
  threshold: a twelve-line file with a four-line doc comment is a third prose and
  says nothing.

Counting is done from the Go token stream and comment nodes rather than from
line patterns. A comment marker inside a string literal, and the closing
delimiter of a raw string spanning several lines, read exactly like prose to
anything matching on lines.

## Pre-trim measurement

Every eligible Go file in the tree before any comment was trimmed.

| file | prose | code | counted | ratio | eligible |
|---|---:|---:|---:|---:|---|
| cmd/pincheck/main.go | 14 | 25 | 39 | 35.9% | yes |
| cmd/prosecheck/main.go | 18 | 34 | 52 | 34.6% | yes |
| cmd/tracker/main.go | 109 | 482 | 591 | 18.4% | yes |
| cmd/uiverify/main.go | 17 | 63 | 80 | 21.2% | yes |
| cmd/vulngate/main.go | 15 | 31 | 46 | 32.6% | yes |
| internal/auth/auth.go | 53 | 47 | 100 | 53.0% | yes |
| internal/auth/auth_test.go | 19 | 98 | 117 | 16.2% | yes |
| internal/config/config.go | 146 | 212 | 358 | 40.8% | yes |
| internal/config/config_test.go | 23 | 274 | 297 | 7.7% | yes |
| internal/config/windows_test.go | 31 | 181 | 212 | 14.6% | yes |
| internal/db/db.go | 41 | 76 | 117 | 35.0% | yes |
| internal/db/db_test.go | 62 | 138 | 200 | 31.0% | yes |
| internal/db/partitions.go | 75 | 122 | 197 | 38.1% | yes |
| internal/db/partitions_test.go | 46 | 246 | 292 | 15.8% | yes |
| internal/db/roles_test.go | 22 | 75 | 97 | 22.7% | yes |
| internal/pingate/demonstrations.go | 51 | 199 | 250 | 20.4% | yes |
| internal/pingate/gosource.go | 52 | 164 | 216 | 24.1% | yes |
| internal/pingate/gradle.go | 32 | 204 | 236 | 13.6% | yes |
| internal/pingate/images.go | 23 | 186 | 209 | 11.0% | yes |
| internal/pingate/manifests.go | 46 | 283 | 329 | 14.0% | yes |
| internal/pingate/pingate.go | 98 | 221 | 319 | 30.7% | yes |
| internal/pingate/pingate_test.go | 91 | 823 | 914 | 10.0% | yes |
| internal/pingate/regress_0059_F1_test.go | 2 | 53 | 55 | 3.6% | yes |
| internal/pingate/workflows.go | 23 | 148 | 171 | 13.5% | yes |
| internal/presentation/presentation.go | 98 | 57 | 155 | 63.2% | yes |
| internal/presentation/presentation_test.go | 35 | 126 | 161 | 21.7% | yes |
| internal/prosegate/count.go | 38 | 142 | 180 | 21.1% | yes |
| internal/prosegate/count_test.go | 6 | 204 | 210 | 2.9% | yes |
| internal/prosegate/demonstrations.go | 41 | 210 | 251 | 16.3% | yes |
| internal/prosegate/demonstrations_test.go | 14 | 76 | 90 | 15.6% | yes |
| internal/prosegate/prosegate.go | 33 | 71 | 104 | 31.7% | yes |
| internal/prosegate/record.go | 10 | 86 | 96 | 10.4% | yes |
| internal/prosegate/record_test.go | 4 | 105 | 109 | 3.7% | yes |
| internal/prosegate/sweep.go | 37 | 270 | 307 | 12.1% | yes |
| internal/prosegate/sweep_test.go | 7 | 216 | 223 | 3.1% | yes |
| internal/push/dispatcher_test.go | 19 | 114 | 133 | 14.3% | yes |
| internal/push/fcm.go | 32 | 87 | 119 | 26.9% | yes |
| internal/push/fcm_test.go | 11 | 116 | 127 | 8.7% | yes |
| internal/push/ledger.go | 186 | 131 | 317 | 58.7% | yes |
| internal/push/ledger_test.go | 93 | 498 | 591 | 15.7% | yes |
| internal/push/notify.go | 46 | 73 | 119 | 38.7% | yes |
| internal/push/notify_test.go | 6 | 42 | 48 | 12.5% | yes |
| internal/push/push.go | 119 | 142 | 261 | 45.6% | yes |
| internal/push/regress_0013_F1_test.go | 41 | 104 | 145 | 28.3% | yes |
| internal/push/serviceaccount.go | 39 | 167 | 206 | 18.9% | yes |
| internal/push/serviceaccount_test.go | 11 | 133 | 144 | 7.6% | yes |
| internal/push/unifiedpush.go | 21 | 55 | 76 | 27.6% | yes |
| internal/push/unifiedpush_test.go | 8 | 79 | 87 | 9.2% | yes |
| internal/retention/retention.go | 39 | 61 | 100 | 39.0% | yes |
| internal/retention/retention_test.go | 13 | 129 | 142 | 9.2% | yes |
| internal/secretscan/secretscan.go | 40 | 129 | 169 | 23.7% | yes |
| internal/secretscan/secretscan_test.go | 16 | 97 | 113 | 14.2% | yes |
| internal/server/alert_test.go | 69 | 208 | 277 | 24.9% | yes |
| internal/server/authz_surface_test.go | 48 | 185 | 233 | 20.6% | yes |
| internal/server/export_test.go | 9 | 11 | 20 | 45.0% | no (under 30) |
| internal/server/geofence.go | 32 | 74 | 106 | 30.2% | yes |
| internal/server/geofence_test.go | 22 | 143 | 165 | 13.3% | yes |
| internal/server/ingest_test.go | 64 | 302 | 366 | 17.5% | yes |
| internal/server/map_test.go | 54 | 166 | 220 | 24.5% | yes |
| internal/server/map_verification_record_test.go | 21 | 30 | 51 | 41.2% | yes |
| internal/server/presentation.go | 74 | 94 | 168 | 44.0% | yes |
| internal/server/presentation_read_test.go | 83 | 476 | 559 | 14.8% | yes |
| internal/server/presentation_stream_test.go | 114 | 576 | 690 | 16.5% | yes |
| internal/server/push.go | 58 | 69 | 127 | 45.7% | yes |
| internal/server/push_test.go | 16 | 102 | 118 | 13.6% | yes |
| internal/server/read.go | 71 | 229 | 300 | 23.7% | yes |
| internal/server/read_test.go | 46 | 424 | 470 | 9.8% | yes |
| internal/server/regress_0010_F5_test.go | 53 | 110 | 163 | 32.5% | yes |
| internal/server/server.go | 137 | 296 | 433 | 31.6% | yes |
| internal/server/server_test.go | 12 | 94 | 106 | 11.3% | yes |
| internal/server/stream.go | 259 | 379 | 638 | 40.6% | yes |
| internal/server/stream_test.go | 65 | 324 | 389 | 16.7% | yes |
| internal/store/entities.go | 115 | 189 | 304 | 37.8% | yes |
| internal/store/expiry_test.go | 17 | 94 | 111 | 15.3% | yes |
| internal/store/geofence.go | 168 | 284 | 452 | 37.2% | yes |
| internal/store/geofence_crud_test.go | 11 | 114 | 125 | 8.8% | yes |
| internal/store/geofence_test.go | 46 | 188 | 234 | 19.7% | yes |
| internal/store/ingest_test.go | 25 | 149 | 174 | 14.4% | yes |
| internal/store/presentation.go | 64 | 102 | 166 | 38.6% | yes |
| internal/store/push.go | 53 | 73 | 126 | 42.1% | yes |
| internal/store/push_test.go | 7 | 94 | 101 | 6.9% | yes |
| internal/store/read.go | 40 | 75 | 115 | 34.8% | yes |
| internal/store/spatial_test.go | 168 | 483 | 651 | 25.8% | yes |
| internal/store/store.go | 160 | 159 | 319 | 50.2% | yes |
| internal/store/validate_test.go | 19 | 109 | 128 | 14.8% | yes |
| internal/testsupport/postgis.go | 60 | 66 | 126 | 47.6% | yes |
| internal/toolchain/pins.go | 41 | 159 | 200 | 20.5% | yes |
| internal/toolchain/pins_test.go | 6 | 198 | 204 | 2.9% | yes |
| internal/uiverify/android.go | 69 | 188 | 257 | 26.8% | yes |
| internal/uiverify/android_demonstrations_test.go | 40 | 122 | 162 | 24.7% | yes |
| internal/uiverify/audit.go | 10 | 315 | 325 | 3.1% | yes |
| internal/uiverify/browser.go | 57 | 316 | 373 | 15.3% | yes |
| internal/uiverify/checks.go | 83 | 1353 | 1436 | 5.8% | yes |
| internal/uiverify/mutation.go | 18 | 54 | 72 | 25.0% | yes |
| internal/uiverify/readouts.go | 15 | 138 | 153 | 9.8% | yes |
| internal/uiverify/record.go | 118 | 492 | 610 | 19.3% | yes |
| internal/uiverify/record_test.go | 80 | 664 | 744 | 10.8% | yes |
| internal/uiverify/refusal.go | 52 | 276 | 328 | 15.9% | yes |
| internal/uiverify/regress_0056_F1_test.go | 48 | 88 | 136 | 35.3% | yes |
| internal/uiverify/regress_0056_F8_test.go | 88 | 205 | 293 | 30.0% | yes |
| internal/uiverify/regress_0074_F1_test.go | 24 | 98 | 122 | 19.7% | yes |
| internal/uiverify/regress_0074_F2_test.go | 30 | 83 | 113 | 26.5% | yes |
| internal/uiverify/run.go | 13 | 100 | 113 | 11.5% | yes |
| internal/uiverify/stack.go | 81 | 247 | 328 | 24.7% | yes |
| internal/uiverify/stack_test.go | 12 | 59 | 71 | 16.9% | yes |
| internal/vulngate/gate.go | 31 | 134 | 165 | 18.8% | yes |
| internal/vulngate/gate_test.go | 45 | 387 | 432 | 10.4% | yes |
| internal/vulngate/report.go | 58 | 259 | 317 | 18.3% | yes |
| internal/vulngate/report_test.go | 15 | 240 | 255 | 5.9% | yes |
| internal/vulngate/runner.go | 31 | 76 | 107 | 29.0% | yes |
| internal/vulngate/runner_test.go | 9 | 56 | 65 | 13.8% | yes |
| internal/vulngate/suppressions.go | 37 | 137 | 174 | 21.3% | yes |

files measured: 112
files eligible: 111
files generated and excluded: 0
repository-wide prose lines: 5543
repository-wide code lines: 21020
repository-wide ratio: 20.9%
highest eligible per-file ratio: 63.2% (internal/presentation/presentation.go, 98 prose, 57 code)
eligible files whose prose lines reach their code lines: 4
  internal/presentation/presentation.go 63.2% (98 prose, 57 code)
  internal/push/ledger.go 58.7% (186 prose, 131 code)
  internal/auth/auth.go 53.0% (53 prose, 47 code)
  internal/store/store.go 50.2% (160 prose, 159 code)

## Post-trim measurement

The same sweep after trimming. Comments only: no non-comment line changed.

| file | prose | code | counted | ratio | eligible |
|---|---:|---:|---:|---:|---|
| cmd/pincheck/main.go | 14 | 25 | 39 | 35.9% | yes |
| cmd/prosecheck/main.go | 18 | 34 | 52 | 34.6% | yes |
| cmd/tracker/main.go | 109 | 482 | 591 | 18.4% | yes |
| cmd/uiverify/main.go | 17 | 63 | 80 | 21.2% | yes |
| cmd/vulngate/main.go | 15 | 31 | 46 | 32.6% | yes |
| internal/auth/auth.go | 37 | 47 | 84 | 44.0% | yes |
| internal/auth/auth_test.go | 19 | 98 | 117 | 16.2% | yes |
| internal/config/config.go | 146 | 212 | 358 | 40.8% | yes |
| internal/config/config_test.go | 23 | 274 | 297 | 7.7% | yes |
| internal/config/windows_test.go | 31 | 181 | 212 | 14.6% | yes |
| internal/db/db.go | 41 | 76 | 117 | 35.0% | yes |
| internal/db/db_test.go | 62 | 138 | 200 | 31.0% | yes |
| internal/db/partitions.go | 75 | 122 | 197 | 38.1% | yes |
| internal/db/partitions_test.go | 46 | 246 | 292 | 15.8% | yes |
| internal/db/roles_test.go | 22 | 75 | 97 | 22.7% | yes |
| internal/pingate/demonstrations.go | 51 | 199 | 250 | 20.4% | yes |
| internal/pingate/gosource.go | 52 | 164 | 216 | 24.1% | yes |
| internal/pingate/gradle.go | 32 | 204 | 236 | 13.6% | yes |
| internal/pingate/images.go | 23 | 186 | 209 | 11.0% | yes |
| internal/pingate/manifests.go | 46 | 283 | 329 | 14.0% | yes |
| internal/pingate/pingate.go | 98 | 221 | 319 | 30.7% | yes |
| internal/pingate/pingate_test.go | 91 | 823 | 914 | 10.0% | yes |
| internal/pingate/regress_0059_F1_test.go | 2 | 53 | 55 | 3.6% | yes |
| internal/pingate/workflows.go | 23 | 148 | 171 | 13.5% | yes |
| internal/presentation/presentation.go | 45 | 57 | 102 | 44.1% | yes |
| internal/presentation/presentation_test.go | 35 | 126 | 161 | 21.7% | yes |
| internal/prosegate/count.go | 38 | 142 | 180 | 21.1% | yes |
| internal/prosegate/count_test.go | 6 | 204 | 210 | 2.9% | yes |
| internal/prosegate/demonstrations.go | 41 | 210 | 251 | 16.3% | yes |
| internal/prosegate/demonstrations_test.go | 14 | 76 | 90 | 15.6% | yes |
| internal/prosegate/prosegate.go | 33 | 71 | 104 | 31.7% | yes |
| internal/prosegate/record.go | 10 | 86 | 96 | 10.4% | yes |
| internal/prosegate/record_test.go | 4 | 105 | 109 | 3.7% | yes |
| internal/prosegate/sweep.go | 37 | 270 | 307 | 12.1% | yes |
| internal/prosegate/sweep_test.go | 7 | 216 | 223 | 3.1% | yes |
| internal/push/dispatcher_test.go | 19 | 114 | 133 | 14.3% | yes |
| internal/push/fcm.go | 32 | 87 | 119 | 26.9% | yes |
| internal/push/fcm_test.go | 11 | 116 | 127 | 8.7% | yes |
| internal/push/ledger.go | 106 | 131 | 237 | 44.7% | yes |
| internal/push/ledger_test.go | 93 | 498 | 591 | 15.7% | yes |
| internal/push/notify.go | 46 | 73 | 119 | 38.7% | yes |
| internal/push/notify_test.go | 6 | 42 | 48 | 12.5% | yes |
| internal/push/push.go | 95 | 142 | 237 | 40.1% | yes |
| internal/push/regress_0013_F1_test.go | 41 | 104 | 145 | 28.3% | yes |
| internal/push/serviceaccount.go | 39 | 167 | 206 | 18.9% | yes |
| internal/push/serviceaccount_test.go | 11 | 133 | 144 | 7.6% | yes |
| internal/push/unifiedpush.go | 21 | 55 | 76 | 27.6% | yes |
| internal/push/unifiedpush_test.go | 8 | 79 | 87 | 9.2% | yes |
| internal/retention/retention.go | 39 | 61 | 100 | 39.0% | yes |
| internal/retention/retention_test.go | 13 | 129 | 142 | 9.2% | yes |
| internal/secretscan/secretscan.go | 40 | 129 | 169 | 23.7% | yes |
| internal/secretscan/secretscan_test.go | 16 | 97 | 113 | 14.2% | yes |
| internal/server/alert_test.go | 69 | 208 | 277 | 24.9% | yes |
| internal/server/authz_surface_test.go | 48 | 185 | 233 | 20.6% | yes |
| internal/server/export_test.go | 9 | 11 | 20 | 45.0% | no (under 30) |
| internal/server/geofence.go | 32 | 74 | 106 | 30.2% | yes |
| internal/server/geofence_test.go | 22 | 143 | 165 | 13.3% | yes |
| internal/server/ingest_test.go | 64 | 302 | 366 | 17.5% | yes |
| internal/server/map_test.go | 54 | 166 | 220 | 24.5% | yes |
| internal/server/map_verification_record_test.go | 21 | 30 | 51 | 41.2% | yes |
| internal/server/presentation.go | 74 | 94 | 168 | 44.0% | yes |
| internal/server/presentation_read_test.go | 83 | 476 | 559 | 14.8% | yes |
| internal/server/presentation_stream_test.go | 114 | 576 | 690 | 16.5% | yes |
| internal/server/push.go | 47 | 69 | 116 | 40.5% | yes |
| internal/server/push_test.go | 16 | 102 | 118 | 13.6% | yes |
| internal/server/read.go | 71 | 229 | 300 | 23.7% | yes |
| internal/server/read_test.go | 46 | 424 | 470 | 9.8% | yes |
| internal/server/regress_0010_F5_test.go | 53 | 110 | 163 | 32.5% | yes |
| internal/server/server.go | 137 | 296 | 433 | 31.6% | yes |
| internal/server/server_test.go | 12 | 94 | 106 | 11.3% | yes |
| internal/server/stream.go | 259 | 379 | 638 | 40.6% | yes |
| internal/server/stream_test.go | 65 | 324 | 389 | 16.7% | yes |
| internal/store/entities.go | 115 | 189 | 304 | 37.8% | yes |
| internal/store/expiry_test.go | 17 | 94 | 111 | 15.3% | yes |
| internal/store/geofence.go | 168 | 284 | 452 | 37.2% | yes |
| internal/store/geofence_crud_test.go | 11 | 114 | 125 | 8.8% | yes |
| internal/store/geofence_test.go | 46 | 188 | 234 | 19.7% | yes |
| internal/store/ingest_test.go | 25 | 149 | 174 | 14.4% | yes |
| internal/store/presentation.go | 64 | 102 | 166 | 38.6% | yes |
| internal/store/push.go | 53 | 73 | 126 | 42.1% | yes |
| internal/store/push_test.go | 7 | 94 | 101 | 6.9% | yes |
| internal/store/read.go | 40 | 75 | 115 | 34.8% | yes |
| internal/store/spatial_test.go | 168 | 483 | 651 | 25.8% | yes |
| internal/store/store.go | 111 | 159 | 270 | 41.1% | yes |
| internal/store/validate_test.go | 19 | 109 | 128 | 14.8% | yes |
| internal/testsupport/postgis.go | 38 | 66 | 104 | 36.5% | yes |
| internal/toolchain/pins.go | 41 | 159 | 200 | 20.5% | yes |
| internal/toolchain/pins_test.go | 6 | 198 | 204 | 2.9% | yes |
| internal/uiverify/android.go | 69 | 188 | 257 | 26.8% | yes |
| internal/uiverify/android_demonstrations_test.go | 40 | 122 | 162 | 24.7% | yes |
| internal/uiverify/audit.go | 10 | 315 | 325 | 3.1% | yes |
| internal/uiverify/browser.go | 57 | 316 | 373 | 15.3% | yes |
| internal/uiverify/checks.go | 83 | 1353 | 1436 | 5.8% | yes |
| internal/uiverify/mutation.go | 18 | 54 | 72 | 25.0% | yes |
| internal/uiverify/readouts.go | 15 | 138 | 153 | 9.8% | yes |
| internal/uiverify/record.go | 118 | 492 | 610 | 19.3% | yes |
| internal/uiverify/record_test.go | 80 | 664 | 744 | 10.8% | yes |
| internal/uiverify/refusal.go | 52 | 276 | 328 | 15.9% | yes |
| internal/uiverify/regress_0056_F1_test.go | 48 | 88 | 136 | 35.3% | yes |
| internal/uiverify/regress_0056_F8_test.go | 88 | 205 | 293 | 30.0% | yes |
| internal/uiverify/regress_0074_F1_test.go | 24 | 98 | 122 | 19.7% | yes |
| internal/uiverify/regress_0074_F2_test.go | 30 | 83 | 113 | 26.5% | yes |
| internal/uiverify/run.go | 13 | 100 | 113 | 11.5% | yes |
| internal/uiverify/stack.go | 81 | 247 | 328 | 24.7% | yes |
| internal/uiverify/stack_test.go | 12 | 59 | 71 | 16.9% | yes |
| internal/vulngate/gate.go | 31 | 134 | 165 | 18.8% | yes |
| internal/vulngate/gate_test.go | 45 | 387 | 432 | 10.4% | yes |
| internal/vulngate/report.go | 58 | 259 | 317 | 18.3% | yes |
| internal/vulngate/report_test.go | 15 | 240 | 255 | 5.9% | yes |
| internal/vulngate/runner.go | 31 | 76 | 107 | 29.0% | yes |
| internal/vulngate/runner_test.go | 9 | 56 | 65 | 13.8% | yes |
| internal/vulngate/suppressions.go | 37 | 137 | 174 | 21.3% | yes |

files measured: 112
files eligible: 111
files generated and excluded: 0
repository-wide prose lines: 5288
repository-wide code lines: 21020
repository-wide ratio: 20.1%
highest eligible per-file ratio: 44.7% (internal/push/ledger.go, 106 prose, 131 code)
smallest whole multiple of five percentage points no eligible file exceeds: 45%

- error ceiling: 45%
- warning band: 35%
- minimum counted lines: 30

## How the thresholds were derived

Arithmetic, in order, not judgement.

1. **The floor.** Trim, worst file first, until no eligible file's prose lines
   reach its code lines. Fifty percent is not a tuned number: it is the point
   where a file has more narration than code. Four eligible files were over it
   before trimming and none is now.

2. **The ceiling** is the smallest whole multiple of five percentage points that
   no eligible file exceeds, and never above fifty. The highest eligible file
   after trimming is `internal/push/ledger.go` at 106 prose over 131 code, which
   is 44.7%: it exceeds 40 (106 x 100 > 40 x 237) and does not exceed 45
   (106 x 100 <= 45 x 237). So the ceiling is **45**.

3. **The band** is ten percentage points below the ceiling: **35**.

4. **The minimum** counted lines is 30. Below that a file is reported in the
   table and carries no threshold, because a twelve-line file with a four-line
   doc comment is a third prose and says nothing.

Comparison of the two measurements:

| figure | pre-trim | post-trim |
|---|---:|---:|
| repository-wide ratio | 20.9% | 20.1% |
| highest eligible per-file ratio | 63.2% | 44.7% |
| eligible files with prose lines reaching code lines | 4 | 0 |
| files measured | 112 | 112 |

The ratchet: the numbers above are committed, `make prose-check` enforces them,
`internal/prosegate` asserts the two agree, and raising one is a source change
with a name on it.
