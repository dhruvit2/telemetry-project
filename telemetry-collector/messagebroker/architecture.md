# MessageBroker — Architecture

> **Module**: `github.com/dhruvit2/messagebroker`  
> **Language**: Go 1.26  
> **Status**: Alpha — core pipeline operational; production hardening in progress

---

## Table of Contents

1. [Overview](#1-overview)
2. [Repository Layout](#2-repository-layout)
3. [Component Map](#3-component-map)
4. [Package Reference](#4-package-reference)
   - [pkg/broker](#41-pkgbroker)
   - [pkg/coordinator](#42-pkgcoordinator)
   - [pkg/consumer](#43-pkgconsumer)
   - [pkg/replication](#44-pkgreplication)
   - [pkg/storage](#45-pkgstorage)
   - [pkg/pb](#46-pkgpb)
5. [Data & Control Flow](#5-data--control-flow)
   - [Write Path (Produce)](#51-write-path-produce)
   - [Read Path (Consume)](#52-read-path-consume)
   - [Consumer Group Rebalance](#53-consumer-group-rebalance)
   - [Broker Failure Failover](#54-broker-failure-failover)
6. [etcd Coordination](#6-etcd-coordination)
7. [Storage Layout](#7-storage-layout)
8. [gRPC API Surface](#8-grpc-api-surface)
9. [Configuration Reference](#9-configuration-reference)
10. [Deployment](#10-deployment)
11. [Implementation Status](#11-implementation-status)
12. [Known Limitations & Roadmap](#12-known-limitations--roadmap)

---

## 1. Overview

MessageBroker is a distributed, partition-based message broker written in Go, inspired by Apache Kafka's architecture. It provides:

- **Partitioned topics** — messages are distributed across N ordered, append-only partitions.
- **Consumer groups** — multiple consumers share a topic's partitions; the broker handles group membership, rebalancing, and offset tracking.
- **Replication** — each partition has a configurable replication factor; one broker acts as **leader**, the rest as **followers** forming an **In-Sync Replica (ISR)** list.
- **etcd coordination** — cluster metadata (leaders, ISR, consumer offsets, broker health) is stored in and watched through etcd, eliminating split-brain scenarios.
- **gRPC API** — all producer, consumer, and admin operations go through a single gRPC service (`MessageBroker`).

Default cluster target: **3 brokers × 3 partitions × RF=3**, with a minimum ISR of 2.

---

## 2. Repository Layout

```
messagebroker/
├── cmd/
│   ├── broker/          # Broker entry point — gRPC server + startup wiring
│   │   └── main.go
│   ├── producer/        # Producer CLI example
│   ├── consumer/        # Consumer CLI example
│   └── admin/           # Admin CLI
├── pkg/
│   ├── broker/          # Core domain model (topics, partitions, messages)
│   ├── coordinator/     # Distributed coordination (etcd + mock)
│   ├── consumer/        # Consumer group manager + rebalancer
│   ├── replication/     # ISR tracking, leader election logic
│   ├── storage/         # File-based segment log + index
│   └── pb/              # Protobuf / gRPC generated code
├── deployment/
│   ├── docker/          # Dockerfile + docker-compose
│   └── helm/            # Helm chart for Kubernetes
├── doc/
│   └── ARCHITECTURE.md  # Original design document
├── Makefile
├── go.mod
└── architecture.md      # ← this file
```

---

## 3. Component Map

```
┌──────────────────────────────────────────────────────────────────┐
│                         Clients Layer                            │
│   Producer CLI          Consumer CLI          Admin CLI          │
│   (cmd/producer)        (cmd/consumer)        (cmd/admin)        │
└────────────┬────────────────────┬──────────────────┬────────────┘
             │   gRPC (port 9092) │                  │
             ▼                    ▼                  ▼
┌──────────────────────────────────────────────────────────────────┐
│                    Broker gRPC Server (cmd/broker)               │
│  ┌──────────────┐  ┌───────────────┐  ┌─────────────────────┐   │
│  │  pkg/broker  │  │ pkg/consumer  │  │  pkg/replication    │   │
│  │  BrokerImpl  │  │ GroupManager  │  │  ReplicationManager │   │
│  │  - Topics    │  │ - Join/Leave  │  │  - ISR tracking     │   │
│  │  - Partitions│  │ - Rebalance   │  │  - Leader election  │   │
│  │  - Messages  │  │ - Offsets     │  │  - Failover detect  │   │
│  └──────┬───────┘  └───────┬───────┘  └──────────┬──────────┘   │
│         │                  │                      │              │
│         ▼                  ▼                      │              │
│  ┌──────────────────────────────────────────────┐ │              │
│  │             pkg/storage                      │ │              │
│  │  MessageLog + Index per (topic, partition)   │ │              │
│  └──────────────────────────────────────────────┘ │              │
└─────────────────────────────┬──────────────────────┘             │
                              │                                    │
                              ▼        Coordinator API             │
             ┌────────────────────────────────────────┐           │
             │        pkg/coordinator                 │◄──────────┘
             │  EtcdCoordinator  |  MockCoordinator   │
             │  - Broker registry (TTL leases)        │
             │  - Leader election (CAS transactions)  │
             │  - ISR management                      │
             │  - Consumer group membership           │
             │  - Committed offset storage            │
             └───────────────────┬────────────────────┘
                                 │
                                 ▼
             ┌────────────────────────────────────────┐
             │               etcd cluster             │
             │   (external — 3-node raft ensemble)    │
             └────────────────────────────────────────┘
```

---

## 4. Package Reference

### 4.1 `pkg/broker`

**Files**: `broker.go`, `types.go`, `errors.go`

The **core domain layer**. Holds all topics and partitions in memory with `sync.RWMutex` guards. Does not interact with etcd directly; coordination is injected at the `cmd/broker` layer.

#### Key types

| Type | Responsibility |
|------|---------------|
| `IBroker` | Interface — topic CRUD, produce/consume, partition management, health |
| `BrokerImpl` | Concrete implementation backed by `map[string]*Topic` and `map[string]*Partition` |
| `Topic` | Name, `NumPartitions`, `ReplicationFactor`, config map |
| `Partition` | In-memory message slice (capped at `maxMessages=1_000_000`), leader + ISR lists, monotonic `Offset` counter |
| `Message` | ID, topic, partition, key, value, headers, timestamp, offset |
| `BrokerConfig` | All tunable parameters — ID, host, port, coordinator URL, RF, minISR, retention, segment sizes, heartbeat intervals |
| `ConsumerGroup` | In-broker group snapshot: member map + partition→consumer mapping |
| `ReplicationState` | Per-partition replica offset tracking |

#### Partition key format

Partitions are stored with the key `"<topic>-" + string(rune(partitionID))`. This is a **known issue** — `string(rune(0))` is the null byte `"\x00"`, not `"0"`.

---

### 4.2 `pkg/coordinator`

**Files**: `coordinator.go`, `etcd.go`

Defines the `Coordinator` interface and two implementations:

| Implementation | Used when | Notes |
|----------------|-----------|-------|
| `EtcdCoordinator` | `--coordinator-type=etcd` (default) | Real distributed coordination |
| `MockCoordinator` | `--coordinator-type=mock` | In-process, suitable for development/testing |

#### `EtcdCoordinator`

Communicates with an etcd v3 cluster via `go.etcd.io/etcd/client/v3`.

- **Lease-based health**: On `Start()`, creates an etcd lease (`TTL = sessionTimeout`). Broker registration keys and consumer group membership keys are attached to this lease, so they expire automatically when the broker dies.
- **Heartbeat loop**: A goroutine calls `KeepAliveOnce` every `heartbeatInterval` to renew the lease.
- **Leader election**: Uses etcd transactions (`If version==0 Then Put`) for Compare-And-Swap semantics — guarantees only one broker becomes leader per partition.
- **Watch-based failure detection**: `WatchBrokerHealth` sets up a `client.Watch` on the broker prefix and emits `BrokerHealthEvent` on PUT (broker appeared) or DELETE (broker gone/lease expired).

#### etcd Key Schema

```
/messagebroker/brokers/<brokerID>              → BrokerMetadata JSON  [lease-attached]
/messagebroker/topics/<topicName>              → TopicMetadata JSON
/messagebroker/partitions/<topic>/<partition>  → PartitionAssignment JSON
/messagebroker/leaders/<topic>-<partition>     → int32 (leader broker ID)
/messagebroker/isr/<topic>-<partition>         → []int32 JSON
/messagebroker/consumers/<groupID>/members/<memberID> → member JSON [lease-attached]
/messagebroker/offsets/<groupID>/<topic>-<partition>  → int64 JSON
```

#### `MockCoordinator`

An in-memory implementation (`sync.RWMutex` guarded maps). Runs a `healthCheckLoop` that evicts brokers whose heartbeat timestamp exceeds `sessionTimeout` and calls `triggerFailover` to promote a new leader from the ISR.

---

### 4.3 `pkg/consumer`

**Files**: `group_manager.go`, `rebalancer.go`

#### `ConsumerGroupManager`

Manages the full lifecycle of all consumer groups on a broker. Depends on a `Coordinator` instance for cross-broker state.

```
ConsumerGroupManager
├── groups: map[groupID] → GroupState
│   ├── State: Empty | Preparing | Stable | Rebalancing | Dead
│   ├── Members: map[memberID] → MemberState
│   ├── CurrentAssignments: map[memberID] → []int32 (partitions)
│   ├── GenerationID: int32  (bumped on every rebalance)
│   └── LeaderID: string
└── monitorLoop: checks member heartbeats every 5s, evicts stale members, triggers rebalance
```

#### Rebalancing protocol

When a member joins or leaves:
1. `GenerationID` is incremented — all consumers must re-fetch assignments with the new generation.
2. `GetGroupMembers` is called on the **coordinator** (etcd) to get a **globally consistent** member list — all brokers compute identical assignments from the same sorted member list.
3. Partitions are assigned **round-robin** across sorted member IDs.
4. Committed offset for each assigned partition is fetched from the coordinator so consumers resume from the correct position.

#### Assignment strategies (implemented)

| Strategy | Description |
|----------|-------------|
| `RoundRobin` | Partitions distributed evenly in round-robin order (active default) |
| `Range` | Consecutive partition ranges per member |
| `Sticky` | (Defined, not yet wired in) |

---

### 4.4 `pkg/replication`

**File**: `replication.go`

`ReplicationManager` tracks replication state in memory. It is **not yet fully wired** to the network — follower sync currently happens conceptually.

| Type | Purpose |
|------|---------|
| `ReplicationManager` | Tracks `followerOffsets`, `inSyncReplicas`, `leaderElections` per `"topic-partition"` |
| `ReplicaSync` | Per-partition: leader, followers, ISR, last replication timestamp |
| `LeaderElection` | Captures election state: candidates, epoch, election time |

**Leader election flow (local)**:
1. `DetectFailure(brokerID)` removes the failed broker from all ISRs and calls `triggerLeaderElection` for any partition it led.
2. `triggerLeaderElection` picks `inSyncReplicas[0]` as the new leader (first-in-ISR preference).
3. The etcd coordinator layer does the same thing durably — the local `ReplicationManager` is a cache/fast-path.

---

### 4.5 `pkg/storage`

**File**: `storage.go`

Provides **per-partition message logs** and **offset indices**.

```
Storage
├── logs:    map["topic-partition"] → MessageLog
└── indices: map["topic-partition"] → Index
```

#### `MessageLog`

- Backed by an in-memory `[]map[string]interface{}` (JSON-serialisable).
- Tracks `CurrentSize` vs `MaxSize` (default 1 GB); calls `rotateLog` when full — resets the slice but bumps `BaseOffset`.
- **Disk path created** (`os.MkdirAll`) at `<dataDir>/<topic>/<partition>/log.data`, but writes are currently to the in-memory slice only (disk persistence is a known gap).

#### `Index`

- Stores `(offset, position, timestamp)` triples in `[]IndexEntry` for O(1) seek by offset.
- Paired with each `MessageLog`.

#### Retention

`Cleanup()` removes messages older than `retentionMs` milliseconds by scanning all logs and rebuilding slices.

---

### 4.6 `pkg/pb`

Generated protobuf + gRPC code. See [§8 gRPC API Surface](#8-grpc-api-surface) for the full service definition.

---

## 5. Data & Control Flow

### 5.1 Write Path (Produce)

```
Producer Client
    │
    │  ProduceRequest{topic, partition, key, value}
    ▼
cmd/broker — Server.ProduceMessage()
    │
    ├─ syncTopicLocally()            ← pulls topic metadata from etcd if missing locally
    │
    ├─ broker.ProduceMessage()       ← appends message to in-memory partition slice
    │      Partition.Offset++
    │      returns int64 offset
    │
    └─ storage.WriteMessage()        ← writes to storage log + index
           updates MessageLog.Messages[]
           adds IndexEntry{offset, position}
           checks size → rotateLog if needed

    ProduceResponse{topic, partition, offset}
```

> Replication to followers is **not yet active** in the network layer. The `ReplicationManager` tracks state locally.

---

### 5.2 Read Path (Consume)

```
Consumer Client
    │
    │  ConsumeRequest{topic, partition, offset, maxMessages}
    ▼
cmd/broker — Server.ConsumeMessages()
    │
    ├─ syncTopicLocally()            ← ensures topic exists locally
    │
    └─ broker.ConsumeMessages()      ← scans partition.messages[] from requested offset
           returns up to maxMessages messages

    ConsumeResponse{topic, partition, []Message}
```

---

### 5.3 Consumer Group Rebalance

```
New Consumer C3 joins group "telemetry-collectors"
    │
    │  JoinConsumerGroupRequest{groupId, memberId, topics}
    ▼
Server.JoinConsumerGroup()
    │
    └─ GroupManager.JoinConsumerGroup()
           ├─ coordinator.JoinConsumerGroup()     ← writes /consumers/<group>/members/<id> to etcd
           ├─ GenerationID++
           ├─ State = Rebalancing
           │
           └─ rebalanceGroup()
                  ├─ coordinator.GetTopicMetadata()    ← gets numPartitions from etcd
                  ├─ coordinator.GetGroupMembers()     ← gets ALL members across ALL brokers (etcd)
                  ├─ sort.Strings(members)             ← deterministic ordering
                  └─ assignPartitionsRoundRobin()
                         partition[i] → members[i % len(members)]

    ├─ State = Stable
    └─ JoinConsumerGroupResponse{generationId, members}

Consumer then calls FetchAssignments() → returns its assigned []int32 partition IDs
Consumer calls FetchOffset() per partition → resumes from last committed offset
```

---

### 5.4 Broker Failure Failover

```
Broker 2 crashes (lease expires in etcd)
    │
    ▼
etcd emits DELETE event on /messagebroker/brokers/2
    │
    ▼
EtcdCoordinator.WatchBrokerHealth() goroutine
    │  BrokerHealthEvent{BrokerID:2, IsHealthy:false}
    │
    ▼
ReplicationManager.DetectFailure(brokerID=2)
    ├─ Remove broker 2 from all ISR lists
    │
    └─ For each partition led by broker 2:
           triggerLeaderElection()
               newLeader = inSyncReplicas[0]
               leaders[key] = newLeader

EtcdCoordinator.ElectLeader() (durable)
    ├─ Reads ISR from etcd
    ├─ Picks first healthy ISR member
    └─ CAS: If version(leaderKey)==0 Then Put(leaderKey, newLeader)
```

---

## 6. etcd Coordination

### Startup Sequence (per broker)

```
1. Parse flags / env vars (BROKER_ID, BROKER_HOST, BROKER_PORT, COORDINATOR_URL)
2. NewEtcdCoordinator(endpoints, brokerID, heartbeatInterval, sessionTimeout)
3. coordinator.Start(ctx)
   ├─ client.Grant(TTL=sessionTimeout)  → leaseID
   └─ go heartbeatLoop()               → KeepAliveOnce every heartbeatInterval
4. coordinator.RegisterBroker()         → PUT /brokers/<id> with lease
5. consumer group manager.Start()       → go monitorLoop()
6. grpc.NewServer() + Serve()
```

### Broker ID Assignment

In Kubernetes `StatefulSet` mode, the broker ID is parsed from the pod name suffix:

```
POD_NAME=messagebroker-2  →  BROKER_ID=2
```

Falls back to `BROKER_ID` env var, then flag `-id`, then defaults to `1`.

---

## 7. Storage Layout

```
<dataDir>/
└── <topic>/
    └── <partition>/          (directory, created by storage.CreatePartitionLog)
        ├── log.data          (message log — JSON records)
        └── index.idx         (offset index — IndexEntry list)
```

**Segment rotation**: when `CurrentSize >= MaxSize` (default 1 GB), `rotateLog` bumps `BaseOffset` and resets the slice. In production this would flush the old segment to disk and open a new file.

**Retention**: `Cleanup()` removes records where `now.UnixMilli() - msg.timestamp > retentionMs` (default 7 days).

---

## 8. gRPC API Surface

Service defined in `pkg/pb/messagebroker.proto`:

```protobuf
service MessageBroker {
  // Topic management
  rpc CreateTopic(CreateTopicRequest)         returns (CreateTopicResponse);
  rpc GetTopicMetadata(GetTopicMetadataRequest) returns (TopicMetadata);
  rpc BrokerMetadata(BrokerMetadataRequest)   returns (BrokerMetadataResponse);

  // Message I/O
  rpc ProduceMessage(ProduceRequest)          returns (ProduceResponse);
  rpc ConsumeMessages(ConsumeRequest)         returns (ConsumeResponse);

  // Consumer group lifecycle
  rpc JoinConsumerGroup(JoinConsumerGroupRequest)   returns (JoinConsumerGroupResponse);
  rpc LeaveConsumerGroup(LeaveConsumerGroupRequest) returns (LeaveConsumerGroupResponse);
  rpc FetchAssignments(FetchAssignmentsRequest)     returns (FetchAssignmentsResponse);
  rpc HeartbeatMember(...)                          returns (...);   // planned

  // Offset management
  rpc CommitOffset(CommitOffsetRequest)       returns (CommitOffsetResponse);
  rpc FetchOffset(FetchOffsetRequest)         returns (FetchOffsetResponse);

  // Admin
  rpc ListConsumerGroups(ListConsumerGroupsRequest)   returns (ListConsumerGroupsResponse);
  rpc DescribeConsumerGroup(DescribeConsumerGroupRequest) returns (DescribeConsumerGroupResponse);
}
```

### Important message fields

| Message | Key fields |
|---------|-----------|
| `ProduceRequest` | `topic`, `partition` (−1 = auto), `key []byte`, `value []byte` |
| `ProduceResponse` | `topic`, `partition`, `offset int64` |
| `ConsumeRequest` | `topic`, `partition`, `offset int64`, `max_messages int32` |
| `JoinConsumerGroupRequest` | `group_id`, `member_id`, `topics []string` |
| `JoinConsumerGroupResponse` | `generation_id int32`, `members []string`, `need_rebalance bool` |
| `FetchAssignmentsResponse` | `generation_id`, `assignments []{partition int32}` |

---

## 9. Configuration Reference

All parameters can be set via **flags** or **environment variables** (env takes precedence at default level).

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `-id` | `BROKER_ID` | auto from POD_NAME | Unique broker integer ID |
| `-host` | `BROKER_HOST` | `localhost` | Advertised hostname |
| `-port` | `BROKER_PORT` | `9092` | gRPC listen port |
| `-coordinator` | `COORDINATOR_URL` | `localhost:2379` | etcd endpoint(s) |
| `-coordinator-type` | `COORDINATOR_TYPE` | `etcd` | `etcd` or `mock` |
| `-etcd-username` | `ETCD_USERNAME` | `""` | etcd auth username |
| `-etcd-password` | `ETCD_PASSWORD` | `""` | etcd auth password |
| `-data-dir` | `DATA_DIR` | `/tmp/messagebroker` | Persistent storage root |

**Hardcoded defaults** (in `BrokerConfig`):

| Parameter | Value |
|-----------|-------|
| MaxPartitions | 1,000 |
| ReplicationFactor | 3 |
| MinISR | 2 |
| RetentionMs | 604,800,000 (7 days) |
| SegmentMs | 86,400,000 (1 day) |
| SegmentBytes | 1,073,741,824 (1 GB) |
| HeartbeatInterval | 5 s |
| SessionTimeout | 30 s |
| Rebalance Timeout | 60 s |
| Monitor loop interval | 5 s |

---

## 10. Deployment

### Local development

```bash
# Prerequisites: etcd running on localhost:2379

make build-broker
make run-broker BROKER_ID=1 BROKER_PORT=9092

# Or with in-memory mock coordinator (no etcd needed):
./bin/broker -id 1 -coordinator-type=mock
```

### Docker Compose (3-broker cluster)

```bash
make run-docker    # starts 3 brokers + etcd via docker-compose
make stop-docker
```

Brokers expose:
- Broker 0: `localhost:9092`
- Broker 1: `localhost:9093`
- Broker 2: `localhost:9094`
- etcd: `localhost:2379`

### Kubernetes (Helm)

```bash
make deploy-k8s       # helm install into namespace 'messagebroker'
make status-k8s       # kubectl get pods/svc
make delete-k8s       # helm uninstall
```

The Helm chart deploys a `StatefulSet` so pod names are `messagebroker-0`, `messagebroker-1`, `messagebroker-2` — broker IDs are derived automatically from the ordinal suffix.

### Build targets

```bash
make build          # build all binaries → bin/{broker,producer,consumer}
make build-docker   # build Docker image
make push-docker    # push to registry
make proto          # regenerate protobuf code
make test           # go test -v ./...
make lint           # golangci-lint
make clean
```

---

## 11. Implementation Status

| Component | Status | Notes |
|-----------|--------|-------|
| Broker core (topics, partitions, messages) | ✅ Done | In-memory, thread-safe |
| gRPC API server | ✅ Done | All RPC handlers wired |
| etcd coordinator | ✅ Done | Lease-based, CAS leader election, watch-based health |
| Mock coordinator | ✅ Done | Dev/test only |
| Consumer group manager | ✅ Done | Join/leave, round-robin rebalance, offset commit |
| Rebalance via etcd global view | ✅ Done | `GetGroupMembers` from etcd ensures determinism |
| Storage layer (log + index) | ✅ Done | In-memory backed, directory structure created on disk |
| Segment rotation | ✅ Done | Size-based rotation (time-based not yet wired) |
| Retention cleanup | ✅ Done | `Cleanup()` implemented, not yet scheduled |
| StatefulSet pod name → broker ID | ✅ Done | Parses ordinal from `POD_NAME` |
| Network replication (follower fetch) | ❌ Not started | `ReplicationManager` tracks state locally only |
| Disk flush (actual persistence) | ⚠️ Partial | Directory + path created; `WriteMessage` writes in-memory |
| ACK levels (none / leader / all) | ❌ Not started | Proto has no ACK field yet |
| Batch produce/consume | ❌ Not started | Single-message RPCs only |
| Metrics / Prometheus endpoint | ❌ Not started | No `/metrics` handler |
| Health/readiness endpoint | ❌ Not started | `IsHealthy()` exists; HTTP endpoint missing |
| TLS / mTLS | ❌ Not started | gRPC server has no TLS config |
| Compression (snappy, gzip) | ❌ Not started | |
| Sticky rebalance strategy | ❌ Not started | Constant defined, not implemented |
| Schema registry | 📋 Planned | |
| Transactions | 📋 Planned | |

---

## 12. Known Limitations & Roadmap

### Active bugs / gaps

| Issue | Location | Impact |
|-------|----------|--------|
| Partition key uses `string(rune(id))` instead of `strconv.Itoa(id)` | `broker.go`, `storage.go` | Partition 0 maps to `"\x00"` — silent data corruption for partition >127 |
| Topic sync (`syncTopicLocally`) is one-way and best-effort | `cmd/broker/main.go` | No authoritative topic registry broadcast |
| Follower replication is in-memory simulation only | `pkg/replication` | No actual data durability across broker restarts |
| `ListConsumerGroups` returns a hardcoded topic placeholder | `cmd/broker/main.go:545` | Incorrect topic list in admin responses |
| Disk `Flush()` is a no-op | `pkg/storage/storage.go` | Data lost on process restart |

### Phase 2 — Production Ready

- [ ] Fix partition key encoding (`strconv.Itoa`)
- [ ] Network replication: follower fetch loop over gRPC
- [ ] Actual disk flush in `storage.WriteMessage`
- [ ] ACK levels in proto + produce handler
- [ ] Batch produce / consume RPCs
- [ ] Prometheus metrics at `/metrics`
- [ ] HTTP health + readiness endpoints
- [ ] TLS for gRPC
- [ ] Scheduled retention cleanup goroutine

### Phase 3 — Advanced Features

- [ ] Compression (snappy, gzip) in message encoding
- [ ] Sticky rebalance strategy
- [ ] Schema registry integration
- [ ] Cross-partition transactions
- [ ] Tiered storage (hot/cold separation)
- [ ] Multi-tenancy (namespace isolation)
- [ ] Stream processing API (KStream-style)
