# MessageBroker - Kafka-like Message Queue in Go

A distributed, highly available message queue system inspired by Apache Kafka, built in Go with support for millions of topics, partitions, replication, and fault tolerance.

## Features

- **Distributed Architecture**: Multiple brokers for scalability
- **High Availability**: Replication across brokers with leader-follower model
- **Fault Tolerance**: Automatic failover and recovery
- **Topic & Partition Management**: Support for millions of topics with multi-partition support
- **Producer API**: Publish messages with various strategies (round-robin, key-based, custom)
- **Consumer API**: Consume messages with consumer groups and offset management
- **gRPC Communication**: High-performance inter-broker and client communication
- **Persistent Storage**: In-disk message persistence with optional compression
- **Metadata Management**: Distributed coordination using etcd

## Project Structure

```
messagebroker/
├── cmd/
│   ├── broker/          # Broker server entry point
│   ├── producer/        # Producer client example
│   └── consumer/        # Consumer client example
├── pkg/
│   ├── broker/          # Core broker logic
│   ├── client/          # Producer and Consumer clients
│   ├── partition/       # Partition management
│   ├── replication/     # Replication logic
│   ├── storage/         # Storage and indexing
│   └── coordinator/     # Metadata coordination
├── deployment/
│   ├── docker/          # Dockerfile
│   ├── helm/            # Helm charts
│   └── ansible/         # Ansible playbooks
└── proto/               # Protocol buffer definitions
```

## Building

```bash
go build -o bin/broker ./cmd/broker
go build -o bin/producer ./cmd/producer
go build -o bin/consumer ./cmd/consumer
```

## Running

### Start Broker
```bash
./bin/broker --id 1 --port 9092 --coordinator-url localhost:2379
```

### Run Producer
```bash
./bin/producer --brokers localhost:9092
```

### Run Consumer
```bash
./bin/consumer --brokers localhost:9092 --topic my-topic --group my-group
```

## Deployment

### Docker
```bash
docker build -t messagebroker:latest -f deployment/docker/Dockerfile .
docker run -p 9092:9092 messagebroker:latest
```

### Kubernetes with Helm
```bash
helm install messagebroker deployment/helm/messagebroker \
  --namespace messagebroker \
  --create-namespace
```

### Ansible
```bash
ansible-playbook deployment/ansible/deploy.yml -i inventory.ini
```

## Architecture

### Broker
- Manages partitions assigned to it
- Handles leader election using etcd
- Replicates data to follower brokers
- Exposes gRPC API for clients

### Partition
- Immutable log of messages
- Leader handles writes
- Followers sync from leader
- Offset-based message retrieval

### Replication
- Leader-follower model
- In-sync replicas (ISR) tracking
- Configurable replication factor
- Automatic failure detection

### Storage
- Log-structured storage
- Index for fast offset lookup
- Disk persistence
- Optional compression (snappy/gzip)

## Configuration

See `config.yaml` for available configuration options.

## License

MIT
# messagebroker
