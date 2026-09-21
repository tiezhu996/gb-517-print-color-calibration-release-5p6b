#!/usr/bin/env sh
set -eu
project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_root"
set -a
if [ -f .env ]; then . ./.env; else . ./.env.example; fi
set +a
(command -v jq >/dev/null 2>&1) || { echo "jq is required for API validation" >&2; exit 1; }
(cd backend && go test ./... && go build ./...)
(cd frontend && npm install --no-audit --no-fund && npm run build)
docker compose config --quiet
docker compose up -d --build
cleanup() { docker compose down -v --remove-orphans; }
if [ "${KEEP_RUNNING:-0}" = "1" ]; then
  trap cleanup INT TERM
else
  trap cleanup EXIT INT TERM
fi
i=0
until curl -fsS "http://127.0.0.1:${BACKEND_PORT:-19517}/healthz" >/dev/null; do
  i=$((i+1)); [ "$i" -lt 60 ] || { docker compose logs; exit 1; }; sleep 2
done
curl -fsS "http://127.0.0.1:${FRONTEND_PORT:-18517}/" >/dev/null
login_token() {
  curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT:-19517}/api/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"Admin123!\"}" | jq -er '.data.token'
}
token=$(login_token admin)
viewer_token=$(login_token viewer)
operator_token=$(login_token operator)
reviewer_token=$(login_token reviewer)
[ -n "$token" ]
curl -fsS "http://127.0.0.1:${BACKEND_PORT:-19517}/api/overview" -H "Authorization: Bearer $token" >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/session" -H "Authorization: Bearer $token" | jq -e '.data.role == "admin" and (.data.requestId | length > 0)' >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/runtime" -H "Authorization: Bearer $token" | jq -e '.data.appName and .data.databaseDriver and (.data.requestLimit > 0)' >/dev/null
paths=$(sed -n "s/.*path: '\\([^']*\\)'.*/\\1/p" frontend/src/types/status.ts)
for path in $paths; do
  curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/$path?page=1&pageSize=20" -H "Authorization: Bearer $token" | jq -e '.data | type == "array"' >/dev/null
done
entity_config=$(sed -n "s/.*path: '\\([^']*\\)'.*statuses: \\['\\([^']*\\)', '\\([^']*\\)'.*/\\1|\\2|\\3/p" frontend/src/types/status.ts | head -n 1)
resource=$(printf '%s' "$entity_config" | cut -d '|' -f 1)
initial_status=$(printf '%s' "$entity_config" | cut -d '|' -f 2)
next_status=$(printf '%s' "$entity_config" | cut -d '|' -f 3)
now=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
code="SMOKE-$(date +%s)"
payload=$(printf '{"code":"%s","name":"Runtime smoke record","description":"Automated Compose workflow validation","facility":"Validation Lab","owner":"admin","category":"smoke","riskLevel":"low","metricValue":1,"metricUnit":"unit","effectiveAt":"%s","evidence":"scripts/validate.sh","relatedCode":"SMOKE"}' "$code" "$now")
created=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/$resource" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$payload")
id=$(printf '%s' "$created" | jq -er '.data.id')
version=$(printf '%s' "$created" | jq -er '.data.version')
printf '%s' "$created" | jq -e --arg status "$initial_status" '.data.status == $status' >/dev/null
transition=$(printf '{"status":"%s","expectedVersion":%s,"reason":"automated runtime validation"}' "$next_status" "$version")
curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/$resource/$id/transition" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$transition" | jq -e --arg status "$next_status" '.data.status == $status' >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/audits?page=1&pageSize=100" -H "Authorization: Bearer $token" | jq -e '.meta.total >= 2' >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/audit-summary?windowHours=24" -H "Authorization: Bearer $token" | jq -e '.data.total >= 2 and .data.transitions >= 1' >/dev/null

# A viewer may inspect operations but may never mutate them or inspect audits.
viewer_write_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs" \
  -H "Authorization: Bearer $viewer_token" -H 'Content-Type: application/json' -d "$payload")
[ "$viewer_write_status" = "403" ]
viewer_audit_status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${BACKEND_PORT}/api/audits" -H "Authorization: Bearer $viewer_token")
[ "$viewer_audit_status" = "403" ]

# Release decisions are versioned and only reviewer/admin may cross the release gate.
decision_code="RD-SMOKE-$(date +%s)"
decision_payload=$(printf '{"code":"%s","name":"Runtime release gate","description":"RBAC and immutable revision validation","facility":"Validation Lab","owner":"operator","category":"calibration","riskLevel":"medium","metricValue":2.2,"metricUnit":"dE","effectiveAt":"%s","evidence":"spectrophotometer validation evidence","relatedCode":"PR-001"}' "$decision_code" "$now")
decision=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/release" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -H 'X-Request-ID: release-create-smoke' -d "$decision_payload")
decision_id=$(printf '%s' "$decision" | jq -er '.data.id')
decision_version=$(printf '%s' "$decision" | jq -er '.data.version')
release_payload=$(printf '{"status":"release","expectedVersion":%s,"reason":"validated proof and colour tolerance"}' "$decision_version")
operator_release_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/release/$decision_id/transition" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$release_payload")
[ "$operator_release_status" = "403" ]
curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/release/$decision_id/transition" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -H 'X-Request-ID: release-review-smoke' -d "$release_payload" | jq -e '.data.status == "release" and .data.version == 2' >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/release/$decision_id" -H "Authorization: Bearer $reviewer_token" | jq -e '.data.revisions | length == 2 and .[0].requestId == "release-review-smoke" and .[1].requestId == "release-create-smoke"' >/dev/null

# Proof capture is operational work; accepting the proof is a reviewer action.
proof_code="CP-SMOKE-$(date +%s)"
proof_payload=$(printf '{"code":"%s","name":"Runtime proof gate","description":"Proof acceptance validation","facility":"Validation Lab","owner":"operator","category":"calibration","riskLevel":"low","metricValue":1.8,"metricUnit":"dE","effectiveAt":"%s","evidence":"proof strip measurements","relatedCode":"PR-001"}' "$proof_code" "$now")
proof=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/proofs" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$proof_payload")
proof_id=$(printf '%s' "$proof" | jq -er '.data.id')
proof_version=$(printf '%s' "$proof" | jq -er '.data.version')
proof_review=$(printf '{"status":"review","expectedVersion":%s,"reason":"measurement capture completed"}' "$proof_version")
proof=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/proofs/$proof_id/transition" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$proof_review")
proof_version=$(printf '%s' "$proof" | jq -er '.data.version')
proof_accept=$(printf '{"status":"accepted","expectedVersion":%s,"reason":"colour tolerance independently verified"}' "$proof_version")
operator_accept_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/proofs/$proof_id/transition" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$proof_accept")
[ "$operator_accept_status" = "403" ]
curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/proofs/$proof_id/transition" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$proof_accept" | jq -e '.data.status == "accepted"' >/dev/null

# Batch colour re-calibration closed loop: one pending request per batch,
# equipment/deadline validation, reviewer-only actions, pass releases the
# gate and fail holds the batch with an auto-generated quarantine decision.
cal_run_code="PR-CAL-SMOKE-$(date +%s)"
cal_run_payload=$(printf '{"code":"%s","name":"Runtime calibration batch","description":"Calibration closed loop validation","facility":"Validation Lab","owner":"operator","category":"calibration","riskLevel":"high","metricValue":2.8,"metricUnit":"dE","effectiveAt":"%s","evidence":"proofing batch entering recalibration","relatedCode":"PR-001"}' "$cal_run_code" "$now")
cal_run=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$cal_run_payload")
cal_run_id=$(printf '%s' "$cal_run" | jq -er '.data.id')
cal_run_version=$(printf '%s' "$cal_run" | jq -er '.data.version')
for cal_state in printing proofing; do
	cal_step=$(printf '{"status":"%s","expectedVersion":%s,"reason":"advance calibration smoke batch"}' "$cal_state" "$cal_run_version")
	cal_run=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs/$cal_run_id/transition" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$cal_step")
	cal_run_version=$(printf '%s' "$cal_run" | jq -er '.data.version')
done
cal_due=$(date -u -d '+2 days' '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -v+2d '+%Y-%m-%dT%H:%M:%SZ')
cal_past=$(date -u -d '-1 hour' '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -v-1H '+%Y-%m-%dT%H:%M:%SZ')
cal_open=$(printf '{"printRunId":%s,"pressUnitId":1,"targetDelta":2,"samples":"CMYK colour bar and gray balance","retestDueAt":"%s"}' "$cal_run_id" "$cal_due")
operator_cal_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$cal_open")
[ "$operator_cal_status" = "403" ]
cal_past_body=$(printf '{"printRunId":%s,"pressUnitId":1,"targetDelta":2,"samples":"CMYK colour bar","retestDueAt":"%s"}' "$cal_run_id" "$cal_past")
cal_past_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$cal_past_body")
[ "$cal_past_status" = "422" ]
calibration=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -H 'X-Request-ID: calibration-open-smoke' -d "$cal_open")
calibration_id=$(printf '%s' "$calibration" | jq -er '.data.id')
printf '%s' "$calibration" | jq -e '.data.status == "pending" and .data.version == 1 and .data.pressCode == "PU-001"' >/dev/null
cal_duplicate_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$cal_open")
[ "$cal_duplicate_status" = "409" ]
cal_blocked=$(printf '{"status":"released","expectedVersion":%s,"reason":"release must wait for retest"}' "$cal_run_version")
cal_blocked_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs/$cal_run_id/transition" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$cal_blocked")
[ "$cal_blocked_status" = "422" ]
cal_complete=$(printf '{"expectedVersion":1,"measuredDelta":1.6,"result":"passed","resultNote":"within target tolerance"}')
curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations/$calibration_id/complete" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -H 'X-Request-ID: calibration-pass-smoke' -d "$cal_complete" | jq -e '.data.status == "passed" and .data.version == 2 and .data.runStatus == "proofing"' >/dev/null
cal_repeat_status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations/$calibration_id/complete" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$cal_complete")
[ "$cal_repeat_status" = "409" ]
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/calibrations/$calibration_id" -H "Authorization: Bearer $reviewer_token" | jq -e '.data.measuredDelta == 1.6 and .data.completedBy == "reviewer"' >/dev/null
curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs/$cal_run_id/transition" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$cal_blocked" | jq -e '.data.status == "released"' >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/runs/$cal_run_id" -H "Authorization: Bearer $reviewer_token" | jq -e '.data.latestCalibration.status == "passed" and (.data.calibrations | length == 1)' >/dev/null

# Failure branch on a second batch: over-tolerance retest holds the batch and
# creates exactly one immutable quarantine decision.
cal_fail_code="PR-CAL-FAIL-$(date +%s)"
cal_fail_payload=$(printf '{"code":"%s","name":"Runtime calibration fail batch","description":"Over tolerance calibration validation","facility":"Validation Lab","owner":"operator","category":"calibration","riskLevel":"high","metricValue":3.4,"metricUnit":"dE","effectiveAt":"%s","evidence":"over tolerance proofing batch","relatedCode":"PR-001"}' "$cal_fail_code" "$now")
cal_fail_run=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$cal_fail_payload")
cal_fail_run_id=$(printf '%s' "$cal_fail_run" | jq -er '.data.id')
cal_fail_version=$(printf '%s' "$cal_fail_run" | jq -er '.data.version')
for cal_state in printing proofing; do
	cal_fail_step=$(printf '{"status":"%s","expectedVersion":%s,"reason":"advance failing calibration batch"}' "$cal_state" "$cal_fail_version")
	cal_fail_run=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/runs/$cal_fail_run_id/transition" -H "Authorization: Bearer $operator_token" -H 'Content-Type: application/json' -d "$cal_fail_step")
	cal_fail_version=$(printf '%s' "$cal_fail_run" | jq -er '.data.version')
done
cal_fail_open=$(printf '{"printRunId":%s,"pressUnitId":2,"targetDelta":2,"samples":"CMYK colour bar and gray balance","retestDueAt":"%s"}' "$cal_fail_run_id" "$cal_due")
cal_fail_cal=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -d "$cal_fail_open")
cal_fail_cal_id=$(printf '%s' "$cal_fail_cal" | jq -er '.data.id')
cal_fail_complete='{"expectedVersion":1,"measuredDelta":3.4,"result":"failed","resultNote":"magenta channel out of tolerance"}'
cal_fail_result=$(curl -fsS -X POST "http://127.0.0.1:${BACKEND_PORT}/api/calibrations/$cal_fail_cal_id/complete" -H "Authorization: Bearer $reviewer_token" -H 'Content-Type: application/json' -H 'X-Request-ID: calibration-fail-smoke' -d "$cal_fail_complete")
quarantine_id=$(printf '%s' "$cal_fail_result" | jq -er '.data.quarantineDecisionId')
printf '%s' "$cal_fail_result" | jq -e '.data.status == "failed" and .data.deviation == 1.4 and .data.runStatus == "hold" and (.data.quarantineCode | startswith("RD-CAL-"))' >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/runs/$cal_fail_run_id" -H "Authorization: Bearer $reviewer_token" | jq -e ".data.status == \"hold\" and .data.version == $((cal_fail_version + 1))" >/dev/null
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/api/release/$quarantine_id" -H "Authorization: Bearer $reviewer_token" | jq -e '.data.status == "quarantine" and .data.metricValue == 3.4 and (.data.revisions | length == 1)' >/dev/null
docker compose ps
[ "${KEEP_RUNNING:-0}" = "1" ] && echo "KEEP_RUNNING=1: containers left running for browser validation"
