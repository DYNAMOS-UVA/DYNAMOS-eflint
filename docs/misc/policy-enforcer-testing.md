# Policy Enforcer Testing

This guide shows how to test the Policy Enforcer API from WSL.

All commands below should be run inside WSL.

## What You Are Testing

The Policy Enforcer service exposes these main endpoints:

- `GET /api/v1/health`
- `GET /api/v1/policy-enforcer/allowed-clauses`
- `POST /api/v1/policy-enforcer/validate`

The service runs in the `orchestrator` namespace.

## 1. Check That The Service Is Running

```bash
kubectl -n orchestrator get svc policy-enforcer
kubectl -n orchestrator get endpoints policy-enforcer
kubectl -n orchestrator get pods -l app=policy-enforcer
```

You should see:

- a `policy-enforcer` service
- at least one endpoint
- a running pod

## 2. Start Port Forwarding

Open a WSL terminal and run:

```bash
kubectl -n orchestrator port-forward svc/policy-enforcer 18082:8080
```

Keep this terminal open while testing.

## 3. Test The Health Endpoint

Open a second WSL terminal and run:

```bash
curl -i http://127.0.0.1:18082/api/v1/health
```

Expected result:

- HTTP `200 OK`
- JSON response containing `healthy`

## 4. Test Allowed Clauses

```bash
curl -i "http://127.0.0.1:18082/api/v1/policy-enforcer/allowed-clauses?steward=VU&requester=user@example.com"
```

Possible results:

- `200 OK`: endpoint works and the steward model exists
- `500 Internal Server Error` with `eFLINT model not found`: endpoint is reachable, but the required model data is missing
- `503 Service Unavailable`: the reasoner is not running yet

## 5. Test Request Validation

```bash
curl -i \
  -X POST http://127.0.0.1:18082/api/v1/policy-enforcer/validate \
  -H "Content-Type: application/json" \
  -d '{
    "user": {
      "id": "1",
      "user_name": "jorrit.stutterheim@cloudnation.nl"
    },
    "data_providers": ["VU"]
  }'
```

Possible results:

- `200 OK`: the request was evaluated successfully
- `400 Bad Request`: the JSON body is missing required fields
- `500 Internal Server Error`: data is missing, such as the eFLINT model for the steward
- `503 Service Unavailable`: the reasoner is not running

## 6. Stop Port Forwarding

Go back to the first terminal and press `Ctrl+C`.

## Notes

- The correct health URL is `/api/v1/health`, not `/health`.
- The service port is `8080`, so the port-forward command must use `18082:8080`.
- If you get `eFLINT model not found for steward VU`, the HTTP interface is working, but the model has not been loaded into the system yet.
