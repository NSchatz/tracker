#!/usr/bin/env bash
# S0's acceptance gate: bring the REAL stack up and prove /healthz answers 200.
#
# "It compiled" and "the unit tests pass" do not prove that the binary can find its
# database, that the embedded migrations run inside the image, or that compose wired the
# two together. This does. It is the difference between a green build and a working
# deploy.
#
# Fails loudly on every path: there is no branch here that reports success without having
# seen a 200 whose body says the database is up.
set -Eeuo pipefail

cd "$(dirname "$0")/.."

PROJECT=tracker-smoke
COMPOSE=(docker compose -p "$PROJECT")
DEADLINE=120 # seconds; a cold image build is slow

# docker-compose.yml uses the `${VAR:?}` form for the database password, so it REFUSES to
# start without one — that is the point, and it means this gate must supply its own.
#
# Synthetic, throwaway, and local to a stack that publishes no database port and is torn
# down (`down -v`) on the way out. It is not a credential and must never become one.
export TRACKER_DB_PASSWORD="smoke-only-$$"

cleanup() {
  local code=$?
  if [ "$code" -ne 0 ]; then
    echo >&2
    echo "--- SMOKE FAILED (exit $code). Stack logs follow. ---" >&2
    "${COMPOSE[@]}" logs --no-color --tail=60 >&2 || true
  fi
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  exit "$code"
}
trap cleanup EXIT

echo "--- validating docker-compose.yml"
"${COMPOSE[@]}" config -q

echo "--- building and starting the stack"
"${COMPOSE[@]}" up -d --build

echo "--- waiting for /healthz (up to ${DEADLINE}s)"
body=""
code="000"
deadline=$((SECONDS + DEADLINE))
while [ "$SECONDS" -lt "$deadline" ]; do
  # compose publishes tracker on 8080, so drive it exactly as an operator would.
  code=$(curl -fsS -o /tmp/tracker-healthz.$$ -w '%{http_code}' \
    http://localhost:8080/healthz 2>/dev/null) || code="000"

  if [ "$code" = "200" ]; then
    body=$(cat /tmp/tracker-healthz.$$)
    rm -f /tmp/tracker-healthz.$$
    echo "--- /healthz => 200 ${body}"

    # A 200 is necessary but not sufficient. Assert the server actually reached PostGIS:
    # a health check reporting 200 while the database is down is the precise lie this
    # endpoint exists to prevent, so the smoke test will not accept the status code alone
    # as proof that the stack works.
    if ! printf '%s' "$body" | grep -q '"database":"up"'; then
      echo "--- /healthz returned 200 but does not report the database up: ${body}" >&2
      exit 1
    fi

    echo
    echo "--- SMOKE PASSED: the stack came up and tracker reached PostGIS."
    exit 0
  fi
  sleep 2
done

rm -f /tmp/tracker-healthz.$$
echo "--- /healthz never returned 200 within ${DEADLINE}s (last status: ${code})" >&2
exit 1
