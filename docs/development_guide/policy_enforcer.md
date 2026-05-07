# Policy Enforcer — Technical Documentation

## Table of Contents

- [1. Overview](#1-overview)
- [2. Layered eFLINT Design](#2-layered-eflint-design)
  - [2.1 The Three Layers](#21-the-three-layers)
  - [2.2 Where Each Layer Lives](#22-where-each-layer-lives)
  - [2.3 Query Facts and the `submit-data-request` Act](#23-query-facts-and-the-submit-data-request-act)
- [3. Request Approval Flow](#3-request-approval-flow)
  - [3.1 High-Level Pipeline](#31-high-level-pipeline)
  - [3.2 Internal Flow Inside the Policy Enforcer](#32-internal-flow-inside-the-policy-enforcer)
  - [3.3 Agreement Phrase Providers](#33-agreement-phrase-providers)
  - [3.4 Layered Reasoner Evaluation](#34-layered-reasoner-evaluation)
  - [3.5 Policy Update Flow](#35-policy-update-flow)
- [4. RabbitMQ Messaging](#4-rabbitmq-messaging)
  - [4.1 Consumed Messages](#41-consumed-messages)
  - [4.2 Published Messages](#42-published-messages)
- [5. HTTP API Endpoints](#5-http-api-endpoints)
  - [5.1 Health](#51-health)
  - [5.2 Policy Enforcer API (Policy-Engineer Testing)](#52-policy-enforcer-api-policy-engineer-testing)
  - [5.3 eFLINT Instance Management API](#53-eflint-instance-management-api)
  - [5.4 eFLINT Pool Management API](#54-eflint-pool-management-api)
  - [5.5 eFLINT State Management API (POC)](#55-eflint-state-management-api-poc)
- [6. Software Architecture](#6-software-architecture)
  - [6.1 Package Structure](#61-package-structure)
  - [6.2 Key Types](#62-key-types)
  - [6.3 Agreement Phrase Provider Pattern](#63-agreement-phrase-provider-pattern)
  - [6.4 Reasoner Abstraction](#64-reasoner-abstraction)
  - [6.5 Repository Pattern](#65-repository-pattern)
- [7. eFLINT Instance Pool](#7-eflint-instance-pool)
  - [7.1 Architecture](#71-architecture)
  - [7.2 Pool Lifecycle](#72-pool-lifecycle)
  - [7.3 Instance States](#73-instance-states)
  - [7.4 Health Monitoring](#74-health-monitoring)
  - [7.5 State Management (POC)](#75-state-management-poc)
  - [7.6 eFLINT Commands](#76-eflint-commands)
- [8. Configuration](#8-configuration)
  - [8.1 Local Configuration](#81-local-configuration)
  - [8.2 Production Configuration](#82-production-configuration)
  - [8.3 etcd Key Paths](#83-etcd-key-paths)
  - [8.4 Provider Validation Configs](#84-provider-validation-configs)
- [9. Key Data Structures (Protobuf)](#9-key-data-structures-protobuf)
- [10. Deployment](#10-deployment)
- [11. Diagrams](#11-diagrams)

---

## 1. Overview

The **Policy Enforcer** is a core DYNAMOS microservice that decides whether a data analyst's `RequestApproval` is permitted by each requested data steward. It sits between the API Gateway and the Orchestrator in the request approval pipeline.

The Policy Enforcer is built around a **layered eFLINT specification**: every approval decision is the outcome of a single eFLINT evaluation that combines a stable Layer-1 interface policy, Layer-2 agreement rules and per-steward agreements, and Layer-3 request facts derived from the inbound message. See [docs/diagrams/eflint-policy-layers.md](../diagrams/eflint-policy-layers.md) for the full design rationale.

The Policy Enforcer supports **two agreement-storage formats**, both feeding the same canonical layered evaluation:

| Format     | Description                                                                            | Configuration Source                      |
| ---------- | -------------------------------------------------------------------------------------- | ----------------------------------------- |
| **Legacy** | JSON agreement documents stored in etcd; translated into Layer-2 phrases on the fly    | `/policyEnforcer/agreements/{steward}`    |
| **eFLINT** | Layer-2 eFLINT phrase blocks stored verbatim in etcd                                   | `/policyEnforcer/eflintModels/{steward}`  |

The format used for each steward is determined at runtime by consulting that steward's `ProviderValidationConfig` in etcd, so a single request can mix legacy and eFLINT-format stewards without changing the evaluation path.

### Key Capabilities

- **Single-pass layered eFLINT evaluation** for the entire `RequestApproval` (no per-steward goroutines)
- **Agreement-phrase provider abstraction** so legacy JSON and eFLINT-format stewards share the same evaluation pipeline
- **eFLINT instance pooling** for stateless throughput (each evaluation gets a clean Layer-1 baseline)
- **`submit-data-request` Act firing** for every permitted steward, materialising the audit-trail fact and `obligated-log` duty on the execution graph
- **Repository pattern** for abstracting etcd storage access
- **Health monitoring** with automatic unhealthy instance replacement

---

## 2. Layered eFLINT Design

### 2.1 The Three Layers

| Layer | Name                  | Purpose                                                                                                  | Lifetime         |
| ----- | --------------------- | -------------------------------------------------------------------------------------------------------- | ---------------- |
| **1** | Interface policy      | Stable schema: base fact-types, query-fact declarations, the `submit-data-request` Act and `obligated-log` Duty. **No derivation rules.** | Versioned with the enforcer binary |
| **2** | Agreement rules + per-steward agreements | Shared `Extend Fact` rules + per-steward base facts (`+agreement`, `+steward-supports-*`, `+has-relation`, `+relation-allows-*`) | Replaceable at runtime via etcd |
| **3** | Request facts         | Per-evaluation `+requester(...)` and `+requested-steward(...)` facts derived from the `RequestApproval`  | Lives only for one evaluation |

A single evaluation pushes all three layers onto one eFLINT instance, queries the Layer-1 query facts (`permitted-request`, `permitted-at-steward`, `valid-archetype`, `valid-compute-provider`), and fires `submit-data-request(R, S)` for each permitted steward. The pool then restarts the eFLINT process, so no state leaks between requests.

### 2.2 Where Each Layer Lives

| Artefact                                     | Storage                                                       | Loaded By                                                |
| -------------------------------------------- | ------------------------------------------------------------- | -------------------------------------------------------- |
| `01_interface_policy.eflint` (Layer 1)       | **Embedded** in the policy-enforcer binary at `eflint/01_interface_policy.eflint` | `eflint.InstancePool` (boot model for every pool entry) |
| `02_agreement_rules.eflint` (Layer 2 shared) | etcd: `/policyEnforcer/eflintRules/shared`                    | `EflintRulesRepository` → pushed at evaluation time     |
| `<steward>.eflint` (Layer 2 per-steward)     | etcd: `/policyEnforcer/eflintModels/{steward}`                | `EflintAgreementPhraseProvider`                          |
| Legacy JSON agreement (translated to Layer 2)| etcd: `/policyEnforcer/agreements/{steward}`                  | `LegacyAgreementPhraseProvider` → `TranslateLegacyAgreement` |
| Layer-3 request facts                        | Built in-memory from `pb.RequestApproval`                     | `EflintReasoner.EvaluateRequestApproval`                 |

The orchestrator's `etcd_config.go` populates the etcd entries on startup by routing files from `configuration/eflint-models/` based on filename: `01_interface_policy.eflint` is loaded for reference at `/policyEnforcer/eflintLayer1/interface`, `02_agreement_rules.eflint` is loaded at `/policyEnforcer/eflintRules/shared`, and any other `<name>.eflint` file lands at `/policyEnforcer/eflintModels/<name>`.

### 2.3 Query Facts and the `submit-data-request` Act

Layer 1 declares the query facts the enforcer consumes:

- `permitted-request(requester)` — top-level decision; at least one steward must permit the request.
- `permitted-at-steward(requester, steward)` — true when the requester has an admissible relation with the steward.
- `valid-archetype(requester, steward, archetype)` and `valid-compute-provider(requester, steward, compute-provider)` — the matched archetype and compute-provider sets.

Layer 1 also declares the `submit-data-request(requester, data-steward)` Act, the `data-request(...)` fact it creates, and the `obligated-log(...)` Duty derived from each `data-request`. The reasoner fires this Act for every permitted steward so the audit trail is recorded on the execution graph.

This implementation covers the **Request approval scenario plus `submit-data-request` firing** as described in the layered design. Continuation/ongoing-authorisation phrases (`permitted-continuation`, agreement renewals, etc.) are intentionally out of scope for the current code path.

---

## 3. Request Approval Flow

### 3.1 High-Level Pipeline

The Policy Enforcer is step 3 in the wider DYNAMOS request approval pipeline:

```
1. [Data Analyst] HTTP POST /api/v1/requestApproval
                                          │
                                          ▼
2. [API Gateway] Publishes requestApproval → RabbitMQ (policyEnforcer-in)
                                          │
                                          ▼
3. [Policy Enforcer]                     ◀── THIS SERVICE
   │  Builds Layer 2 + Layer 3, runs one layered eFLINT evaluation
   │  Fires submit-data-request for permitted stewards
   │  Publishes validationResponse → RabbitMQ (orchestrator-in)
                                          │
                                          ▼
4. [Orchestrator]
   │  Picks archetype, dispatches compositionRequests to agents
   │  Publishes requestApprovalResponse → RabbitMQ (api-gateway-in)
                                          │
                                          ▼
5. [API Gateway] Sends data requests to authorised agents
                                          │
                                          ▼
6. [Data Analyst] Receives aggregated response
```

### 3.2 Internal Flow Inside the Policy Enforcer

```
┌──────────────────────────────────────────────────────────────────────┐
│                  POLICY ENFORCER — internal flow                      │
└──────────────────────────────────────────────────────────────────────┘

1. [consume.go] handleIncomingMessages()
   │  Routes message by type: "requestApproval"
   ▼
2. [consume.go] handleRequestApproval()
   │  Unmarshals pb.RequestApproval
   │  Calls ValidationService.ValidateRequest()
   ▼
3. [service/validation_service.go] ValidateRequest()
   │
   ├── Phase 1 — collectStewardPhrases()
   │      For each requested steward:
   │        resolveProvider(steward)             (etcd /policyEnforcer/configs/{steward})
   │          ├── ValidationStrategyEflint  → EflintAgreementPhraseProvider
   │          └── otherwise                 → LegacyAgreementPhraseProvider
   │        provider.GetLayer2Phrases(steward)
   │          ├── eFLINT format  → load /policyEnforcer/eflintModels/{steward} verbatim
   │          └── legacy JSON    → load /policyEnforcer/agreements/{steward}
   │                                and run TranslateLegacyAgreement(...)
   │      Stewards without an agreement land in InvalidDataproviders.
   │
   ├── Phase 2 — loadSharedRules()
   │      Read /policyEnforcer/eflintRules/shared via EflintRulesRepository.
   │
   ├── Phase 3 — single layered evaluation (only if at least one steward
   │              has phrases):
   │      reasoner.EvaluateRequestApproval(RequestApprovalParams{
   │          Requester, Stewards, SharedRules, StewardPhrases,
   │      })
   │      → see §3.4 for what happens inside the reasoner.
   │
   └── applyEvaluation()
          For each steward in eval.PerSteward:
            decision.Permitted = true
              → ValidDataproviders[steward] = {Archetypes, ComputeProviders}
            decision.Permitted = false
              → InvalidDataproviders ← steward (with Reason for diagnostics)
   ▼
4. [consume.go] Sends ValidationResponse via RabbitMQ → orchestrator-in.
```

### 3.3 Agreement Phrase Providers

Both supported agreement-storage formats implement a single `AgreementPhraseProvider` interface so the layered evaluation has one canonical input shape per steward:

```go
type AgreementPhraseProvider interface {
    Name() string
    GetLayer2Phrases(steward string) (string, bool, error)
    ValidateAndPersist(ctx context.Context, steward string, payload []byte) error
}
```

| Provider                          | File                                    | `GetLayer2Phrases` source                                                  |
| --------------------------------- | --------------------------------------- | -------------------------------------------------------------------------- |
| `EflintAgreementPhraseProvider`   | `service/eflint_validation_strategy.go` | Returns the stored eFLINT phrase block from `/policyEnforcer/eflintModels/{steward}` verbatim. |
| `LegacyAgreementPhraseProvider`   | `service/legacy_validation_strategy.go` | Loads the JSON agreement from `/policyEnforcer/agreements/{steward}` and runs `TranslateLegacyAgreement` to produce equivalent Layer-2 phrases on the fly. |

`TranslateLegacyAgreement` (in `service/legacy_translator.go`) emits exactly the per-steward facts the Layer-2 shared rules consume (`+data-steward`, `+agreement`, `+steward-supports-archetype`, `+steward-supports-compute-provider`, `+has-relation`, `+relation-allows-*`), with consistent identifier quoting (`quoteEflintIdentifier`) so e.g. email-style requesters round-trip safely through eFLINT's parser.

`ValidateAndPersist` is used by the policy-update path: legacy providers parse the JSON and write it back to etcd; eFLINT providers ask the reasoner to load the candidate phrases on a pool instance (so syntax/reference errors surface as a parse error) and then persist the text.

### 3.4 Layered Reasoner Evaluation

The single `Reasoner.EvaluateRequestApproval` call in `reasoner/eflint_layered.go` performs the full layered evaluation on **one pool entry**:

```
EflintReasoner.EvaluateRequestApproval(ctx, params)
  │
  ├── 1. Acquire one pool entry. Each entry boots with the Layer-1
  │      interface policy already loaded.
  │
  ├── 2. SendPhrases(SharedRules)            ← Layer 2 shared rules
  │
  ├── 3. For each steward S with phrases:
  │        SendPhrases(StewardPhrases[S])    ← Layer 2 per-steward facts
  │
  ├── 4. Build & SendPhrases the Layer-3 facts:
  │        +requester("...").
  │        +requested-steward("...")...
  │
  ├── 5. ?Holds(permitted-request(R)).        → result.PermittedRequest
  │
  ├── 6. For each requested steward S:
  │        ?Holds(permitted-at-steward(R, S)).
  │        If true:
  │          fetchFacts() once and intersect
  │             relation-allows-archetype(R, S, A)  ∩  steward-supports-archetype(S, A)
  │             relation-allows-compute-provider(R, S, P) ∩ steward-supports-compute-provider(S, P)
  │          Fire submit-data-request(R, S)  → emits data-request + obligated-log
  │        If false:
  │          decision.Reason = "permitted-at-steward did not hold"
  │
  └── 7. Release the pool entry → the pool restarts the eFLINT process so the
         next evaluation starts from a clean Layer-1 baseline.
```

The `data-request` and `obligated-log` materialised by step 6 live for the duration of a single pool entry's lifetime; they are observable via the `eflint/state` HTTP API while the evaluation is in flight, and are flushed when the pool restarts the entry on release.

### 3.5 Policy Update Flow

The Policy Enforcer also handles `policyUpdate` messages, which are sent by the Orchestrator when an agreement changes and active jobs need re-validation:

```
1. [consume.go] handleIncomingMessages()  → routes "policyUpdate"
   │
   ▼
2. [consume.go] handlePolicyUpdate()
   │  Converts pb.PolicyUpdate → pb.RequestApproval (reuses the layered
   │  evaluation pipeline)
   │  Calls ValidationService.ValidateRequest()
   │  Embeds the resulting ValidationResponse back into the PolicyUpdate
   │  Sets DestinationQueue = "orchestrator-in"
   │  Publishes the PolicyUpdate to RabbitMQ.
```

---

## 4. RabbitMQ Messaging

### 4.1 Consumed Messages

The Policy Enforcer consumes from the **`policyEnforcer-in`** queue.

| Message Type        | Protobuf Type        | Description                                                  |
| ------------------- | -------------------- | ------------------------------------------------------------ |
| `"requestApproval"` | `pb.RequestApproval` | Request from API Gateway to validate data access             |
| `"policyUpdate"`    | `pb.PolicyUpdate`    | Request from Orchestrator to re-validate after policy change |

### 4.2 Published Messages

| Message Type           | Protobuf Type           | Destination Queue             | Description                           |
| ---------------------- | ----------------------- | ----------------------------- | ------------------------------------- |
| `"validationResponse"` | `pb.ValidationResponse` | `orchestrator-in` (hardcoded) | Result of request approval validation |
| `"policyUpdate"`       | `pb.PolicyUpdate`       | `orchestrator-in` (hardcoded) | Result of policy update re-validation |

```
                policyEnforcer-in                 orchestrator-in
                ┌──────────────┐                  ┌──────────────┐
 API Gateway ──►│              │                  │              │──► Orchestrator
                │   Policy     │ ───────────────► │              │
 Orchestrator ─►│   Enforcer   │                  │              │
  (policyUpdate)└──────────────┘                  └──────────────┘
```

---

## 5. HTTP API Endpoints

The request-approval flow is RabbitMQ-driven; the HTTP API exposes the policy-engineer testing surface plus the eFLINT instance/pool/state and health endpoints. All endpoints are prefixed with `/api/v1`. The full OpenAPI 3.0.3 specification is at [docs/openapi/policy-enforcer-openapi.yaml](../openapi/policy-enforcer-openapi.yaml).

The HTTP port is **8082** (local) or **8080** (production).

### 5.1 Health

| Method | Path             | Description                                       |
| ------ | ---------------- | ------------------------------------------------- |
| `GET`  | `/api/v1/health` | Health check endpoint (used by Kubernetes probes) |

### 5.2 Policy Enforcer API (Policy-Engineer Testing)

These endpoints let a policy engineer exercise a steward's Layer-2 agreement without going through the RabbitMQ pipeline. **Both endpoints share the production code path** — the same `ValidationService`, the same `EflintReasoner`, the same pool — so what you test here is what the RabbitMQ flow runs at request time. The pool restarts each instance on release, so neither endpoint has persistent side effects.

| Method | Path                                      | Description                                                                              |
| ------ | ----------------------------------------- | ---------------------------------------------------------------------------------------- |
| `GET`  | `/api/v1/policy-enforcer/allowed-clauses` | Introspect a steward's Layer-2 facts (steward-supports-* and relation-allows-*).         |
| `POST` | `/api/v1/policy-enforcer/validate`        | Simulate a `RequestApproval`; returns the same `pb.ValidationResponse` as the RabbitMQ flow. |

#### `GET /api/v1/policy-enforcer/allowed-clauses`

| Query parameter | Required | Description                                                       |
| --------------- | -------- | ----------------------------------------------------------------- |
| `steward`       | yes      | Data steward to introspect (e.g. `VU`).                           |
| `requester`     | no       | When set, narrows the response to that requester's relation only. |

The handler resolves the steward's `AgreementPhraseProvider` (legacy or eFLINT, per `/policyEnforcer/configs/{steward}`), loads its Layer-2 phrases, pushes them onto a clean pool entry together with the Layer-2 shared rules, and returns the resulting `steward-supports-*` and `relation-allows-*` facts. **No Layer 3 is pushed and no Acts are fired.**

```
GET /api/v1/policy-enforcer/allowed-clauses?steward=VU
```

```json
{
  "steward": "VU",
  "supported_archetypes": ["computeToData", "dataThroughTtp"],
  "supported_compute_providers": ["SURF"],
  "relations": [
    {
      "requester": "jorrit.stutterheim@cloudnation.nl",
      "request_types": ["sqlDataRequest", "genericRequest"],
      "datasets": ["wageGap"],
      "archetypes": ["computeToData", "dataThroughTtp"],
      "compute_providers": ["SURF"]
    }
  ]
}
```

`404` is returned when the steward has no agreement (no entry under `/policyEnforcer/eflintModels/{steward}` or `/policyEnforcer/agreements/{steward}`, depending on the configured format).

#### `POST /api/v1/policy-enforcer/validate`

Body:

```json
{
  "user": { "id": "1", "user_name": "jorrit.stutterheim@cloudnation.nl" },
  "data_providers": ["VU", "UVA", "RUG"]
}
```

The body mirrors the relevant fields of `pb.RequestApproval`. The handler maps it to a `pb.RequestApproval` and runs `ValidationService.ValidateRequest` — exactly the function `consume.go` calls when a real RabbitMQ message arrives. The response is the resulting `pb.ValidationResponse` encoded as JSON, identical in shape to what would land on `orchestrator-in`:

```json
{
  "type": "validationResponse",
  "request_type": "requestApproval",
  "request_approved": true,
  "user": { "id": "1", "user_name": "jorrit.stutterheim@cloudnation.nl" },
  "valid_dataproviders": {
    "VU":  { "archetypes": ["computeToData"], "compute_providers": ["SURF"] },
    "UVA": { "archetypes": ["computeToData", "dataThroughTtp"], "compute_providers": ["SURF"] }
  },
  "invalid_dataproviders": ["RUG"],
  "valid_archetypes": { "user_name": "jorrit.stutterheim@cloudnation.nl", "archetypes": { ... } },
  "auth": { "access_token": "...", "refresh_token": "..." }
}
```

Because the eFLINT pool restarts each instance on release, the `submit-data-request` Acts fired for permitted stewards do **not** persist beyond the simulated evaluation.

### 5.3 eFLINT Instance Management API

These endpoints manage individual eFLINT server instances. They are primarily used for debugging and development.

| Method | Path                     | Description                                                        |
| ------ | ------------------------ | ------------------------------------------------------------------ |
| `GET`  | `/api/v1/eflint/status`  | Get instance status (optional `?instance_id=X` for pool instances) |
| `POST` | `/api/v1/eflint/start`   | Start an eFLINT instance with a model (supports `force` flag)      |
| `POST` | `/api/v1/eflint/stop`    | Stop a running instance                                            |
| `POST` | `/api/v1/eflint/restart` | Restart an instance with its current model                         |
| `POST` | `/api/v1/eflint/command` | Send a raw command to the eFLINT server                            |

### 5.4 eFLINT Pool Management API

These endpoints manage the eFLINT instance pool used for layered evaluation.

| Method | Path                                 | Description                                               |
| ------ | ------------------------------------ | --------------------------------------------------------- |
| `GET`  | `/api/v1/eflint/instances`           | List all pool instances with their state                  |
| `GET`  | `/api/v1/eflint/instances/pool-size` | Get pool size statistics (total, idle, in_use, unhealthy) |
| `PUT`  | `/api/v1/eflint/instances/pool-size` | Dynamically resize the pool                               |

### 5.5 eFLINT State Management API (POC)

These endpoints provide state persistence and checkpoint/restore capabilities. They are a **proof-of-concept** for future stateful eFLINT use cases and are not used by the request-approval flow.

| Method   | Path                                      | Description                        |
| -------- | ----------------------------------------- | ---------------------------------- |
| `GET`    | `/api/v1/eflint/state`                    | Get current execution graph state  |
| `POST`   | `/api/v1/eflint/state/export`             | Export state for persistence       |
| `POST`   | `/api/v1/eflint/state/import`             | Import a previously exported state |
| `POST`   | `/api/v1/eflint/state/checkpoint`         | Create a named checkpoint          |
| `POST`   | `/api/v1/eflint/state/checkpoint/restore` | Restore a named checkpoint         |
| `GET`    | `/api/v1/eflint/state/checkpoints`        | List all checkpoints               |
| `DELETE` | `/api/v1/eflint/state/checkpoint/{name}`  | Delete a checkpoint                |

---

## 6. Software Architecture

### 6.1 Package Structure

```
go/cmd/policy-enforcer/
├── main.go                          # Application bootstrap and dependency wiring
├── consume.go                       # RabbitMQ message handlers
├── routes.go                        # HTTP route registration (health + eFLINT only)
├── config_local.go                  # Local development configuration (build tag: local)
├── config_prod.go                   # Production configuration (build tag: !local)
│
├── service/                         # Business logic layer
│   ├── validation_service.go            # Single-pass layered orchestration
│   ├── validation_strategy.go           # AgreementPhraseProvider interface
│   ├── legacy_validation_strategy.go    # Legacy JSON agreement provider
│   ├── eflint_validation_strategy.go    # eFLINT-format agreement provider
│   ├── legacy_translator.go             # JSON → Layer-2 eFLINT phrase translator
│   ├── response_sender.go               # Abstracts RabbitMQ response sending
│   └── auth_token_generator.go          # Auth token generation interface
│
├── repository/                      # Data access layer
│   ├── agreement_repository.go              # Legacy JSON agreement repository
│   ├── etcd_agreement_repository.go         # etcd implementation
│   ├── eflint_model_repository.go           # eFLINT per-steward model repository
│   ├── eflint_rules_repository.go           # Layer-2 shared rules repository
│   ├── eflint_state_repository.go           # POC: eFLINT state repository
│   └── provider_config_repository.go        # Provider-format config repository
│
├── eflint/                          # eFLINT server management
│   ├── instance.go                  # Single eFLINT process wrapper
│   ├── manager.go                   # Instance lifecycle (start/stop/command)
│   ├── pool.go                      # Instance pool (acquire/release/health)
│   ├── state_manager.go             # State export/import/checkpoint
│   ├── instance_api.go              # HTTP handlers for instance management
│   ├── state_api.go                 # HTTP handlers for state management
│   └── 01_interface_policy.eflint   # Layer-1 boot model (embedded into the binary)
│
├── reasoner/                        # Reasoner abstraction
│   ├── reasoner.go                  # Reasoner interface + RequestApproval / IntrospectStewardClauses types
│   ├── eflint_reasoner.go           # eFLINT reasoner (pool wiring + ValidateAndPersistModel)
│   └── eflint_layered.go            # EvaluateRequestApproval, IntrospectStewardClauses + helpers
│
└── policyenforcerhttp/              # HTTP handlers for the policy-engineer testing endpoints
    └── http_handler.go              # /policy-enforcer/allowed-clauses + /policy-enforcer/validate
```

### 6.2 Key Types

#### `Application` (`main.go`)

```go
type Application struct {
    logger            *zap.Logger
    etcdClient        *clientv3.Client
    grpcConn          *grpc.ClientConn
    rabbitMQClient    pb.RabbitMQClient
    validationService *service.ValidationService
    responseSender    service.ResponseSender
}
```

`main.go` wires everything together: the `eflint.InstancePool` (booted with the embedded Layer-1 policy), the four etcd repositories, the two `AgreementPhraseProvider` implementations, the `EflintReasoner`, and the `ValidationService`.

#### `ValidationService` (`service/validation_service.go`)

```go
type ValidationService struct {
    providerConfigRepo repository.ProviderConfigRepository
    rulesRepo          repository.EflintRulesRepository
    legacyProvider     AgreementPhraseProvider
    eflintProvider     AgreementPhraseProvider // optional
    reasoner           reasoner.Reasoner
    authGenerator      AuthTokenGenerator
    logger             *zap.Logger
}
```

`ValidateRequest` is the single entrypoint used from `consume.go`. It collects per-steward Layer-2 phrases, loads the shared rules, runs **one** `reasoner.EvaluateRequestApproval` for the entire `RequestApproval`, and maps the per-steward decisions back into the existing `pb.ValidationResponse` wire format.

### 6.3 Agreement Phrase Provider Pattern

The Policy Enforcer uses a small **provider pattern** to support multiple agreement-storage formats while keeping a single canonical evaluation path. Each provider produces the *same shape of input* — a Layer-2 eFLINT phrase block — for its native storage format.

#### Interface

```go
type AgreementPhraseProvider interface {
    Name() string
    GetLayer2Phrases(steward string) (string, bool, error)
    ValidateAndPersist(ctx context.Context, steward string, payload []byte) error
}
```

#### Implementations

| Provider                          | File                                    | Source                                                                     |
| --------------------------------- | --------------------------------------- | -------------------------------------------------------------------------- |
| `LegacyAgreementPhraseProvider`   | `service/legacy_validation_strategy.go` | etcd JSON agreements + `TranslateLegacyAgreement`                          |
| `EflintAgreementPhraseProvider`   | `service/eflint_validation_strategy.go` | etcd-stored Layer-2 eFLINT phrases (verbatim)                              |

#### Resolution

Each steward's provider is resolved per evaluation by `ValidationService.resolveProvider`:

```go
func (s *ValidationService) resolveProvider(steward string) AgreementPhraseProvider {
    config, found, err := s.providerConfigRepo.GetProviderConfig(steward)
    // etcd key: /policyEnforcer/configs/{steward}

    if found && config.ValidationStrategy == api.ValidationStrategyEflint && s.eflintProvider != nil {
        return s.eflintProvider
    }
    return s.legacyProvider // default fallback
}
```

This means a single request like `dataProviders = ["VU", "UVA", "RUG"]` can mix formats:

```
VU  → ValidationStrategyEflint  → EflintAgreementPhraseProvider
UVA → ValidationStrategyEflint  → EflintAgreementPhraseProvider
RUG → ValidationStrategyLegacy  → LegacyAgreementPhraseProvider

All three feed one EvaluateRequestApproval call.
```

### 6.4 Reasoner Abstraction

The `Reasoner` interface (`reasoner/reasoner.go`) is intentionally narrow — every approval decision is the result of a single `EvaluateRequestApproval` call. `IntrospectStewardClauses` is the read-only sibling that backs `GET /policy-enforcer/allowed-clauses`:

```go
type Reasoner interface {
    EvaluateRequestApproval(ctx context.Context, params RequestApprovalParams) (*RequestApprovalResult, error)
    IntrospectStewardClauses(ctx context.Context, params IntrospectStewardClausesParams) (*StewardClauses, error)
    IsRunning() bool
    ValidateAndPersistModel(ctx context.Context, organization string, modelText string) error
    Name() string
}

type RequestApprovalParams struct {
    Requester      string
    Stewards       []string
    SharedRules    string
    StewardPhrases map[string]string // missing entries imply no agreement
}

type StewardDecision struct {
    Permitted        bool
    Archetypes       []string
    ComputeProviders []string
    Reason           string
}

type RequestApprovalResult struct {
    PermittedRequest bool
    PerSteward       map[string]StewardDecision
}
```

The single implementation, `EflintReasoner`, is split across two files:

- `reasoner/eflint_reasoner.go` — pool wiring and `ValidateAndPersistModel` (used by the policy-update path).
- `reasoner/eflint_layered.go` — `EvaluateRequestApproval`, `IntrospectStewardClauses`, and the helpers they share (`fetchFacts`, `quoteEflintLiteral`, `buildLayer3Phrases`, `queryHolds`, `filterBinary`, `filterTernary`, `intersectPreservingOrder`, `requestersWithRelationTo`).

A separate optional `StateManager` interface is provided for the eFLINT state-persistence POC (see §5.4). It is independent of the request-approval flow.

### 6.5 Repository Pattern

Repositories abstract the etcd data-access layer behind clean interfaces:

| Repository                 | Interface                                                        | etcd Key Pattern                              | Used By                                  |
| -------------------------- | ---------------------------------------------------------------- | --------------------------------------------- | ---------------------------------------- |
| `AgreementRepository`      | `GetAgreement(steward) → *api.Agreement`                         | `/policyEnforcer/agreements/{steward}`        | `LegacyAgreementPhraseProvider`          |
| `EflintModelRepository`    | `GetEflintModel(name) → string`                                  | `/policyEnforcer/eflintModels/{name}`         | `EflintAgreementPhraseProvider`, `EflintReasoner.ValidateAndPersistModel` |
| `EflintRulesRepository`    | `GetSharedAgreementRules() → string`                             | `/policyEnforcer/eflintRules/shared`          | `ValidationService.loadSharedRules`      |
| `ProviderConfigRepository` | `GetProviderConfig(provider) → *api.ProviderValidationConfig`    | `/policyEnforcer/configs/{provider}`          | `ValidationService.resolveProvider`      |
| `EflintStateRepository`    | `GetEflintState() / SaveEflintState()`                           | `/policyEnforcer/eflint-states/{provider}`    | State management (POC)                   |

All repositories have an `Etcd*Repository` implementation that reads from / writes to etcd.

---

## 7. eFLINT Instance Pool

### 7.1 Architecture

The eFLINT instance pool manages a set of pre-started eFLINT server processes. Each pool entry is an independent eFLINT server running on a unique TCP port, **bootstrapped with the Layer-1 interface policy** (`eflint/01_interface_policy.eflint`, embedded in the binary). When a layered evaluation arrives, an idle entry is acquired, the Layer-2 + Layer-3 phrases are pushed onto it, queries are executed, and on release the entry is restarted (process-level isolation) back to the Layer-1 baseline.

```
┌──────────────────────────────────────────────────┐
│                  InstancePool                     │
│                                                   │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐         │
│  │ Instance │  │ Instance │  │ Instance │  ...    │
│  │ (idle)   │  │ (in_use) │  │ (idle)   │         │
│  │ port:9001│  │ port:9002│  │ port:9003│         │
│  │  Manager │  │  Manager │  │  Manager │         │
│  │ StateMgr │  │ StateMgr │  │ StateMgr │         │
│  └──────────┘  └──────────┘  └──────────┘         │
│                                                   │
│  Health Monitor (background goroutine)            │
│  ├── Checks health every 10s                      │
│  ├── Replaces unhealthy instances                 │
│  └── Enforces target pool size                    │
└──────────────────────────────────────────────────┘
```

### 7.2 Pool Lifecycle

```
Startup                     Layered Evaluation               After Evaluation
───────                     ──────────────────              ────────────────
1. Create pool with         1. Acquire() → idle entry       1. Restart eFLINT
   target size (3)              (Layer-1 already loaded)        process with the
2. Start N eFLINT           2. Mark as "in_use"                 Layer-1 baseline
   server processes         3. SendPhrases(Layer-2 shared       (process-level
   in parallel                 + per-steward + Layer-3)         isolation)
3. Bootstrap each with      4. ?Holds(...) queries          2. If restart fails
   01_interface_policy      5. fetchFacts() + intersect        → mark "unhealthy"
4. All start as "idle"      6. Fire submit-data-request        (health monitor
5. Start health monitor                                        replaces it)
                                                            3. Mark "idle" and
                                                               return to pool
                                                               (async goroutine)
```

### 7.3 Instance States

Each pool entry (`PoolEntry`) can be in one of three states:

| State       | Description                                                       |
| ----------- | ----------------------------------------------------------------- |
| `idle`      | Available for acquisition. eFLINT process holds the Layer-1 baseline only. |
| `in_use`    | Currently acquired by a layered evaluation.                       |
| `unhealthy` | Process crashed or health check failed. Will be replaced.         |

### 7.4 Health Monitoring

A background goroutine runs every `HealthCheckInterval` (default: 10 seconds):

1. **Check health**: verifies each instance's process is alive.
2. **Replace unhealthy**: stops and replaces any unhealthy instances.
3. **Enforce target size**: spins up new instances if below target, stops excess idle instances if above.
4. **Log statistics**: reports pool utilisation metrics.

### 7.5 State Management (POC)

Each `PoolEntry` has a `StateManager` that handles:

- **Revert**: sends `revert` command to node 1 (initial empty state).
- **Verify empty state**: checks that `target_contents` is empty after revert.
- **Export/Import**: serialises/deserialises the eFLINT execution graph (used by the state HTTP API).
- **Checkpoints**: named snapshots of eFLINT state that can be restored.

The state manager also handles **eFLINT server bugs** by transforming the execution graph JSON during import (renaming `"program"` → `"label"`, stripping `"Type extension"` lines).

### 7.6 eFLINT Commands

The Policy Enforcer communicates with eFLINT server instances over TCP using JSON commands:

| Command         | Purpose                                | Example                                      |
| --------------- | -------------------------------------- | -------------------------------------------- |
| `facts`         | Get all current facts                  | `{"command": "facts"}`                       |
| `phrases`       | Load eFLINT phrases (used for Layer 2 + Layer 3)                  | `{"command": "phrases", "text": "..."}`      |
| `status`        | Get server status and execution graph  | `{"command": "status"}`                      |
| `create-export` | Export execution graph for persistence | `{"command": "create-export"}`               |
| `load-export`   | Import a previously exported graph     | `{"command": "load-export", "graph": {...}}` |
| `revert`        | Revert to a specific execution node    | `{"command": "revert", "value": 1}`          |

`?Holds(...)` queries are sent as eFLINT phrases via the `phrases` command; their results come back in the response's `query-results` array (`"success"` / `"failed"`).

---

## 8. Configuration

Configuration is managed via **Go build tags** — there are no environment variables. Use `go build -tags local` for local development or the default build for production.

### 8.1 Local Configuration

File: `config_local.go` (build tag: `local`)

| Setting                   | Value                                              |
| ------------------------- | -------------------------------------------------- |
| Service Name              | `"policyEnforcer"`                                 |
| etcd Endpoints            | `http://localhost:30005`                           |
| gRPC Address (sidecar)    | `localhost:50051`                                  |
| HTTP Port                 | `:8082`                                            |
| API Version Prefix        | `/api/v1`                                          |
| eFLINT Server Path        | `"eflint-server"`                                  |
| eFLINT Model Path (boot)  | `eflint/01_interface_policy.eflint` (relative)     |
| eFLINT State Directory    | `eflint-states` (relative)                         |
| eFLINT Connection Timeout | 60 seconds                                         |
| eFLINT Startup Delay      | 3 seconds                                          |
| eFLINT Port Range         | 1025–65535                                         |
| Auto Start eFLINT         | `true`                                             |
| Pool Size                 | 3                                                  |
| Health Check Interval     | 10 seconds                                         |
| Acquire Timeout           | 30 seconds                                         |

### 8.2 Production Configuration

File: `config_prod.go` (build tag: `!local`)

| Setting                   | Value                                                         |
| ------------------------- | ------------------------------------------------------------- |
| Service Name              | `"policyEnforcer"`                                            |
| etcd Endpoints            | Kubernetes cluster endpoints (3 nodes)                        |
| gRPC Address (sidecar)    | `localhost:50051`                                             |
| HTTP Port                 | `:8080`                                                       |
| API Version Prefix        | `/api/v1`                                                     |
| eFLINT Server Path        | `"eflint-server"`                                             |
| eFLINT Model Path (boot)  | `""` (uses embedded `01_interface_policy.eflint`)             |
| eFLINT State Directory    | `/app/eflint-states`                                          |
| eFLINT Connection Timeout | 60 seconds                                                    |
| eFLINT Startup Delay      | 3 seconds                                                     |
| eFLINT Port Range         | 1025–65535                                                    |
| Auto Start eFLINT         | `true`                                                        |
| Pool Size                 | 3                                                             |
| Health Check Interval     | 10 seconds                                                    |
| Acquire Timeout           | 30 seconds                                                    |

### 8.3 etcd Key Paths

| Path                                       | Purpose                              | Data Format                           | Used By                                  |
| ------------------------------------------ | ------------------------------------ | ------------------------------------- | ---------------------------------------- |
| `/policyEnforcer/eflintLayer1/interface`   | Layer-1 interface policy (informational; the binary embeds Layer 1 too) | Plain text (`.eflint`) | Loaded by the orchestrator's `etcd_config.go`; not read at runtime |
| `/policyEnforcer/eflintRules/shared`       | Layer-2 shared agreement rules       | Plain text (`.eflint`)                | `EflintRulesRepository` (`ValidationService.loadSharedRules`) |
| `/policyEnforcer/eflintModels/{steward}`   | Layer-2 per-steward eFLINT phrases   | Plain text (`.eflint`)                | `EflintAgreementPhraseProvider`          |
| `/policyEnforcer/agreements/{steward}`     | Legacy JSON agreement                | JSON (`api.Agreement`)                | `LegacyAgreementPhraseProvider` (translated to Layer-2 on load) |
| `/policyEnforcer/configs/{steward}`        | Per-steward agreement-format config  | JSON (`api.ProviderValidationConfig`) | `ValidationService.resolveProvider`      |
| `/policyEnforcer/eflint-states/{provider}` | Saved eFLINT states                  | JSON                                  | State management (POC)                   |
| `/agents/online/{name}`                    | Online agent status                  | JSON                                  | Orchestrator (not Policy Enforcer)       |
| `/archetypes/{name}`                       | Archetype configurations             | JSON                                  | Orchestrator (not Policy Enforcer)       |

### 8.4 Provider Validation Configs

Each data provider's agreement-storage format is configured in etcd at `/policyEnforcer/configs/{provider}`:

```json
{
  "name": "VU",
  "validationStrategy": "eflint",
  "agreementLocation": "/app/eflint-models/VU.eflint"
}
```

Example configuration set (from `configuration/etcd_launch_files/provider_configs.json`):

| Provider | Strategy | Agreement Location               |
| -------- | -------- | -------------------------------- |
| VU       | `eflint` | `/app/eflint-models/VU.eflint`   |
| UVA      | `eflint` | `/app/eflint-models/UVA.eflint`  |
| RUG      | `legacy` | `/policyEnforcer/agreements/RUG` |

> The `validationStrategy` field name predates the agreement-phrase provider refactor; it now selects which `AgreementPhraseProvider` is used to feed the layered evaluation, not which evaluation algorithm runs.

---

## 9. Key Data Structures (Protobuf)

### `RequestApproval` (API Gateway → Policy Enforcer)

```protobuf
message RequestApproval {
  string type = 1;                    // "requestApproval"
  User user = 2;                      // {id, user_name}
  repeated string data_providers = 3; // ["VU", "UVA", "RUG"]
  string destination_queue = 4;       // "policyEnforcer-in"
  map<string, bool> options = 5;
}
```

The Policy Enforcer treats `User.UserName` as the requester (Layer 3: `+requester("...")`) and `data_providers` as the requested stewards (Layer 3: `+requested-steward("...")`).

### `ValidationResponse` (Policy Enforcer → Orchestrator)

```protobuf
message ValidationResponse {
  string type = 1;                                        // "validationResponse"
  string request_type = 2;                                // e.g., "sqlDataRequest"
  map<string, DataProvider> valid_dataproviders = 3;      // permitted-at-steward = true
  repeated string invalid_dataproviders = 4;              // missing agreement OR permitted-at-steward = false
  Auth auth = 5;                                          // generated only on approval
  User user = 6;
  bool request_approved = 7;                              // = (len(valid_dataproviders) > 0)
  UserArchetypes valid_archetypes = 8;                    // per-steward valid archetypes
  map<string, bool> options = 9;
}
```

The wire format is unchanged from the previous codebase; only the *derivation* of `valid_dataproviders` / `invalid_dataproviders` switched to the layered evaluation.

### `PolicyUpdate` (Orchestrator ↔ Policy Enforcer)

```protobuf
message PolicyUpdate {
  string type = 1;                            // "policyUpdate"
  User user = 2;
  repeated string data_providers = 3;
  RequestMetadata request_metadata = 4;
  ValidationResponse validation_response = 5; // populated on return
}
```

### Supporting Types

```protobuf
message User {
  string id = 1;
  string user_name = 2;
}

message Auth {
  string access_token = 1;
  string refresh_token = 2;
}

message DataProvider {
  repeated string archetypes = 1;
  repeated string compute_providers = 2;
}

message UserArchetypes {
  string user_name = 1;
  map<string, UserAllowedArchetypes> archetypes = 2; // key = steward
}

message RequestMetadata {
  string correlation_id = 1;
  string destination_queue = 2;
  string job_name = 3;
  string return_address = 4;
  string job_id = 5;
  map<string, bytes> traces = 6;
}
```

---

## 10. Deployment

### Kubernetes Deployment

The Policy Enforcer is deployed via the Helm chart at `charts/orchestrator/templates/policyEnforcer.yaml`.

**Key deployment details:**

| Setting         | Value                                                     |
| --------------- | --------------------------------------------------------- |
| Replicas        | 1                                                         |
| Container Port  | 8080                                                      |
| Image           | `{dockerArtifactAccount}/policy-enforcer:{branchNameTag}` |
| Readiness Probe | `GET /api/v1/health` (initial: 5s, period: 10s)           |
| Liveness Probe  | `GET /api/v1/health` (initial: 15s, period: 20s)          |

**Volumes:**

| Volume          | Type             | Mount Path           | Purpose                                 |
| --------------- | ---------------- | -------------------- | --------------------------------------- |
| `eflint-states` | `emptyDir`       | `/app/eflint-states` | Runtime eFLINT state files              |
| `eflint-models` | PVC (`etcd-pvc`) | `/app/eflint-models` | eFLINT layered model files (shared with etcd seed loader) |

**Sidecar:**

The Policy Enforcer runs alongside a **sidecar** container that handles RabbitMQ communication via gRPC:

- Image: `{dockerArtifactAccount}/sidecar:{branchNameTag}`
- Connects to RabbitMQ using credentials from the `rabbit` Kubernetes secret
- Exposes gRPC on `localhost:50051` (pod-internal)

**Ingress:**

- Host: `policy-enforcer.orchestrator.svc.cluster.local`
- Path: `/api/v1` (Prefix)
- Ingress class: `nginx`

### Local redeploy helpers

`configuration/dynamos-helpers.sh` provides shell helpers used during local development. The most relevant for the policy enforcer:

- `redeploy_structurally policy-enforcer` — rebuild the Docker image and restart the pod.
- `fill-rabbit-pvc.sh` — re-seeds etcd with the contents of `configuration/eflint-models/`, `configuration/etcd_launch_files/`, etc., so layered models / shared rules / provider configs are in their expected etcd keys before the pod starts.

After updating `configuration/eflint-models/*.eflint` or `configuration/etcd_launch_files/provider_configs.json`, run `fill-rabbit-pvc.sh` followed by `redeploy_structurally policy-enforcer` (and, if the orchestrator owns the seeding, also `redeploy_structurally orchestrator`) to pick up the changes.

---

## 11. Diagrams

For visual representations of the request approval flow (PlantUML activity diagrams, sequence diagrams, component diagrams, and architecture overview), see:

- **Layered eFLINT design overview:** [docs/diagrams/eflint-policy-layers.md](../diagrams/eflint-policy-layers.md) — the design document this implementation is based on (Layer 1 / Layer 2 / Layer 3, query facts, `submit-data-request` Act).
- **PlantUML source + embedded diagrams:** [docs/diagrams/request_approval_diagrams.md](../diagrams/request_approval_diagrams.md) — full request-approval flow, eFLINT pool lifecycle, software architecture, and deployment diagrams. Some diagrams in this file still use the pre-layered `*ValidationStrategy` naming and will be refreshed in a follow-up.
- **C4 model diagrams:** [docs/diagrams/request_approval_c4.md](../diagrams/request_approval_c4.md) — C4 Levels 1–4.
- **Two-layer overview:** [docs/diagrams/two_layer_policy_enforcer_architecture.md](../diagrams/two_layer_policy_enforcer_architecture.md).
- **Legacy (pre-layered) diagrams:** [docs/diagrams/old_request_approval_diagrams.md](../diagrams/old_request_approval_diagrams.md) and [docs/diagrams/old_request_approval_c4.md](../diagrams/old_request_approval_c4.md).
- **Legacy flow documentation:** [docs/development_guide/legacy_request_approval_flow.md](./legacy_request_approval_flow.md).
