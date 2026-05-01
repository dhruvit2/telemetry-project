# Distributed Telemetry Pipeline — Architecture

> **Stack**: Go 1.21+ · Kubernetes · Helm 3 · etcd v3 · InfluxDB 2.7 · gRPC · Docker  
> **Images**: `dhruvit2/{messagebroker,telemetry-collector,telemetry-api,telemetry-streaming}:5.0.16`

---

## High-Level System Overview

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                          KUBERNETES CLUSTER (namespace: default)                │
│                                                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────┐   │
│  │  DATA INGESTION LAYER                                                    │   │
│  │                                                                          │   │
│  │  ┌─────────────────────────────────────────┐                            │   │
│  │  │  telemetry-streaming  (Pod ×1)           │                            │   │
│  │  │  Image: dhruvit2/telemetry-streaming     │                            │   │
│  │  │  Port: 8080                              │                            │   │
│  │  │                                          │                            │   │
│  │  │  • Reads data1.csv from ConfigMap        │                            │   │
│  │  │  • Streams at 100 msgs/sec               │                            │   │
│  │  │  • Batch size: 50, Snappy compressed     │                            │   │
│  │  │  • Loops CSV continuously                │                            │   │
│  │  └────────────────┬────────────────────────┘                            │   │
│  │                   │ gRPC Produce (topic: telemetry-data)                 │   │
│  └───────────────────┼──────────────────────────────────────────────────────┘  │
│                      │                                                          │
│  ┌───────────────────▼──────────────────────────────────────────────────────┐  │
│  │  MESSAGE BROKER LAYER                                                    │  │
│  │                                                                          │  │
│  │  StatefulSet: messagebroker (×1 pod, scalable to 3)                      │  │
│  │  Image: dhruvit2/messagebroker   Port: 9092 (gRPC)                       │  │
│  │  Headless Service: messagebroker-headless                                │  │
│  │                                                                          │  │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐       │  │
│  │  │  messagebroker-0 │  │  messagebroker-1 │  │  messagebroker-2 │       │  │
│  │  │  :9092 (leader)  │  │  :9092 (follower)│  │  :9092 (follower)│       │  │
│  │  └────────┬─────────┘  └────────┬─────────┘  └────────┬─────────┘       │  │
│  │           │                     │                      │                 │  │
│  │  ┌────────┴─────────────────────┴──────────────────────┴──────────────┐  │  │
│  │  │  Topic: telemetry-data                                             │  │  │
│  │  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐               │  │  │
│  │  │  │ Partition 0 │  │ Partition 1 │  │ Partition 2 │               │  │  │
│  │  │  │ offset: N   │  │ offset: N   │  │ offset: N   │               │  │  │
│  │  │  └─────────────┘  └─────────────┘  └─────────────┘               │  │  │
│  │  └────────────────────────────────────────────────────────────────────┘  │  │
│  │                                                                          │  │
│  │  Internal packages:                                                      │  │
│  │    pkg/broker     → topics, partitions, in-memory message store          │  │
│  │    pkg/consumer   → consumer group manager + round-robin rebalancer      │  │
│  │    pkg/replication→ ISR tracking, leader election                        │  │
│  │    pkg/storage    → per-partition MessageLog + Index (disk-backed path)  │  │
│  │    pkg/coordinator→ etcd coordination (via pkg/pb gRPC)                  │  │
│  │    pkg/pb         → protobuf / gRPC generated code                       │  │
│  │                                                                          │  │
│  │  PersistentVolume: 1Gi (longhorn-custom)                                │  │
│  └──────────┬──────────────────────────────────────────┬───────────────────┘  │
│             │ gRPC consume                              │ etcd watch/lease      │
│  ┌──────────▼───────────────────────────────┐  ┌───────▼─────────────────────┐│
│  │  DATA COLLECTION LAYER                   │  │  COORDINATION LAYER         ││
│  │                                          │  │                             ││
│  │  Deployment: telemetry-collector (×3)    │  │  StatefulSet: telemetry-etcd││
│  │  Image: dhruvit2/telemetry-collector     │  │  (bitnami/etcd, ×3 replicas)││
│  │  Port: 8080                              │  │  Port: 2379                 ││
│  │                                          │  │                             ││
│  │  Consumer Group: telemetry-collectors    │  │  etcd key schema:           ││
│  │  Strategy: RoundRobin                    │  │  /messagebroker/brokers/<id>││
│  │  Batch size: 50, timeout: 5s             │  │  /messagebroker/leaders/... ││
│  │  Session timeout: 30s                    │  │  /messagebroker/isr/...     ││
│  │  Heartbeat: 10s                          │  │  /messagebroker/consumers/..││
│  │                                          │  │  /messagebroker/offsets/... ││
│  │  collector-0 → Partition 0               │  │  /messagebroker/topics/...  ││
│  │  collector-1 → Partition 1               │  │                             ││
│  │  collector-2 → Partition 2               │  │  Auth: root/root123         ││
│  │                                          │  │  PVC: 1Gi (longhorn-custom) ││
│  │  Writes to: influxdb-tsdb:8086           │  │                             ││
│  │  Org: telemetry                          │  └─────────────────────────────┘│
│  │  Bucket: gpu_metrics_raw                 │                                  │
│  │  PVC: 2Gi (longhorn-custom)              │                                  │
│  └──────────┬───────────────────────────────┘                                  │
│             │ HTTP Write (Line Protocol)                                        │
│  ┌──────────▼─────────────────────────────────────────────────────────────┐    │
│  │  STORAGE LAYER                                                         │    │
│  │                                                                        │    │
│  │  Deployment: influxdb-tsdb (×1)                                        │    │
│  │  Image: influxdb:2.7    Port: 8086                                     │    │
│  │  Service: influxdb-tsdb (ClusterIP)                                    │    │
│  │                                                                        │    │
│  │  Organization: telemetry                                               │    │
│  │  Bucket: gpu_metrics_raw   Retention: 4 weeks                         │    │
│  │  Token: CtptH6YYj... (stored as K8s Secret)                           │    │
│  │  PVC: 2Gi (longhorn-custom)  → /var/lib/influxdb2                     │    │
│  │                                                                        │    │
│  │  Admin: admin / admin123                                               │    │
│  └──────────┬─────────────────────────────────────────────────────────────┘   │
│             │ HTTP Query (Flux / InfluxQL)                                      │
│  ┌──────────▼─────────────────────────────────────────────────────────────┐   │
│  │  API LAYER                                                             │   │
│  │                                                                        │   │
│  │  Deployment: telemetry-api (×1)                                        │   │
│  │  Image: dhruvit2/telemetry-api:5.0.14   Port: 8080                    │   │
│  │  Service: telemetry-api (ClusterIP)                                    │   │
│  │                                                                        │   │
│  │  Endpoints:                                                            │   │
│  │    GET  /health          → liveness probe                              │   │
│  │    GET  /ready           → readiness probe                             │   │
│  │    GET  /metrics         → telemetry metrics                           │   │
│  │    GET  /api/v1/...      → query GPU telemetry from InfluxDB           │   │
│  │                                                                        │   │
│  │  OpenAPI spec: openapi.yaml                                            │   │
│  └────────────────────────────────────────────────────────────────────────┘   │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## Data Flow (End-to-End)

```
[CSV data1.csv]
      │
      ▼
┌─────────────────────┐
│  telemetry-streaming│  → Reads CSV rows at 100 msgs/sec
│  (streamer pod)     │  → Batches (50 msgs), Snappy compress
└─────────┬───────────┘  → ProduceMessage() via gRPC (acks: all)
          │
          │ gRPC :9092  topic="telemetry-data"
          ▼
┌─────────────────────────────────────────────────────┐
│              messagebroker (StatefulSet)             │
│                                                     │
│  ProduceMessage() handler:                          │
│    → syncTopicLocally() from etcd                   │
│    → broker.ProduceMessage() → partition.Offset++   │
│    → storage.WriteMessage() → MessageLog + Index    │
│                                                     │
│  Partitions (round-robin key hash):                 │
│    Partition 0  Partition 1  Partition 2            │
└──────────────────────┬──────────────────────────────┘
                       │
        ┌──────────────┼──────────────┐
        │              │              │
        ▼              ▼              ▼
┌──────────────┐ ┌──────────────┐ ┌──────────────┐
│  collector-0 │ │  collector-1 │ │  collector-2 │
│  ← Part. 0  │ │  ← Part. 1  │ │  ← Part. 2  │
│              │ │              │ │              │
│ ConsumeMsg() │ │ ConsumeMsg() │ │ ConsumeMsg() │
│ batchSize=50 │ │ batchSize=50 │ │ batchSize=50 │
│ CommitOffset │ │ CommitOffset │ │ CommitOffset │
└──────┬───────┘ └──────┬───────┘ └──────┬───────┘
       │                │                │
       └────────────────┴────────────────┘
                        │
                        │ HTTP Write (InfluxDB Line Protocol)
                        ▼
             ┌─────────────────────┐
             │   influxdb-tsdb     │
             │   :8086             │
             │   bucket: gpu_metrics_raw
             │   org: telemetry   │
             └──────────┬──────────┘
                        │
                        │ HTTP Flux Query
                        ▼
             ┌─────────────────────┐
             │   telemetry-api     │
             │   :8080             │
             │   REST API          │
             └─────────────────────┘
                        │
                        ▼
                  [External Clients]
               (Dashboards, Alerts, etc.)
```

---

## Consumer Group Rebalancing Flow (via etcd)

```
New collector pod joins (or existing one crashes)
          │
          ▼
  JoinConsumerGroup(groupId="telemetry-collectors", memberId, topics)
          │
          ▼
  coordinator.JoinConsumerGroup()
    → PUT /messagebroker/consumers/telemetry-collectors/members/<id>  [lease-attached]
          │
          ▼
  GenerationID++ → State = Rebalancing
          │
          ▼
  coordinator.GetGroupMembers()  ← reads ALL members from etcd (global view)
  sort.Strings(members)          ← deterministic ordering across all brokers
          │
          ▼
  assignPartitionsRoundRobin():
    partition[i] → members[i % len(members)]
          │
          ▼
  State = Stable
  FetchAssignments() → consumer gets its []int32 partition IDs
  FetchOffset()      → resume from last committed offset
```

---

## Broker Failure Failover Flow

```
messagebroker-1 crashes
          │
          ▼
  etcd lease expires → DELETE /messagebroker/brokers/1
          │
          ▼
  EtcdCoordinator.WatchBrokerHealth() goroutine fires
    BrokerHealthEvent{BrokerID: 1, IsHealthy: false}
          │
          ▼
  ReplicationManager.DetectFailure(brokerID=1)
    → Remove broker 1 from all ISR lists
    → For each partition led by broker 1:
         triggerLeaderElection()
           newLeader = inSyncReplicas[0]
          │
          ▼
  EtcdCoordinator.ElectLeader() (durable CAS)
    → Reads ISR from etcd
    → CAS: If version(leaderKey)==0 Then Put(leaderKey, newLeader)
```

---

## Kubernetes Resources Summary

| Component | Kind | Replicas | Image | Port | Storage |
|-----------|------|----------|-------|------|---------|
| `telemetry-streaming` | Deployment | 1 | `dhruvit2/telemetry-streaming:5.0.16` | 8080 | ConfigMap (CSV) |
| `messagebroker` | StatefulSet | 1–3 | `dhruvit2/messagebroker:5.0.16` | 9092 | 1Gi PVC (longhorn-custom) |
| `telemetry-etcd` | StatefulSet | 3 | `bitnami/etcd` | 2379 | 1Gi PVC (longhorn-custom) |
| `telemetry-collector` | Deployment | 3 | `dhruvit2/telemetry-collector:5.0.16` | 8080 | 2Gi PVC (longhorn-custom) |
| `influxdb-tsdb` | Deployment | 1 | `influxdb:2.7` | 8086 | 2Gi PVC (longhorn-custom) |
| `telemetry-api` | Deployment | 1 | `dhruvit2/telemetry-api:5.0.14` | 8080 | — |

---

## Helm Charts & Deployment Paths

```
telemetry-project/
├── messagebroker/
│   └── deployment/helm/messagebroker/         → Helm chart (StatefulSet)
│       ├── values.yaml                         broker port 9092, etcd coordinator
│       └── templates/
├── telemetry-etcd/
│   └── values.yaml                            → bitnami/etcd, RF=3, auth enabled
├── telemetry-collector/
│   └── deployment/helm/telemetry-collector/   → Helm chart (Deployment ×3)
│       └── values.yaml                         consumer group, InfluxDB target
├── telemetry-streaming/
│   └── deployment/helm/telemetry-streaming/   → Helm chart (Deployment ×1)
│       └── values.yaml                         CSV via ConfigMap, broker address
├── tsdb-influxdb/
│   └── helm/                                  → Helm chart (Deployment ×1)
│       └── values.yaml                         org=telemetry, bucket=gpu_metrics_raw
└── telemetry-api/
    └── helm/deployment/                       → Helm chart (Deployment ×1)
        └── values.yaml                         REST API → InfluxDB
```

---

## Port & Network Map

```
Service                 Port    Protocol   Consumers
─────────────────────────────────────────────────────────────
telemetry-etcd          2379    TCP        messagebroker (coordinator)
messagebroker-headless  9092    gRPC       telemetry-streaming (producer)
                                           telemetry-collector (consumer)
influxdb-tsdb           8086    HTTP       telemetry-collector (write)
                                           telemetry-api (query)
telemetry-collector     8080    HTTP       Prometheus (metrics scrape)
telemetry-api           8080    HTTP       External clients / dashboards
telemetry-streaming     8080    HTTP       Health probes
```

---

## MessageBroker Internal Package Map

```
cmd/broker/main.go  (entry point)
    │
    ├─── pkg/broker/BrokerImpl         ← topics, partitions, in-memory msg store
    │        └─ pkg/storage/MessageLog ← per-partition log + offset index
    │
    ├─── pkg/consumer/GroupManager     ← consumer group lifecycle + rebalancer
    │
    ├─── pkg/replication/Manager       ← ISR tracking, leader election cache
    │
    └─── pkg/coordinator/EtcdCoordinator  ← etcd v3 client
             ├─ RegisterBroker()
             ├─ LeaderElection() CAS
             ├─ WatchBrokerHealth()
             ├─ JoinConsumerGroup()
             ├─ GetGroupMembers()
             └─ CommitOffset() / FetchOffset()
```

---

## gRPC API (MessageBroker Service, port 9092)

```protobuf
service MessageBroker {
  // Topic management
  rpc CreateTopic(CreateTopicRequest)          returns (CreateTopicResponse);
  rpc GetTopicMetadata(GetTopicMetadataRequest) returns (TopicMetadata);
  rpc BrokerMetadata(BrokerMetadataRequest)    returns (BrokerMetadataResponse);

  // Message I/O
  rpc ProduceMessage(ProduceRequest)           returns (ProduceResponse);
  rpc ConsumeMessages(ConsumeRequest)          returns (ConsumeResponse);

  // Consumer group lifecycle
  rpc JoinConsumerGroup(JoinConsumerGroupRequest)   returns (JoinConsumerGroupResponse);
  rpc LeaveConsumerGroup(LeaveConsumerGroupRequest) returns (LeaveConsumerGroupResponse);
  rpc FetchAssignments(FetchAssignmentsRequest)     returns (FetchAssignmentsResponse);

  // Offset management
  rpc CommitOffset(CommitOffsetRequest)        returns (CommitOffsetResponse);
  rpc FetchOffset(FetchOffsetRequest)          returns (FetchOffsetResponse);

  // Admin
  rpc ListConsumerGroups(...)                  returns (...);
  rpc DescribeConsumerGroup(...)               returns (...);
}
```

---

## Configuration Quick Reference

| Service | Key Config | Value |
|---------|-----------|-------|
| messagebroker | broker.port | 9092 |
| messagebroker | coordinator.url | `telemetry-etcd:2379` |
| messagebroker | replicationFactor | 1 (default), 3 (target) |
| messagebroker | retentionMs | 604,800,000 (7 days) |
| telemetry-etcd | auth.rbac | enabled, root/root123 |
| telemetry-etcd | replicaCount | 3 |
| telemetry-collector | replicaCount | 3 |
| telemetry-collector | brokerAddresses | `messagebroker-{0,1,2}.messagebroker-headless:9092` |
| telemetry-collector | consumerGroup | `telemetry-collectors` |
| telemetry-collector | batchSize | 50 |
| telemetry-streaming | readSpeed | 100 msgs/sec |
| telemetry-streaming | brokerAddresses | `messagebroker-0.messagebroker-headless:9092` |
| telemetry-streaming | batchSize | 50 |
| influxdb-tsdb | port | 8086 |
| influxdb-tsdb | org | `telemetry` |
| influxdb-tsdb | bucket | `gpu_metrics_raw` |
| influxdb-tsdb | retention | 4 weeks |
| telemetry-api | tsdb.url | `http://influxdb-tsdb:8086` |
| telemetry-api | port | 8080 |

---

## Implementation Status

| Component | Status | Notes |
|-----------|--------|-------|
| Broker core (topics, partitions) | ✅ Done | In-memory, thread-safe |
| gRPC API server | ✅ Done | All RPC handlers wired |
| etcd coordinator | ✅ Done | Lease-based, CAS leader election |
| Consumer group manager | ✅ Done | Join/leave, round-robin rebalance |
| Rebalance via etcd global view | ✅ Done | Deterministic assignment |
| Storage layer (log + index) | ✅ Done | In-memory backed, dir structure on disk |
| StatefulSet pod → broker ID | ✅ Done | Parses ordinal from POD_NAME |
| Telemetry streaming (CSV → broker) | ✅ Done | Loops CSV, configurable speed |
| Telemetry collector (broker → TSDB) | ✅ Done | 3-replica consumer group |
| InfluxDB TSDB | ✅ Done | Helm-deployed, 4-week retention |
| Telemetry REST API | ✅ Done | Queries InfluxDB, OpenAPI spec |
| Network replication (follower fetch) | ❌ Not started | ISR tracked locally only |
| Disk flush (actual persistence) | ⚠️ Partial | Directory created; writes in-memory |
| Prometheus /metrics | ❌ Not started | Annotations present, handler missing |
| TLS / mTLS | ❌ Not started | — |
| Batch produce/consume RPCs | ❌ Not started | Single-message only |
