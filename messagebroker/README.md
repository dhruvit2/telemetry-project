# MessageBroker

A distributed, partition-based message broker written in Go — inspired by Apache Kafka's architecture. Built for high-throughput telemetry and event streaming pipelines.

```
Producer ──► Broker Cluster (gRPC) ──► Consumer Groups
                     │
              etcd (coordination)
```

---

## Features

- **Partitioned topics** — ordered, append-only partitions with configurable replication
- **Consumer groups** — automatic partition assignment, rebalancing, and offset tracking
- **Leader election** — etcd-backed Compare-And-Swap elections; TTL leases auto-expire dead brokers
- **In-Sync Replicas (ISR)** — tracks which replicas are caught up; configurable minimum ISR
- **gRPC API** — all produce, consume, and admin operations via a single service
- **Kubernetes-native** — StatefulSet-friendly; broker ID derived from pod ordinal
- **Mock coordinator** — in-process etcd replacement for local development (no etcd required)

---

## Quick Start

### Prerequisites

| Tool | Version |
|------|---------|
| Go | 1.22+ |
| etcd | 3.5+ |
| Docker | 20+ (optional) |
| kubectl + Helm | (optional, for K8s) |

### Run locally (single broker, mock coordinator)

```bash
# Clone and build
git clone https://github.com/dhruvit2/messagebroker
cd messagebroker
make build

# Start broker — no etcd needed in mock mode
./bin/broker -id 1 -coordinator-type=mock
```

### Run locally (with etcd)

```bash
# Start etcd (if not running)
etcd --advertise-client-urls http://localhost:2379 --listen-client-urls http://localhost:2379 &

# Start broker
make run-broker BROKER_ID=1 BROKER_PORT=9092
```

### Run a 3-broker cluster (Docker Compose)

```bash
make run-docker   # starts etcd + 3 brokers on ports 9092/9093/9094
make stop-docker
```

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Clients                          │
│   Producer        Consumer         Admin            │
└────────┬──────────────┬───────────────┬─────────────┘
         │              │  gRPC :9092   │
         ▼              ▼               ▼
┌─────────────────────────────────────────────────────┐
│              Broker gRPC Server                     │
│  pkg/broker  │  pkg/consumer  │  pkg/replication    │
│  pkg/storage │                │                     │
└──────────────────────┬──────────────────────────────┘
                       │ Coordinator API
                       ▼
              pkg/coordinator (EtcdCoordinator)
                       │
                       ▼
                  etcd cluster
```

See [`architecture.md`](./architecture.md) for a full deep-dive into every package, data flows, etcd key schema, and known limitations.

---

## Repository Layout

```
messagebroker/
├── cmd/
│   ├── broker/        # Broker entry point (gRPC server + startup wiring)
│   ├── producer/      # Producer CLI example
│   ├── consumer/      # Consumer CLI example
│   └── admin/         # Admin CLI
├── pkg/
│   ├── broker/        # Core domain — topics, partitions, messages
│   ├── coordinator/   # etcd + mock coordinator implementations
│   ├── consumer/      # Consumer group manager + rebalancer
│   ├── replication/   # ISR tracking, leader election
│   ├── storage/       # File-based segment log + offset index
│   └── pb/            # Protobuf / gRPC generated code
├── deployment/
│   ├── docker/        # Dockerfile + docker-compose
│   └── helm/          # Helm chart for Kubernetes
├── doc/               # Original design documents
├── architecture.md    # Detailed architecture reference
└── Makefile
```

---

## Configuration

All flags can be overridden with environment variables:

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `-id` | `BROKER_ID` | *(pod ordinal)* | Unique broker integer ID |
| `-host` | `BROKER_HOST` | `localhost` | Advertised hostname |
| `-port` | `BROKER_PORT` | `9092` | gRPC listen port |
| `-coordinator` | `COORDINATOR_URL` | `localhost:2379` | etcd endpoint |
| `-coordinator-type` | `COORDINATOR_TYPE` | `etcd` | `etcd` or `mock` |
| `-etcd-username` | `ETCD_USERNAME` | `""` | etcd auth username |
| `-etcd-password` | `ETCD_PASSWORD` | `""` | etcd auth password |
| `-data-dir` | `DATA_DIR` | `/tmp/messagebroker` | Storage root directory |

---

## gRPC API

```protobuf
service MessageBroker {
  rpc CreateTopic(CreateTopicRequest)           returns (CreateTopicResponse);
  rpc GetTopicMetadata(GetTopicMetadataRequest) returns (TopicMetadata);
  rpc BrokerMetadata(BrokerMetadataRequest)     returns (BrokerMetadataResponse);

  rpc ProduceMessage(ProduceRequest)            returns (ProduceResponse);
  rpc ConsumeMessages(ConsumeRequest)           returns (ConsumeResponse);

  rpc JoinConsumerGroup(JoinConsumerGroupRequest)       returns (JoinConsumerGroupResponse);
  rpc LeaveConsumerGroup(LeaveConsumerGroupRequest)     returns (LeaveConsumerGroupResponse);
  rpc FetchAssignments(FetchAssignmentsRequest)         returns (FetchAssignmentsResponse);
  rpc CommitOffset(CommitOffsetRequest)                 returns (CommitOffsetResponse);
  rpc FetchOffset(FetchOffsetRequest)                   returns (FetchOffsetResponse);

  rpc ListConsumerGroups(ListConsumerGroupsRequest)         returns (ListConsumerGroupsResponse);
  rpc DescribeConsumerGroup(DescribeConsumerGroupRequest)   returns (DescribeConsumerGroupResponse);
}
```

---

## Deployment

### Docker Compose

```bash
make run-docker          # start cluster (etcd + 3 brokers)
make stop-docker         # tear down
```

### Kubernetes (Helm)

```bash
make deploy-k8s          # helm install → namespace 'messagebroker'
make status-k8s          # kubectl get pods/svc
make update-k8s          # helm upgrade
make delete-k8s          # helm uninstall
```

Brokers are deployed as a `StatefulSet`. Pod names (`messagebroker-0`, `messagebroker-1`, …) map directly to broker IDs.

---

## Development

```bash
make build               # build all binaries → bin/{broker,producer,consumer}
make build-broker        # broker only
make test                # go test -v ./...
make lint                # golangci-lint
make proto               # regenerate protobuf code from .proto files
make clean               # remove build artifacts
```

### Run producer / consumer examples

```bash
# Broker must be running first
make run-producer        # sends 100 messages to 'test-topic'
make run-consumer        # reads from 'telemetry-data', group 'consumer-group-1'
```

---

## Implementation Status

| Feature | Status |
|---------|--------|
| Topics, partitions, in-memory messages | ✅ |
| gRPC API (all handlers) | ✅ |
| etcd coordinator (lease, CAS, watch) | ✅ |
| Consumer group join/leave/rebalance | ✅ |
| Round-robin partition assignment | ✅ |
| Committed offset storage (etcd) | ✅ |
| Storage log + index per partition | ✅ |
| Segment rotation (size-based) | ✅ |
| StatefulSet pod ordinal → broker ID | ✅ |
| Network replication (follower fetch) | ❌ |
| Disk flush / true persistence | ⚠️ partial |
| ACK levels (none / leader / all) | ❌ |
| Batch produce / consume | ❌ |
| Prometheus metrics endpoint | ❌ |
| TLS / mTLS | ❌ |
| Compression (snappy, gzip) | ❌ |

---

## License

MIT
