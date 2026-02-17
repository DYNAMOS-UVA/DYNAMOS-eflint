# Old/Legacy Policy Update Diagrams

> **Branch:** `legacy-policy-enforcer`
>
> These diagrams describe the **old/legacy** policy update flow. See also: [Legacy Policy Update Flow](../development_guide/legacy_policy_update_flow.md)

## 1. Full Sequence Diagram — Policy Update Flow

This diagram shows the complete message flow from when an agreement is updated via HTTP until the orchestrator finishes processing the policy update.

```mermaid
sequenceDiagram
    actor Client as External Client
    participant OrcAPI as Orchestrator<br/>(HTTP API)
    participant OrcJobs as Orchestrator<br/>(Job Management)
    participant etcd as etcd
    participant OrcSidecar as Sidecar<br/>(Orchestrator)
    participant RabbitMQ as RabbitMQ
    participant PESidecar as Sidecar<br/>(Policy Enforcer)
    participant PE as Policy Enforcer

    Note over Client, PE: Phase 1: Trigger — Agreement Update

    Client->>OrcAPI: HTTP PUT /api/v1/policyEnforcer/agreements<br/>{Agreement JSON}
    OrcAPI->>etcd: Save agreement to<br/>/policyEnforcer/agreements/{name}
    OrcAPI-->>Client: HTTP 200 OK
    OrcAPI-)OrcJobs: go checkJobs(agreement)

    Note over OrcJobs, etcd: Phase 2: Evaluate Active Jobs

    loop For each relation (user) in agreement
        OrcJobs->>etcd: Get job names from<br/>/agents/jobs/{agreementName}/{userName}

        alt No active jobs
            Note over OrcJobs: Skip — nothing to update
        else No allowed archetypes in new agreement
            OrcJobs->>etcd: Delete job info (deleteJobInfo)
        else Active jobs with allowed archetypes
            Note over OrcJobs: evaluateArchetypeInActiveJobs

            loop For each active job
                OrcJobs->>etcd: Get current job info<br/>/agents/jobs/{agent}/{user}/{job}
                OrcJobs->>etcd: Get all online agents<br/>/agents/online/

                loop For each online agent
                    OrcJobs->>etcd: Check agent's job entry<br/>/agents/jobs/{agent}/{user}/{job}
                end

                Note over OrcJobs: Build PolicyUpdate message<br/>Store agentsWithThisJob in policyUpdateMap<br/>(keyed by correlationId)

                OrcJobs->>OrcSidecar: gRPC: SendPolicyUpdate(policyUpdate)

                Note over OrcSidecar, RabbitMQ: Phase 3: Publish to RabbitMQ

                OrcSidecar->>RabbitMQ: Publish to "policyEnforcer-in"<br/>type: "policyUpdate"
            end
        end
    end

    Note over RabbitMQ, PE: Phase 4: Policy Enforcer Validates

    RabbitMQ->>PESidecar: Deliver message
    PESidecar->>PE: gRPC stream: SideCarMessage<br/>type: "policyUpdate"
    PE->>PE: Unmarshal PolicyUpdate

    Note over PE: checkPolicyUpdate

    PE->>PE: Create ValidationResponse<br/>(type: "policyUpdate")

    loop For each data provider in PolicyUpdate
        PE->>etcd: Get agreement<br/>/policyEnforcer/agreements/{steward}

        alt Agreement not found
            Note over PE: Add to invalidDataproviders
        else User not in agreement relations
            Note over PE: Add to invalidDataproviders
        else User found
            PE->>PE: Match user archetypes ∩ agreement archetypes
            alt No matching archetypes
                Note over PE: Add to invalidDataproviders
            else Matching archetypes found
                Note over PE: Add to validDataproviders<br/>with matched archetypes & compute providers
            end
        end
    end

    PE->>PE: Set destination = "orchestrator-in"<br/>Set RequestApproved = (validProviders > 0)<br/>Attach ValidationResponse to PolicyUpdate

    PE->>PESidecar: gRPC: SendPolicyUpdate(policyUpdate)
    PESidecar->>RabbitMQ: Publish to "orchestrator-in"<br/>type: "policyUpdate"

    Note over OrcJobs, RabbitMQ: Phase 5: Orchestrator Processes Response

    RabbitMQ->>OrcSidecar: Deliver message
    OrcSidecar->>OrcJobs: gRPC stream: SideCarMessage<br/>type: "policyUpdate"

    OrcJobs->>OrcJobs: Look up agentsWithThisJob<br/>from policyUpdateMap[correlationId]
    OrcJobs->>OrcJobs: Delete entry from policyUpdateMap

    Note over OrcJobs: processPolicyUpdate

    OrcJobs->>etcd: Get authorized providers<br/>/agents/online/{provider}
    OrcJobs->>OrcJobs: chooseArchetype(validationResponse)
    OrcJobs->>etcd: Get archetype config<br/>/archetypes/{archetype}

    alt Same archetype as before
        Note over OrcJobs: No changes needed
    else New archetype: computeToData
        loop For each agent in job
            alt Agent role = computeProvider
                OrcJobs->>etcd: Delete job entry
            else Agent role = dataProvider/all
                OrcJobs->>etcd: Update role to "all",<br/>set new archetype
            end
        end
    else New archetype: dataThroughTtp
        OrcJobs->>OrcJobs: chooseThirdParty(validationResponse)
        loop For each agent in job
            alt Agent is correct TTP
                Note over OrcJobs: Keep as computeProvider
            else Agent is wrong TTP
                OrcJobs->>etcd: Delete job entry
            else Agent role = all
                OrcJobs->>etcd: Update role to "dataProvider",<br/>set new archetype
            end
        end

        opt No compute provider assigned yet
            OrcJobs->>OrcSidecar: gRPC: SendCompositionRequest<br/>(to new TTP)
            OrcSidecar->>RabbitMQ: Publish to TTP routing key
        end
    end
```

## 2. Simplified Overview Diagram

A high-level view focusing on the main message flow between components.

```mermaid
sequenceDiagram
    actor Client as External Client
    participant Orc as Orchestrator
    participant MQ as RabbitMQ
    participant PE as Policy Enforcer
    participant etcd as etcd

    Client->>Orc: PUT /agreements (updated agreement)
    Orc->>etcd: Save agreement
    Orc->>Orc: checkJobs → evaluateArchetypeInActiveJobs

    Orc->>MQ: PolicyUpdate → "policyEnforcer-in"
    MQ->>PE: Deliver PolicyUpdate

    PE->>etcd: Look up agreements for each data provider
    PE->>PE: Validate & build ValidationResponse

    PE->>MQ: PolicyUpdate (with ValidationResponse) → "orchestrator-in"
    MQ->>Orc: Deliver PolicyUpdate response

    Orc->>Orc: processPolicyUpdate<br/>(choose archetype, update roles)
    Orc->>etcd: Update/delete job entries

    opt Archetype change requires new compute provider
        Orc->>MQ: CompositionRequest → TTP agent
    end
```

## 3. Flow Chart — checkJobs Decision Logic

```mermaid
flowchart TD
    A[Agreement Updated via HTTP PUT] --> B[Save to etcd]
    B --> C{For each relation/user<br/>in agreement}

    C --> D[Look up active jobs in etcd<br/>/agents/jobs/agreementName/userName]

    D --> E{Active jobs<br/>found?}
    E -- No --> F[Skip — nothing to update]
    E -- Yes --> G{User has allowed<br/>archetypes?}

    G -- No --> H[deleteJobInfo<br/>Clean up all job entries]
    G -- Yes --> I[evaluateArchetypeInActiveJobs]

    I --> J{For each active job}
    J --> K[Get current job info from etcd]
    K --> L[Get all agents involved in this job<br/>getJobAcrossAgents]
    L --> M[Build PolicyUpdate message<br/>with data providers]
    M --> N[Store job context in policyUpdateMap]
    N --> O[Send PolicyUpdate to policyEnforcer-in<br/>via sidecar/RabbitMQ]

    J --> J

    C --> C
```

## 4. Flow Chart — processPolicyUpdate Decision Logic

```mermaid
flowchart TD
    A[Receive PolicyUpdate response<br/>from Policy Enforcer] --> B[Look up agentsWithThisJob<br/>from policyUpdateMap]

    B --> C[getAuthorizedProviders<br/>Check which providers are online]
    C --> D[chooseArchetype<br/>Select best archetype]
    D --> E[Get archetype config from etcd]

    E --> F{For each agent<br/>in the job}
    F --> G{Same archetype<br/>as before?}
    G -- Yes --> H[Do nothing — return]

    G -- No --> I{New archetype<br/>type?}

    I -- computeToData --> J{Agent role?}
    J -- computeProvider --> K[Delete job entry from etcd]
    J -- dataProvider/all --> L[Update role to 'all'<br/>Save new archetype to etcd]

    I -- dataThroughTtp --> M[chooseThirdParty<br/>Find common compute provider]
    M --> N{Agent role?}
    N -- "computeProvider<br/>(correct TTP)" --> O[Keep — no change]
    N -- "computeProvider<br/>(wrong TTP)" --> P[Delete job entry from etcd]
    N -- all --> Q{Agent still valid<br/>data provider?}
    Q -- Yes --> R[Update role to 'dataProvider'<br/>Save new archetype to etcd]
    Q -- No --> S[Delete job entry from etcd]

    F --> F

    L --> T{Compute provider<br/>already assigned?}
    R --> T
    K --> T
    O --> T
    P --> T
    S --> T

    T -- No --> U[Create CompositionRequest<br/>for TTP as computeProvider]
    U --> V[Send CompositionRequest<br/>to TTP via RabbitMQ]
    T -- Yes --> W[Done]
```

## 5. Message Content Diagram — PolicyUpdate Lifecycle

Shows how the `PolicyUpdate` message content evolves as it passes through the system.

```mermaid
flowchart LR
    subgraph "Orchestrator Creates"
        A["PolicyUpdate<br/>─────────────<br/>type: 'policyUpdate'<br/>user: {id, userName}<br/>data_providers: [agent1, agent2]<br/>request_metadata:<br/>  correlationId: UUID<br/>  destinationQueue: 'policyEnforcer-in'<br/>validation_response: (empty)"]
    end

    subgraph "Policy Enforcer Enriches"
        B["PolicyUpdate<br/>─────────────<br/>type: 'policyUpdate'<br/>user: {id, userName}<br/>data_providers: [agent1, agent2]<br/>request_metadata:<br/>  correlationId: UUID<br/>  destinationQueue: 'orchestrator-in'<br/>validation_response:<br/>  type: 'policyUpdate'<br/>  valid_dataproviders: {agent1: {...}}<br/>  invalid_dataproviders: [agent2]<br/>  request_approved: true/false<br/>  valid_archetypes: {agent1: [...]}"]
    end

    A -->|"via RabbitMQ<br/>(policyEnforcer-in)"| B
    B -->|"via RabbitMQ<br/>(orchestrator-in)"| C["Orchestrator processes<br/>and updates jobs"]
```

## 6. etcd Data Flow Diagram

Shows which etcd paths are read/written at each stage.

```mermaid
flowchart TD
    subgraph "Phase 1: HTTP Trigger"
        W1[/"WRITE: /policyEnforcer/agreements/{name}"/]
    end

    subgraph "Phase 2: Orchestrator evaluates"
        R1[/"READ: /agents/jobs/{agreement}/{user}"/]
        R2[/"READ: /agents/jobs/{agent}/{user}/{job}"/]
        R3[/"READ: /agents/online/"/]
        D1[/"DELETE: job entries (if no archetypes)"/]
    end

    subgraph "Phase 4: Policy Enforcer validates"
        R4[/"READ: /policyEnforcer/agreements/{steward}"/]
    end

    subgraph "Phase 5: Orchestrator processes"
        R5[/"READ: /agents/online/{provider}"/]
        R6[/"READ: /archetypes/{archetype}"/]
        W2[/"WRITE: /agents/jobs/{agent}/{user}/{job}<br/>(update role & archetype)"/]
        D2[/"DELETE: /agents/jobs/{agent}/{user}/{job}<br/>(obsolete entries)"/]
    end

    W1 --> R1
    R1 --> R2
    R2 --> R3
    R1 -.-> D1

    R4 -.-> R5
    R5 --> R6
    R6 --> W2
    R6 --> D2
```
