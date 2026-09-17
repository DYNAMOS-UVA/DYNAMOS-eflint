#!/bin/bash
# Sends a hardcoded requestApproval and polls /requestStatus every 2s (70s timeout).
#
# Usage: ./trigger-and-poll-request.sh [1|2]
#   1 = computeToData example (aggregate:false)   [default]
#   2 = ttp / dataThroughTtp example (aggregate:true)

base_url="http://localhost:8080"
poll_interval=2
poll_timeout=120
choice="${1:-1}"

body_1='{
    "type": "sqlDataRequest",
    "user": {
        "id": "12324",
        "userName": "Jorrit"
    },
    "dataProviders": ["VU","UVA","RUG"],
    "data_request": {
        "type": "sqlDataRequest",
        "query" : "SELECT * FROM Personen p JOIN Aanstellingen s ON p.Unieknr = s.Unieknr LIMIT 1000",
        "algorithm" : "average",
        "options" : {
            "graph" : false,
            "aggregate": false
        },
        "requestMetadata": {}
    }
}'

body_2='{
    "type": "sqlDataRequest",
    "user": {
        "id": "12324",
        "userName": "Jorrit"
    },
    "dataProviders": ["VU","UVA","RUG"],
    "data_request": {
        "type": "sqlDataRequest",
        "query" : "SELECT * FROM Personen p JOIN Aanstellingen s ON p.Unieknr = s.Unieknr LIMIT 1000",
        "algorithm" : "average",
        "options" : {
            "graph" : false,
            "aggregate": true
        },
        "requestMetadata": {}
    }
}'

if [ "$choice" = "1" ]; then
  body="$body_1"
elif [ "$choice" = "2" ]; then
  body="$body_2"
else
  echo "Usage: $0 [1|2]"
  exit 1
fi

echo "Sending requestApproval..."
response=$(curl -sS --location "$base_url/api/v1/requestApproval" \
  --header 'Content-Type: application/json' \
  --data-raw "$body")

echo "$response"

error=$(echo "$response" | grep -o '"error"[^,}]*' | grep -o '"[^"]*"$' | tr -d '"')

if [ -n "$error" ]; then
  echo "requestApproval returned an error, stopping: $error"
  exit 1
fi

job_id=$(echo "$response" | grep -o '"jobId"[^,}]*' | grep -o '"[^"]*"$' | tr -d '"')

if [ -z "$job_id" ]; then
  echo "Could not find jobId in response, stopping."
  exit 1
fi

echo "Got jobId: $job_id"
echo "Polling every ${poll_interval}s, timeout ${poll_timeout}s..."

elapsed=0
while [ "$elapsed" -lt "$poll_timeout" ]; do
  sleep "$poll_interval"
  elapsed=$((elapsed + poll_interval))

  status_response=$(curl -sS "$base_url/api/v1/requestStatus?jobId=$job_id")
  echo "[${elapsed}s] $status_response"

  echo "$status_response" | grep -q '"status":"pending"' || {
    echo "Job finished."
    exit 0
  }
done

echo "Timed out after ${poll_timeout}s."
exit 1
