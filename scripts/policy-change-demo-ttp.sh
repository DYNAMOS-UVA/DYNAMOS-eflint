#!/bin/bash
# Demonstrates a live policy change affecting the TTP / dataThroughTtp (archetype 2) request.
#
# Flow: run request -> revoke VU access for Jorrit -> confirm -> run request again -> restore VU policy.

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

echo "=== 1. Running request with original policy ==="
bash "$script_dir/trigger-and-poll-request.sh" 2

echo "=== 2. Waiting 5s ==="
sleep 5

echo "=== 3. Uploading restricted VU policy (no Jorrit permissions) ==="
curl -i -X PUT "http://127.0.0.1:18082/api/v1/policyEnforcer/VU" \
  -H "Content-Type: text/plain" \
  --data-binary @"$repo_root/configuration/eflint-models/temp/VU.eflint"

echo "=== 4. Waiting 5s ==="
sleep 5

echo "=== 5. Confirming policy ==="
curl -sS -G "http://127.0.0.1:18083/api/v1/policy-enforcer/allowed-clauses" \
  --data-urlencode "steward=VU" \
  --data-urlencode "requester=Jorrit"

echo "=== 6. Waiting 5s ==="
sleep 5

echo "=== 7. Running request again with restricted policy ==="
bash "$script_dir/trigger-and-poll-request.sh" 2

echo "=== 8. Waiting 5s ==="
sleep 5

echo "=== 9. Restoring original VU policy ==="
curl -i -X PUT "http://127.0.0.1:18082/api/v1/policyEnforcer/VU" \
  -H "Content-Type: text/plain" \
  --data-binary @"$repo_root/configuration/eflint-models/VU.eflint"

echo "=== 10. Waiting 5s ==="
sleep 5

echo "=== 11. Running request again with original policy restored ==="
bash "$script_dir/trigger-and-poll-request.sh" 2

echo "Done."
