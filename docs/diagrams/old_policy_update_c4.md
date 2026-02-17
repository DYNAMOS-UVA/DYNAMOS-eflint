# Old/Legacy Policy Update — C4 Diagrams

> **Branch:** `legacy-policy-enforcer`
>
> These C4-style diagrams describe the **old/legacy** policy update flow at different levels of abstraction. See also: [Legacy Policy Update Flow](../development_guide/legacy_policy_update_flow.md)

## Level 1: System Context Diagram

Shows the policy update flow at the highest level — systems and actors.

```mermaid
C4Context
    title System Context — Policy Update Flow

    Person(client, "External Client / Admin", "Updates agreements via HTTP API")

    System(dynamos, "DYNAMOS Platform", "Distributed data exchange system that manages jobs, agreements, and data flows between organizations")

    System_Ext(dataProviderOrgs, "Data Provider Organizations", "Universities/institutions whose agreements are being updated (e.g., VU, UVA)")

    Rel(client, dynamos, "HTTP PUT /api/v1/policyEnforcer/agreements", "Updated agreement JSON")
    Rel(dynamos, dataProviderOrgs, "May send updated CompositionRequests", "RabbitMQ")
```

## Level 2: Container Diagram

Shows the containers (services) involved and their interactions.

```mermaid
C4Container
    title Container Diagram — Policy Update Flow

    Person(client, "External Client", "Updates agreement")

    System_Boundary(dynamos, "DYNAMOS Platform") {
        Container(orchestrator, "Orchestrator", "Go service", "HTTP API, job management, archetype selection, coordinates policy updates")
        Container(policyEnforcer, "Policy Enforcer", "Go service", "Validates user agreements against data providers")
        Container(orchSidecar, "Sidecar (Orchestrator)", "Go service", "gRPC-to-RabbitMQ bridge for the orchestrator")
        Container(peSidecar, "Sidecar (Policy Enforcer)", "Go service", "gRPC-to-RabbitMQ bridge for the policy enforcer")
        ContainerDb(etcd, "etcd", "Key-value store", "Stores agreements, jobs, agents, archetypes configuration")
        ContainerQueue(rabbitmq, "RabbitMQ", "Message broker", "Queues: policyEnforcer-in, orchestrator-in, agent queues")
    }

    Rel(client, orchestrator, "HTTP PUT", "/api/v1/policyEnforcer/agreements")

    Rel(orchestrator, etcd, "Read/Write", "Agreements, jobs, agents, archetypes")
    Rel(orchestrator, orchSidecar, "gRPC", "SendPolicyUpdate, SendCompositionRequest")

    Rel(orchSidecar, rabbitmq, "AMQP", "Publish to policyEnforcer-in")
    Rel(rabbitmq, peSidecar, "AMQP", "Deliver from policyEnforcer-in")

    Rel(peSidecar, policyEnforcer, "gRPC stream", "SideCarMessage")
    Rel(policyEnforcer, etcd, "Read", "Agreements")
    Rel(policyEnforcer, peSidecar, "gRPC", "SendPolicyUpdate (response)")

    Rel(peSidecar, rabbitmq, "AMQP", "Publish to orchestrator-in")
    Rel(rabbitmq, orchSidecar, "AMQP", "Deliver from orchestrator-in")
    Rel(orchSidecar, orchestrator, "gRPC stream", "SideCarMessage")
```

## Level 3: Component Diagram — Orchestrator

Shows the internal components of the orchestrator involved in the policy update flow.

```mermaid
C4Component
    title Component Diagram — Orchestrator (Policy Update)

    Container_Boundary(orchestrator, "Orchestrator") {
        Component(httpAPI, "HTTP API Layer", "api.go", "Exposes REST endpoints, routes /policyEnforcer to agreementsHandler")
        Component(agreementsHandler, "agreementsHandler", "api.go", "Handles PUT requests, saves agreement to etcd, triggers checkJobs")
        Component(checkJobs, "checkJobs", "manage_jobs.go", "Iterates relations, finds active jobs, decides if update needed")
        Component(evalArchetype, "evaluateArchetypeInActiveJobs", "manage_jobs.go", "Builds PolicyUpdate messages, gathers job context across agents")
        Component(deleteJobInfo, "deleteJobInfo", "manage_jobs.go", "Deletes job entries when user loses all archetypes")
        Component(getJobAcross, "getJobAcrossAgents", "manage_jobs.go", "Scans all online agents for entries of a specific job")
        Component(consumer, "handleIncomingMessages", "consume.go", "Consumes from orchestrator-in queue, dispatches by message type")
        Component(processPU, "processPolicyUpdate", "manage_jobs.go", "Processes PolicyUpdate response, updates job roles/archetypes")
        Component(getAuth, "getAuthorizedProviders", "get_authorized_providers.go", "Checks which valid providers are online")
        Component(chooseArch, "chooseArchetype", "composition_request.go", "Selects best archetype based on options and weights")
        Component(chooseTTP, "chooseThirdParty", "composition_request.go", "Finds common compute provider across all valid data providers")
        Component(policyUpdateMap, "policyUpdateMap", "main.go", "In-memory map correlating request IDs to job context")
    }

    ContainerDb(etcd, "etcd", "Key-value store")
    Container(sidecar, "Sidecar", "gRPC-to-RabbitMQ")

    Rel(httpAPI, agreementsHandler, "Routes to")
    Rel(agreementsHandler, etcd, "Saves agreement")
    Rel(agreementsHandler, checkJobs, "go checkJobs()")
    Rel(checkJobs, etcd, "Reads job names")
    Rel(checkJobs, deleteJobInfo, "If no archetypes")
    Rel(checkJobs, evalArchetype, "If archetypes exist")
    Rel(evalArchetype, etcd, "Reads job info")
    Rel(evalArchetype, getJobAcross, "Gathers agents")
    Rel(getJobAcross, etcd, "Reads agents/jobs")
    Rel(evalArchetype, policyUpdateMap, "Stores job context")
    Rel(evalArchetype, sidecar, "SendPolicyUpdate")

    Rel(sidecar, consumer, "Delivers response")
    Rel(consumer, policyUpdateMap, "Looks up job context")
    Rel(consumer, processPU, "Calls")
    Rel(processPU, getAuth, "Gets online providers")
    Rel(processPU, chooseArch, "Selects archetype")
    Rel(processPU, chooseTTP, "If dataThroughTtp")
    Rel(processPU, etcd, "Updates/deletes jobs")
    Rel(processPU, sidecar, "SendCompositionRequest (if new TTP needed)")
    Rel(getAuth, etcd, "Reads /agents/online/")
    Rel(chooseArch, etcd, "Reads /archetypes/")
    Rel(chooseTTP, etcd, "Reads /agents/online/")
```

## Level 3: Component Diagram — Policy Enforcer

Shows the internal components of the policy enforcer involved in the policy update flow.

```mermaid
C4Component
    title Component Diagram — Policy Enforcer (Policy Update)

    Container_Boundary(policyEnforcer, "Policy Enforcer") {
        Component(consumer, "handleIncomingMessages", "consume.go", "Consumes from policyEnforcer-in, dispatches by message type")
        Component(checkPU, "checkPolicyUpdate", "policy_update.go", "Processes PolicyUpdate, creates ValidationResponse, sends back")
        Component(getValid, "getValidAgreements", "generate_validation_response.go", "Validates each data provider against agreements in etcd")
    }

    ContainerDb(etcd, "etcd", "Key-value store")
    Container(sidecar, "Sidecar", "gRPC-to-RabbitMQ")

    Rel(sidecar, consumer, "Delivers PolicyUpdate<br/>from policyEnforcer-in")
    Rel(consumer, checkPU, "type: policyUpdate")
    Rel(checkPU, getValid, "Validate data providers")
    Rel(getValid, etcd, "Read /policyEnforcer/agreements/{steward}")
    Rel(checkPU, sidecar, "SendPolicyUpdate<br/>to orchestrator-in")
```

## Level 3: Component Diagram — Sidecar

Shows how the sidecar bridges gRPC and RabbitMQ.

```mermaid
C4Component
    title Component Diagram — Sidecar (Message Bridge)

    Container_Boundary(sidecar, "Sidecar") {
        Component(grpcServer, "gRPC Server", "rabbit_send.go", "Exposes RabbitMQ RPC service to the main service")
        Component(sendPU, "SendPolicyUpdate", "rabbit_send.go", "Marshals PolicyUpdate, publishes to RabbitMQ queue")
        Component(sendCR, "SendCompositionRequest", "rabbit_send.go", "Marshals CompositionRequest, publishes to RabbitMQ queue")
        Component(consume, "Consume", "gRPC stream", "Streams incoming RabbitMQ messages to the main service via gRPC")
        Component(send, "send", "rabbit_send.go", "Generic AMQP publish function with retry/backoff")
    }

    Container(mainService, "Main Service", "Orchestrator or Policy Enforcer")
    ContainerQueue(rabbitmq, "RabbitMQ", "Message broker")

    Rel(mainService, grpcServer, "gRPC calls")
    Rel(grpcServer, sendPU, "SendPolicyUpdate")
    Rel(grpcServer, sendCR, "SendCompositionRequest")
    Rel(sendPU, send, "Delegates")
    Rel(sendCR, send, "Delegates")
    Rel(send, rabbitmq, "AMQP Publish")
    Rel(rabbitmq, consume, "AMQP Consume")
    Rel(consume, mainService, "gRPC stream")
```

## Combined: Full System Interaction

A single diagram combining containers and their key interactions for the entire policy update lifecycle.

```mermaid
graph TB
    subgraph "External"
        Client["External Client / Admin"]
    end

    subgraph "Orchestrator Pod"
        OrcHTTP["HTTP API<br/>(api.go)"]
        OrcJobs["Job Management<br/>(manage_jobs.go)"]
        OrcCompose["Composition Logic<br/>(composition_request.go)"]
        OrcConsume["Message Consumer<br/>(consume.go)"]
        OrcSidecar["Sidecar"]
    end

    subgraph "Policy Enforcer Pod"
        PEConsume["Message Consumer<br/>(consume.go)"]
        PEUpdate["Policy Update Handler<br/>(policy_update.go)"]
        PEValidate["Agreement Validator<br/>(generate_validation_response.go)"]
        PESidecar["Sidecar"]
    end

    subgraph "Infrastructure"
        etcd[(etcd)]
        RabbitMQ[[RabbitMQ]]
    end

    Client -->|"1. HTTP PUT<br/>/agreements"| OrcHTTP
    OrcHTTP -->|"2. Save agreement"| etcd
    OrcHTTP -->|"3. go checkJobs()"| OrcJobs
    OrcJobs -->|"4. Read jobs/agents"| etcd
    OrcJobs -->|"5. SendPolicyUpdate"| OrcSidecar
    OrcSidecar -->|"6. Publish"| RabbitMQ

    RabbitMQ -->|"7. Deliver"| PESidecar
    PESidecar -->|"8. Stream"| PEConsume
    PEConsume --> PEUpdate
    PEUpdate --> PEValidate
    PEValidate -->|"9. Read agreements"| etcd
    PEUpdate -->|"10. SendPolicyUpdate"| PESidecar
    PESidecar -->|"11. Publish"| RabbitMQ

    RabbitMQ -->|"12. Deliver"| OrcSidecar
    OrcSidecar -->|"13. Stream"| OrcConsume
    OrcConsume -->|"14. processPolicyUpdate"| OrcJobs
    OrcJobs -->|"15. chooseArchetype"| OrcCompose
    OrcJobs -->|"16. Update/delete jobs"| etcd
    OrcJobs -.->|"17. SendCompositionRequest<br/>(if archetype change)"| OrcSidecar
    OrcSidecar -.->|"18. Publish to TTP"| RabbitMQ

    style Client fill:#f9f,stroke:#333
    style etcd fill:#ffd,stroke:#333
    style RabbitMQ fill:#dff,stroke:#333
```
