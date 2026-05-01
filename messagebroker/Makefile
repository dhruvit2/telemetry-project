.PHONY: help build build-docker push-docker deploy-k8s deploy-ansible clean test lint run-broker run-producer run-consumer run-local

DOCKER_REGISTRY ?= docker.io
DOCKER_IMAGE ?= messagebroker
VERSION ?= 0.0.1
K8S_NAMESPACE ?= messagebroker
HELM_RELEASE ?= messagebroker

# Local run configuration
BROKER_ID ?= 1
BROKER_HOST ?= localhost
BROKER_PORT ?= 9092
COORDINATOR_URL ?= localhost:2379
DATA_DIR ?= /tmp/messagebroker_test

help:
	@echo "MessageBroker Build and Deployment Targets"
	@echo "=========================================="
	@echo ""
	@echo "Building:"
	@echo "  make build              - Build all Go binaries"
	@echo "  make build-broker       - Build broker binary"
	@echo "  make build-producer     - Build producer binary"
	@echo "  make build-consumer     - Build consumer binary"
	@echo ""
	@echo "Local Development:"
	@echo "  make run-broker         - Run broker locally (port 9092)"
	@echo "  make run-producer       - Run producer example"
	@echo "  make run-consumer       - Run consumer example (connects to broker on 9092)"
	@echo "  make run-local          - Build and run broker (quick start)"
	@echo ""
	@echo "Docker:"
	@echo "  make build-docker       - Build Docker image"
	@echo "  make push-docker        - Push Docker image to registry"
	@echo "  make run-docker         - Run broker in Docker"
	@echo ""
	@echo "Kubernetes:"
	@echo "  make deploy-k8s         - Deploy to Kubernetes using Helm"
	@echo "  make update-k8s         - Update Kubernetes deployment"
	@echo "  make delete-k8s         - Delete Kubernetes deployment"
	@echo "  make status-k8s         - Get Kubernetes deployment status"
	@echo ""
	@echo "Ansible:"
	@echo "  make deploy-ansible     - Deploy using Ansible playbooks"
	@echo "  make status-ansible     - Check Ansible deployment status"
	@echo ""
	@echo "Development:"
	@echo "  make test               - Run tests"
	@echo "  make lint               - Run linters"
	@echo "  make clean              - Clean build artifacts"
	@echo ""

# Building targets
build: build-broker build-producer build-consumer

build-broker:
	@echo "Building broker binary..."
	@mkdir -p bin
	@go build -o bin/broker ./cmd/broker
	@echo "✓ Broker binary built: bin/broker"

build-producer:
	@echo "Building producer binary..."
	@mkdir -p bin
	@go build -o bin/producer ./cmd/producer
	@echo "✓ Producer binary built: bin/producer"

build-consumer:
	@echo "Building consumer binary..."
	@mkdir -p bin
	@go build -o bin/consumer ./cmd/consumer
	@echo "✓ Consumer binary built: bin/consumer"

# Docker targets
build-docker:
	@echo "Building Docker image: $(DOCKER_REGISTRY)/$(DOCKER_IMAGE):$(VERSION)"
	@docker build -t $(DOCKER_REGISTRY)/$(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_REGISTRY)/$(DOCKER_IMAGE):latest \
		-f deployment/docker/Dockerfile .
	@echo "✓ Docker image built successfully"

push-docker: build-docker
	@echo "Pushing Docker image to registry..."
	@docker push $(DOCKER_REGISTRY)/$(DOCKER_IMAGE):$(VERSION)
	@docker push $(DOCKER_REGISTRY)/$(DOCKER_IMAGE):latest
	@echo "✓ Docker image pushed successfully"

docker-run: build-docker
	@echo "Starting MessageBroker in Docker..."
	docker run -d --name messagebroker --network=tsdb-network \
		-p 9092:9092 \
		-e BROKER_ID=1 \
		-e BROKER_HOST=messagebroker \
		-e BROKER_PORT=9092 \
		-e COORDINATOR_URL=localhost:2379 \
		-e DATA_DIR=/tmp/messagebroker_test \
		$(DOCKER_REGISTRY)/$(DOCKER_IMAGE):$(VERSION)
	@echo "✓ MessageBroker started in Docker"
	@echo "  Service: messagebroker"
	@echo "  Network: tsdb-network"
	@echo "  Port: 9092"

docker-stop:
	@echo "Stopping MessageBroker container..."
	docker stop messagebroker || true
	docker rm messagebroker || true
	@echo "✓ MessageBroker stopped"

run-docker:
	@echo "Starting MessageBroker cluster with Docker Compose..."
	@docker-compose -f deployment/docker/docker-compose.yml up -d
	@echo "✓ MessageBroker cluster started"
	@echo "  Broker 1: localhost:9092"
	@echo "  Broker 2: localhost:9093"
	@echo "  Broker 3: localhost:9094"
	@echo "  etcd: localhost:2379"

stop-docker:
	@docker-compose -f deployment/docker/docker-compose.yml down
	@echo "✓ MessageBroker cluster stopped"

# Kubernetes targets
deploy-k8s:
	@echo "Creating Kubernetes namespace..."
	@kubectl create namespace $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "Deploying MessageBroker to Kubernetes..."
	@helm install $(HELM_RELEASE) deployment/helm/messagebroker \
		--namespace $(K8S_NAMESPACE) \
		--values deployment/helm/messagebroker/values.yaml
	@echo "✓ MessageBroker deployed to Kubernetes"

update-k8s:
	@echo "Updating MessageBroker deployment..."
	@helm upgrade $(HELM_RELEASE) deployment/helm/messagebroker \
		--namespace $(K8S_NAMESPACE) \
		--values deployment/helm/messagebroker/values.yaml
	@echo "✓ MessageBroker deployment updated"

delete-k8s:
	@echo "Deleting MessageBroker deployment..."
	@helm uninstall $(HELM_RELEASE) --namespace $(K8S_NAMESPACE)
	@echo "✓ MessageBroker deployment deleted"

status-k8s:
	@kubectl get pods -n $(K8S_NAMESPACE) -l app=messagebroker
	@echo ""
	@kubectl get svc -n $(K8S_NAMESPACE)

# Ansible targets
deploy-ansible:
	@echo "Deploying MessageBroker using Ansible..."
	@chmod +x deployment/ansible/run.sh
	@deployment/ansible/run.sh deployment/ansible/deploy.yml deployment/ansible/inventory.ini
	@echo "✓ MessageBroker deployed using Ansible"

status-ansible:
	@echo "Checking MessageBroker cluster status..."
	@chmod +x deployment/ansible/health-check.sh
	@deployment/ansible/health-check.sh

# Development targets
test:
	@echo "Running tests..."
	@go test -v ./...

lint:
	@echo "Running linters..."
	@golangci-lint run ./...

clean:
	@echo "Cleaning build artifacts..."
	@rm -rf bin/
	@go clean
	@echo "✓ Clean complete"

# Additional targets
proto:
	@echo "Generating protobuf code..."
#	@protoc --go_out=. --go-grpc_out=. pkg/pb/messagebroker.proto
	@protoc --proto_path=. --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    pkg/pb/messagebroker.proto
	@echo "✓ Protobuf code generated"

install-deps:
	@echo "Installing Go dependencies..."
	@go mod download
	@go mod tidy
	@echo "✓ Dependencies installed"

# Local run targets (for development and testing)
run-broker: build-broker
	@echo "Starting broker on $(BROKER_HOST):$(BROKER_PORT)..."
	@./bin/broker -id $(BROKER_ID) -host $(BROKER_HOST) -port $(BROKER_PORT) -coordinator $(COORDINATOR_URL) -data-dir $(DATA_DIR)

run-producer: build-producer
	@echo "Running producer..."
	@./bin/producer -brokers $(BROKER_HOST):$(BROKER_PORT) -topic test-topic -messages 100

run-consumer: build-consumer
	@echo "Running consumer..."
	@./bin/consumer -brokers $(BROKER_HOST):$(BROKER_PORT) -topics telemetry-data -group consumer-group-1

run-local: build
	@echo "Running MessageBroker locally..."
	@echo "Broker will listen on $(BROKER_HOST):$(BROKER_PORT)"
	@./bin/broker -id $(BROKER_ID) -host $(BROKER_HOST) -port $(BROKER_PORT) -coordinator $(COORDINATOR_URL) -data-dir $(DATA_DIR)
