#!/usr/bin/env bash
#
# run_model_test.sh -- build and run the singleton_driver_model model-checker
# against a local single-node v2 ledger, then report any findings to stdout.
#
# v2 is single-node, so the topology is fixed and simple: Postgres + the ledger
# image + a thin gateway (Caddy) that maps /api/ledger/* onto the ledger, which
# is the path the SDK the driver uses expects. No fault injection here, so
# transient errors are rare; this is the fast inner-loop check.
#
# A "finding" is a failed antithesis assertion (a hit Unreachable, or an
# Always/Sometimes whose condition was false), or a driver/ledger panic.
#
# Usage:
#   ./run_model_test.sh [DURATION_SECONDS]      # default 30
#
# Environment:
#   LEDGER_TAG       ledger image tag (default v2.3.13)
#   HTTP_PORT        host port for the gateway (default: random)
#   MODEL_LEDGERS    logical ledgers (default: driver default)
#   MODEL_WORKERS    concurrent workers (default: driver default)
#   MODEL_DEBUG      set to enable driver debug logging
#   MODEL_FAIL_FAST  stop on the first finding (default on); 0/off runs the full
#                    duration; a substring stops only on a matching finding
#   KEEP_WORKDIR     if set, don't delete the temp work dir on exit

set -uo pipefail

DURATION="${1:-30}"
case "$DURATION" in ''|*[!0-9]*) echo "ERROR: duration must be a positive integer (got '$DURATION')" >&2; exit 2 ;; esac

LEDGER_TAG="${LEDGER_TAG:-v2.3.13}"
POSTGRES_IMAGE="postgres:15-alpine"
CADDY_IMAGE="caddy:2-alpine"
HTTP_PORT="${HTTP_PORT:-$(( 20000 + RANDOM % 10000 ))}"
MODEL_FAIL_FAST="${MODEL_FAIL_FAST-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKLOAD_DIR="$SCRIPT_DIR/workload"

# Unique per-run names so a leftover from a killed shell never collides with or
# pollutes a new run.
RUN="model-$$-$RANDOM"
NET="$RUN-net"
PG="$RUN-pg"
LEDGER="$RUN-ledger"
GW="$RUN-gw"

WORKDIR="$(mktemp -d /tmp/run-model-test.XXXXXX)"
DRIVER_BIN="$WORKDIR/model-driver"
DRIVER_LOG="$WORKDIR/driver.log"
ASSERTIONS="$WORKDIR/assertions.json"
CADDYFILE="$WORKDIR/Caddyfile"
DRIVER_PID=""
DRIVER_EXITED_EARLY=0

log() { echo "[run_model_test] $*"; }

cleanup() {
	[ -n "$DRIVER_PID" ] && kill "$DRIVER_PID" 2>/dev/null
	sleep 1
	[ -n "$DRIVER_PID" ] && kill -9 "$DRIVER_PID" 2>/dev/null
	docker rm -f "$GW" "$LEDGER" "$PG" >/dev/null 2>&1
	docker network rm "$NET" >/dev/null 2>&1
	wait 2>/dev/null
	if [ -z "${KEEP_WORKDIR:-}" ]; then rm -rf "$WORKDIR"; else log "work dir kept at $WORKDIR"; fi
}
trap cleanup EXIT INT TERM

command -v docker >/dev/null || { echo "ERROR: docker not found" >&2; exit 2; }

# ---------------------------------------------------------------------------
# Build the driver. Prefer the nix dev shell for a reproducible toolchain (as
# CI does), unless nix is unavailable or we are already inside one.
# ---------------------------------------------------------------------------
build_cmd="cd '$WORKLOAD_DIR' && go build -o '$DRIVER_BIN' ./bin/cmds/model/singleton_driver_model"
if [ -n "${IN_NIX_SHELL:-}" ] || ! command -v nix >/dev/null; then
	log "building driver..."
	build_runner=( bash -c "$build_cmd" )
else
	log "building driver (via nix develop)..."
	build_runner=( nix develop "$SCRIPT_DIR" --command bash -c "$build_cmd" )
fi
if ! "${build_runner[@]}" > "$WORKDIR/build.log" 2>&1; then
	echo "ERROR: build failed:" >&2
	cat "$WORKDIR/build.log" >&2
	exit 2
fi
log "build ok"

# ---------------------------------------------------------------------------
# Bring up Postgres + ledger + gateway on a private network. The gateway strips
# the /api/ledger prefix the SDK adds and forwards to the ledger's HTTP API.
# ---------------------------------------------------------------------------
cat > "$CADDYFILE" <<'EOF'
:8080 {
	handle_path /api/ledger/* {
		reverse_proxy ledger:3068
	}
}
EOF

docker network create "$NET" >/dev/null || { echo "ERROR: could not create network" >&2; exit 2; }

log "starting postgres..."
docker run -d --rm --name "$PG" --network "$NET" --network-alias postgres \
	-e POSTGRES_USER=ledger -e POSTGRES_PASSWORD=ledger -e POSTGRES_DB=ledger \
	"$POSTGRES_IMAGE" >/dev/null || { echo "ERROR: postgres failed to start" >&2; exit 2; }

log "waiting for postgres..."
for _ in $(seq 1 30); do
	docker exec "$PG" pg_isready -U ledger >/dev/null 2>&1 && break
	sleep 1
done

log "starting ledger ($LEDGER_TAG)..."
docker run -d --rm --name "$LEDGER" --network "$NET" --network-alias ledger \
	-e POSTGRES_URI="postgresql://ledger:ledger@postgres/ledger?sslmode=disable" \
	-e AUTO_UPGRADE=true -e EXPERIMENTAL_FEATURES=true -e DEBUG="${MODEL_DEBUG:+true}" \
	"ghcr.io/formancehq/ledger:$LEDGER_TAG" >/dev/null || { echo "ERROR: ledger failed to start" >&2; exit 2; }

log "starting gateway (host port $HTTP_PORT)..."
docker run -d --rm --name "$GW" --network "$NET" --network-alias gateway \
	-p "127.0.0.1:$HTTP_PORT:8080" -v "$CADDYFILE:/etc/caddy/Caddyfile:ro" \
	"$CADDY_IMAGE" >/dev/null || { echo "ERROR: gateway failed to start" >&2; exit 2; }

GATEWAY_URL="http://localhost:$HTTP_PORT"
log "waiting for ledger to be ready at $GATEWAY_URL ..."
ready=0
for _ in $(seq 1 60); do
	if curl -fsS "$GATEWAY_URL/api/ledger/v2" >/dev/null 2>&1; then ready=1; break; fi
	if ! docker ps --format '{{.Names}}' | grep -q "^$LEDGER$"; then
		echo "ERROR: ledger container exited during startup:" >&2
		docker logs "$LEDGER" 2>&1 | tail -20 >&2
		exit 2
	fi
	sleep 1
done
[ "$ready" -eq 1 ] || { echo "ERROR: ledger not ready within 60s" >&2; docker logs "$LEDGER" 2>&1 | tail -20 >&2; exit 2; }
log "ledger ready"

# ---------------------------------------------------------------------------
# Run the driver. MODEL_MAX_SECONDS makes it self-terminate even if this script
# never signals it (defence against an orphaned driver); a small buffer over
# DURATION lets the script-driven stop win in the normal case.
# ---------------------------------------------------------------------------
log "running driver for ${DURATION}s ..."
GATEWAY_URL="$GATEWAY_URL" \
ANTITHESIS_SDK_LOCAL_OUTPUT="$ASSERTIONS" \
MODEL_DEBUG="${MODEL_DEBUG:-}" \
MODEL_LEDGERS="${MODEL_LEDGERS:-}" \
MODEL_WORKERS="${MODEL_WORKERS:-}" \
MODEL_MAX_SECONDS="$(( DURATION + 15 ))" \
	"$DRIVER_BIN" > "$DRIVER_LOG" 2>&1 &
DRIVER_PID=$!

# True (0) when MODEL_FAIL_FAST is set and a finding (condition:false + hit:true,
# optionally matching the MODEL_FAIL_FAST substring) is present.
check_fail_fast() {
	case "$MODEL_FAIL_FAST" in ''|0|off|false|no) return 1 ;; esac
	[ -s "$ASSERTIONS" ] || return 1
	local ff
	ff="$(grep -E '"condition":[[:space:]]*false' "$ASSERTIONS" 2>/dev/null | grep -E '"hit":[[:space:]]*true')"
	[ -n "$ff" ] || return 1
	[ "$MODEL_FAIL_FAST" = "1" ] || ff="$(printf '%s\n' "$ff" | grep -F "$MODEL_FAIL_FAST")"
	[ -n "$ff" ]
}

deadline=$(( $(date +%s) + DURATION ))
while [ "$(date +%s)" -lt "$deadline" ]; do
	if ! kill -0 "$DRIVER_PID" 2>/dev/null; then log "driver exited early"; DRIVER_EXITED_EARLY=1; break; fi
	if check_fail_fast; then log "fail-fast: model finding detected, stopping early"; break; fi
	sleep 1
done

log "stopping driver..."
kill "$DRIVER_PID" 2>/dev/null
for _ in $(seq 1 5); do kill -0 "$DRIVER_PID" 2>/dev/null || break; sleep 1; done
kill -9 "$DRIVER_PID" 2>/dev/null
wait "$DRIVER_PID" 2>/dev/null
DRIVER_PID=""

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
echo
echo "================= model test report ================="
findings=0

# 1. Failed assertions (condition:false AND hit:true) from the driver model.
if [ -s "$ASSERTIONS" ]; then
	if command -v jq >/dev/null 2>&1; then
		failed="$(jq -c 'select(.antithesis_assert.hit == true and .antithesis_assert.condition == false) | .antithesis_assert | {display_type, message, details}' "$ASSERTIONS" 2>/dev/null)"
	else
		failed="$(grep -E '"hit":[[:space:]]*true' "$ASSERTIONS" 2>/dev/null | grep -E '"condition":[[:space:]]*false')"
	fi
	if [ -n "$failed" ]; then
		echo "MODEL FINDINGS (failed assertions):"
		echo "$failed"
		findings=$((findings + $(printf '%s\n' "$failed" | grep -c .)))
	else
		echo "model findings: none"
	fi
else
	echo "WARNING: no assertion output at $ASSERTIONS (driver may not have started -- see driver log below)"
fi

# 2. Driver panic / crash.
if grep -qE "panic:|fatal error:" "$DRIVER_LOG" 2>/dev/null; then
	echo
	echo "DRIVER CRASH:"
	grep -nE "panic:|fatal error:" "$DRIVER_LOG" | head -5
	findings=$((findings + 1))
fi

# 3. Ledger panic / crash.
if docker logs "$LEDGER" 2>&1 | grep -qE "panic:|fatal error:"; then
	echo
	echo "LEDGER CRASH:"
	docker logs "$LEDGER" 2>&1 | grep -nE "panic:|fatal error:" | head -5
	findings=$((findings + 1))
fi

# 4. Driver exited before the deadline with nothing above explaining it. The
# driver self-terminates only at MODEL_MAX_SECONDS (> DURATION), so an earlier
# exit is abnormal -- typically a setup/connection error that logged and
# returned. The model did not run for the requested duration, so it is not a pass.
if [ "$DRIVER_EXITED_EARLY" -ne 0 ] && [ "$findings" -eq 0 ]; then
	echo
	echo "DRIVER EXITED EARLY: the model ran for less than ${DURATION}s (likely a setup/connection error; driver log:)"
	tail -20 "$DRIVER_LOG" 2>/dev/null
	findings=$((findings + 1))
fi

echo "-----------------------------------------------------"
if [ "$findings" -eq 0 ]; then
	echo "RESULT: PASS (no findings)"
	echo "====================================================="
	exit 0
fi
echo "RESULT: FAIL ($findings finding(s))"
echo "  driver log: $DRIVER_LOG"
echo "  assertions: $ASSERTIONS"
echo "  (re-run with KEEP_WORKDIR=1 to preserve these)"
echo "====================================================="
exit 1
