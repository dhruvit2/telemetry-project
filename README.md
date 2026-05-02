# Telemetry Project

A distributed, Kafka-inspired telemetry ingestion and query pipeline built in Go,
deployed on Kubernetes via Helm.

## Components

| Service | Description | Port |
|---------|-------------|------|
| `telemetry-streaming` | Streams CSV telemetry data into the message broker | 8080 |
| `messagebroker` | Custom distributed message broker (gRPC, etcd-coordinated) | 9092 |
| `telemetry-etcd` | etcd cluster — broker coordination & consumer group state | 2379 |
| `telemetry-collector` | Consumes from broker (×3 replicas), writes to InfluxDB | 8080 |
| `influxdb-tsdb` | Time-series database (InfluxDB v2.7) | 8086 |
| `telemetry-api` | REST API to query GPU telemetry from InfluxDB | 8080 |

## Documentation

- **[ARCHITECTURE.md](./ARCHITECTURE.md)** — Full system architecture diagram, data flow, package map, gRPC API, and config reference
- **[messagebroker/architecture.md](./messagebroker/architecture.md)** — Deep-dive into the message broker internals

## Quick Start — One-Shot Deploy / Clean

```bash
# Deploy everything (correct dependency order)
./manage.sh deploy

# Deploy into a custom namespace
NAMESPACE=telemetry ./manage.sh deploy

# Preview what will be deployed (no changes)
./manage.sh deploy --dry-run

# Show status of all running components
./manage.sh status

# Tear everything down (prompts for confirmation, including optional PVC deletion)
./manage.sh clean
```

> **Requirements**: `kubectl` (configured), `helm 3+`, `bitnami` helm repo (added automatically if missing)

## Data Flow (Summary)

```
CSV file → telemetry-streaming → messagebroker (topic: telemetry-data, 3 partitions)
                                        ↓  (consumer group: telemetry-collectors)
                                 telemetry-collector ×3
                                        ↓  (InfluxDB Line Protocol)
                                 influxdb-tsdb  (org: telemetry, bucket: gpu_metrics_raw)
                                        ↓  (Flux / InfluxQL queries)
                                 telemetry-api  (REST)
```

## Repository Layout

```
telemetry-project/
├── manage.sh                    ← one-shot deploy/clean/status script
├── ARCHITECTURE.md              ← full architecture diagram
├── messagebroker/               ← custom gRPC message broker (Go)
├── telemetry-etcd/              ← etcd Helm values (bitnami)
├── telemetry-streaming/         ← CSV streamer → broker producer (Go)
├── telemetry-collector/         ← broker consumer → InfluxDB writer (Go)
├── tsdb-influxdb/               ← InfluxDB Helm chart + docker setup
└── telemetry-api/               ← REST query API (Go)
```

## Storage class and resource update

```
telemetry-project/
├── messagebroker/deployment/helm/messagebroker/values.yaml
├── telemetry-etcd/values.yaml
├── telemetry-streaming/deployment/helm/telemetry-streaming/values.yaml
├── telemetry-collector/deployment/helm/telemetry-collector/values.yaml
├── tsdb-influxdb/helm/values.yaml
└── telemetry-api/helm/deployment/values.yaml
```


## Images

All images are on Docker Hub under `dhruvit2/`:

| Image | Tag |
|-------|-----|
| `dhruvit2/messagebroker` | `5.0.16` |
| `dhruvit2/telemetry-collector` | `5.0.17` |
| `dhruvit2/telemetry-streaming` | `5.0.16` |
| `dhruvit2/telemetry-api` | `5.0.19` |
| `influxdb` | `2.7` |

## API access

Swagger to access APIs

```
Do a port forward from cluster and access below API
http://localhost:3006/docs
```