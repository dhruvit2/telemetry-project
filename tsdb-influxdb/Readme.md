# TSDB - InfluxDB High Availability Repository

High Availability Time Series Database (TSDB) setup for GPU telemetry metrics collection and storage using InfluxDB v2.7+.

## Quick Start

### Docker Development

```bash
# Build and start InfluxDB with docker-compose
make docker-run

# Verify health
make health-check

# Initialize org/bucket (auto-run after docker-run)
make init-influx

# Stop containers
make docker-stop

# Clean up (remove volumes)
make docker-clean
```

**Access InfluxDB UI:**
- URL: http://localhost:8086
- Username: `admin`
- Password: `admin123`
- Org: `telemetry`
- Bucket: `gpu_metrics_raw`

### Configuration

Default environment variables (from docker-compose.yml):

```bash
INFLUX_URL=http://localhost:8086
INFLUX_ORG=telemetry
INFLUX_BUCKET=gpu_metrics_raw
INFLUX_TOKEN=my-super-secret-token
INFLUX_USERNAME=admin
INFLUX_PASSWORD=admin123
```

## Architecture

### High Availability Setup

The Helm chart supports HA configuration with:

- **Multi-replica deployments** (configurable in values.yaml)
- **Pod anti-affinity** to spread replicas across nodes
- **Persistent volumes** for data durability
- **Health checks** (liveness + readiness probes)
- **Rolling updates** for zero-downtime upgrades

Enable HA in `helm/values.yaml`:

```yaml
ha:
  enabled: true
  replicaCount: 3
```

### Data Retention

- **Retention Policy**: 30 days (configurable via `INFLUX_ADMIN_RETENTION`)
- **Storage Path**: `/var/lib/influxdb2` (mounted volume)
- **Backup**: Optional scheduled backups (configurable)

## Kubernetes/Helm Deployment

### Install

```bash
# Install InfluxDB using Helm
make helm-install

# Verify
kubectl get pods -l app=influxdb-tsdb
kubectl get svc influxdb-tsdb
```

### Upgrade

```bash
# Upgrade configuration or version
make helm-upgrade
```

### Uninstall

```bash
make helm-uninstall
```

### Accessing in Kubernetes

From within cluster:
- **Service URL**: `http://influxdb-tsdb:8086`
- **Organization**: `telemetry`
- **Bucket**: `gpu_metrics_raw`
- **Token**: `my-super-secret-token`

From outside cluster (port-forward):

```bash
kubectl port-forward svc/influxdb-tsdb 8086:8086
# Access at http://localhost:8086
```

## Integration with Services

### Environment Variables for Services

Services connecting to TSDB should use these variables:

**For Docker (container DNS):**
```bash
TSDB_URL=http://influxdb:8086
TSDB_ORG=telemetry
TSDB_BUCKET=gpu_metrics_raw
TSDB_TOKEN=my-super-secret-token
```

**For Local Development:**
```bash
TSDB_URL=http://localhost:8086
TSDB_ORG=telemetry
TSDB_BUCKET=gpu_metrics_raw
TSDB_TOKEN=my-super-secret-token
```

**For Kubernetes:**
```bash
TSDB_URL=http://influxdb-tsdb:8086
TSDB_ORG=telemetry
TSDB_BUCKET=gpu_metrics_raw
TSDB_TOKEN=my-super-secret-token
```

## Local Development without Docker

### Prerequisites

```bash
# Install influxdb locally (macOS)
brew install influxdb-cli

# Or download binary from https://portal.influxdata.com/downloads/
```

### Run Locally

```bash
# Start InfluxDB
make local-run

# Initialize
make init-influx

# Stop
make local-stop
```

## Monitoring & Troubleshooting

### View Logs

```bash
make logs
```

### Health Check

```bash
make health-check
```

### Container Status

```bash
make status
```

### Manual Health Check

```bash
curl http://localhost:8086/health | jq .
```

## File Structure

```
tsdb-influxdb/
├── Makefile                 # Build and deployment automation
├── Readme.md               # This file
├── docker/
│   ├── Dockerfile          # InfluxDB Docker image
│   └── docker-compose.yml  # Local development orchestration
├── helm/
│   ├── Chart.yaml          # Helm chart metadata
│   ├── values.yaml         # Default configuration
│   └── templates/
│       ├── deployment.yaml # Kubernetes deployment
│       ├── service.yaml    # Kubernetes service
│       ├── pvc.yaml        # Persistent volume claim
│       ├── secret.yaml     # Secret for credentials
│       ├── serviceaccount.yaml
│       └── _helpers.tpl    # Helm template helpers
└── scripts/
    └── init-influx.sh      # InfluxDB initialization script
```

## Performance Tuning

### Resource Limits

Adjust in `helm/values.yaml`:

```yaml
resources:
  limits:
    cpu: 4000m
    memory: 4Gi
  requests:
    cpu: 1000m
    memory: 1Gi
```

### Storage

For production, increase PVC size:

```yaml
influxdb:
  persistence:
    size: 100Gi  # Adjust based on retention + data volume
```

### Retention Policy

Adjust retention in Makefile:

```makefile
INFLUX_RETENTION := 2592000  # Seconds (30 days)
```

## Security

### Token Management

The admin token (`my-super-secret-token`) is stored in:
- Docker Compose: `docker/docker-compose.yml`
- Kubernetes Secret: `helm/templates/secret.yaml`

**For production**, use proper secret management:

```bash
# Kubernetes - use proper secret backend
kubectl create secret generic influxdb-token \
  --from-literal=admin-token=<your-token>
```

### Network Policies

In production, implement network policies to restrict access to port 8086.

## Advanced Topics

### Backup & Recovery

Manual backup (in Docker):

```bash
docker exec influxdb-tsdb influx backup /tmp/backup
docker cp influxdb-tsdb:/tmp/backup ./backups/
```

### Custom Configuration

Modify `docker/docker-compose.yml` environment variables for:
- Admin credentials
- Organization/bucket names
- Token values
- Retention periods

### Multi-Cluster Setup

For multi-cluster HA, use separate TSDB instances with:
- Cross-cluster replication (InfluxDB Enterprise)
- Federated queries
- Shared backup storage

## Support & Documentation

- InfluxDB Docs: https://docs.influxdata.com/influxdb/v2/
- Helm Chart Docs: https://github.com/influxdata/helm-charts
- Go Client: https://github.com/influxdata/influxdb-client-go

## License

See parent project license.
