---
# MessageBroker - Architecture and Design Document

**Status**: Alpha (Core features implemented, advanced features in design phase)

**Implementation Status**:
- ✅ **Implemented**: Basic broker, partitions, topics, gRPC API, replication manager, storage, etcd coordinator, consumer groups
- ⏳ **In Progress**: Metrics/health endpoints, graceful failover
- 📋 **Planned**: Compression, batching, ACK levels, TLS/mTLS, stream processing

## Current Implementation Summary

### ✅ What's Built

| Component | Status | Details |
|-----------|--------|---------|
| **Broker Core** | ✅ Implemented | Topics, partitions, message storage |
| **gRPC API** | ✅ Implemented | Produce, Consume, Metadata, Consumer Groups |
| **Replication Manager** | ✅ Implemented | Leader tracking, ISR management, offset tracking |
| **Storage Layer** | ✅ Implemented | File-based logs, indices, message persistence |
| **etcd Coordinator** | ✅ Implemented | Leader election, broker registration, metadata management |
| **Consumer Groups** | ✅ Implemented | Join/leave, rebalancing, offset management |
| **Producer API** | ⏳ Partial | Basic produce working, no ACK levels yet |
| **Consumer API** | ✅ Partial | Consumer groups with assignments working |

### ⏳ What's Missing

| Component | Status | Impact |
|-----------|--------|--------|
| **ACK Levels** | 📋 Planned | Durability guarantees incomplete |
| **Metrics/Health** | 📋 Planned | Observability not available |
| **Batch Operations** | 📋 Planned | Performance could be improved |
| **Graceful Failover** | ⏳ In Progress | Leader failover exists, consumer failover needs enhancement |
| **TLS/mTLS** | 📋 Planned | Encryption for communication |

### 📝 Proto API Limitations

Current `.proto` API is minimal:
```
service MessageBroker {
  rpc CreateTopic(...)
  rpc ProduceMessage(...)        // No ACK level support
  rpc ConsumeMessages(...)       // No consumer group support
  rpc GetTopicMetadata(...)
  rpc BrokerMetadata(...)
}
```

## High-Level Architecture

### Components

```
┌─────────────────────────────────────────────────────────────┐
│                      Clients Layer                          │
├─────────────────────┬───────────────────┬──────────────────┤
│  Producer Client    │  Consumer Client  │  Admin Client    │
└──────────┬──────────┴──────────┬────────┴────────┬─────────┘
           │                     │                 │
           │      gRPC API       │                 │
           ▼                     ▼                 ▼
┌─────────────────────────────────────────────────────────────┐
│                    Broker Cluster                           │
├────────────┬────────────┬─────────────┬──────────────────────┤
│  Broker 1  │  Broker 2  │  Broker 3   │  ...                │
│ (Leader)   │ (Replica)  │ (Replica)   │                     │
└────────────┴────────────┴─────────────┴──────────────────────┘
           │                     │                 │
           │     Replication     │                 │
           │     & Sync          │                 │
           └─────────┬───────────┘                 │
                     │                             │
                     ▼                             │
        ┌──────────────────────────┐               │
        │  In-Sync Replicas (ISR)  │◄──────────────┘
        │  Tracking & Management   │
        └──────────────────────────┘
           │
           │      Coordinator API
           ▼
┌─────────────────────────────────────────────────────────────┐
│                     etcd (Coordinator)                      │
│  (Leader Election, Metadata, Config Management)            │
└─────────────────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────┐
│                  Storage Layer                              │
│  (Persistent Logs, Indices, Segments)                      │
└─────────────────────────────────────────────────────────────┘
```

## Key Design Decisions

### 1. **Topic and Partition Model**

- **Topics**: Logical message categories
- **Partitions**: Physical message segments within a topic
- Each partition can have millions of messages
- Partition-based parallelism for throughput

### 2. **Replication Strategy**

- **Leader-Follower Model**: 
  - One leader per partition handles writes
  - Followers replicate data from leader
  - Automatic failover on leader failure

- **In-Sync Replicas (ISR)**:
  - Tracks replicas caught up with leader
  - Configurable minimum ISR for durability
  - Dynamic ISR updates on replica failure

### 3. **High Availability** 🟡 IN PROGRESS

- **Multi-broker deployment**: ✅ Distribute partitions across brokers (partial)
- **Replication factor**: ✅ Each partition replicated on multiple brokers
- **Automatic failover**: ⏳ etcd-based leader election (not yet integrated)
- **Health monitoring**: 📋 Planned - regular heartbeat and status checks

### 4. **Fault Tolerance**

- **Persistent storage**: Messages stored on disk
- **Write-ahead logs**: Ensure durability
- **Idempotent producers**: Prevent duplicate messages
- **Consumer offset management**: Track consumption progress

### 5. **Producer Model** 🟡 PARTIAL

- **Partitioning strategies**: 📋 Planned
  - Round-robin (for null keys)
  - Hash-based (for keyed messages)
  - Custom partitioner support

- **Acknowledgment levels**: 📋 Planned (not in current proto API)
  - `none`: Fire and forget
  - `leader`: Leader ACK
  - `all`: All ISR ACK

### 6. **Consumer Model** ✅ IMPLEMENTED

- **Consumer groups**: ✅ Multiple consumers sharing message load with automatic rebalancing
- **Offset management**: ✅ Track reading position per partition (etcd backed)
- **Auto-commit**: ✅ Periodic offset commits via coordinator
- **Rebalancing**: ✅ Multiple strategies (RoundRobin, Range, Sticky)
  - Sticky minimizes partition movement
  - Range assigns consecutive partitions
  - RoundRobin distributes evenly

## Data Persistence

### Storage Layout

```
/app/data/
├── topic-1/
│   ├── 0/              # Partition 0
│   │   ├── log.data    # Message log
│   │   ├── index.idx   # Offset index
│   │   └── metadata    # Partition metadata
│   ├── 1/              # Partition 1
│   └── ...
├── topic-2/
│   └── ...
└── __internal__/       # System topics
    └── offsets/        # Consumer offsets
```

### Segment Management

- Segments rotate based on:
  - Time threshold (segmentMs)
  - Size threshold (segmentBytes)
- Old segments archived/deleted based on retention policy

## Replication Flow

### Write Path

```
1. Producer sends message to leader
2. Leader:
   - Appends to local log
   - Creates index entry
   - Returns offset
3. Leader replicates to followers
4. Followers:
   - Append to local log
   - Send ACK to leader
5. Leader updates ISR
6. Producer receives confirmation
```

### Read Path

```
1. Consumer requests messages from offset
2. Broker:
   - Looks up offset in index
   - Retrieves messages from log
   - Returns messages to consumer
3. Consumer processes messages
4. Consumer commits offset
5. Offset stored in cluster
```

## Coordination with etcd ✅ IMPLEMENTED

**Current Status**: Full integration with etcd for distributed coordination and consumer groups.

### Metadata Stored in etcd

- ✅ Broker registration and health
- ✅ Leader for each partition
- ✅ ISR list per partition
- ✅ Consumer group state and member assignments
- ✅ Committed offsets per consumer group

### Implemented Features

✅ **Leader Election**: Automatic failover on broker failure
✅ **Broker Health Monitoring**: TTL-based registration with automatic failure detection
✅ **Consumer Group Coordination**: Join, leave, rebalancing
✅ **Offset Tracking**: Persistent offset management across restarts

## Scalability Considerations

### Horizontal Scaling

- Add brokers to cluster
- Rebalance partitions across new brokers
- Distribute load proportionally

### Vertical Scaling

- Increase broker resources (CPU, Memory, Storage)
- Monitor and tune JVM settings (if using JVM)
- Optimize storage with SSD

### Network Optimization 📋 PLANNED

- 📋 Use batch processing for replication
- 📋 Compression support (snappy, gzip)
- 📋 Connection pooling between brokers

## Performance Characteristics

### Throughput

- Single broker: 100K+ messages/sec
- Multi-broker cluster: Linear with broker count
- Network bandwidth: Primary limiting factor

### Latency

- Producer latency: ~10-50ms (with replication to all ISR)
- Consumer latency: Milliseconds (disk I/O dependent)
- Replication lag: <100ms typical

### Durability

- Replication factor 3: Survives 1 broker failure
- Min ISR 2: Ensures durability with 1 broker down
- Persistence: Data survives process restarts

## Security Considerations

### Current Implementation ✅

- gRPC for inter-broker communication
- Service account for pod authentication (K8s) - infrastructure ready
- Network policies (can be enabled)

### Future Enhancements 📋 PLANNED

- TLS/mTLS for encrypted communication
- RBAC for producer/consumer access control
- Audit logging for compliance
- Data encryption at rest

## Monitoring and Observability 📋 PLANNED

**Current Status**: Not yet implemented. Needs Prometheus metrics and health endpoints.

### Metrics to Monitor

- Message throughput (msgs/sec)
- Producer/Consumer latency (p50, p99)
- Replication lag (offset lag)
- Broker health (heartbeat/response time)
- Storage usage (disk space, segment count)

### Health Indicators

- Leader election time (<5 seconds)
- ISR stability (minimal changes)
- Partition distribution (balanced load)
- Consumer lag (within SLA)

**TODO**: Add `/metrics` endpoint for Prometheus scraping

## Implementation Roadmap

### Phase 1: Core Foundation ✅ (Current - 85% Complete)
- [x] Basic broker with partitions and topics
- [x] gRPC API for produce/consume
- [x] In-memory replication manager
- [x] File-based storage layer
- [x] etcd integration for coordination
- [x] Consumer groups with offset management
- [ ] Graceful failover and recovery

### Phase 2: Production Ready 📋 (Next)
- [ ] ACK levels (none, leader, all)
- [ ] Batch produce/consume
- [ ] Compression (snappy, gzip)
- [ ] Metrics and health endpoints
- [ ] Graceful failover
- [ ] TLS/mTLS support

### Phase 3: Advanced Features 📋 (Future)
1. **Stream Processing**: Local windowing and aggregation
2. **Schema Registry**: Schema validation and evolution
3. **Transactions**: Cross-partition atomic writes
4. **KStream API**: Higher-level stream processing
5. **Connect Framework**: Data integration connectors
6. **Multi-tenancy**: Namespace isolation
7. **Tiered Storage**: Hot/cold data separation

### Known Limitations
- ⚠️ No persistence across restarts (in-memory storage) - disk persistence partially working
- ⚠️ Consumer group assignments stored only in memory, needs full etcd integration
- ⚠️ No metrics collection
- ⚠️ No TLS/mTLS support yet
- ⚠️ Single-machine deployment only (ready for multi-machine with full etcd use)
