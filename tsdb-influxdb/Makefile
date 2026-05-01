.PHONY: help build docker-build docker-run docker-stop docker-clean helm-install helm-upgrade helm-uninstall local-run local-stop init-influx

# Configuration
DOCKER_COMPOSE_FILE := docker/docker-compose.yml
DOCKER_IMAGE_NAME := tsdb-influxdb
DOCKER_IMAGE_TAG := latest
DOCKER_IMAGE := $(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)
INFLUX_CONTAINER_NAME := influxdb-tsdb
INFLUX_PORT := 8086
INFLUX_ORG := telemetry
INFLUX_BUCKET := gpu_metrics_raw
INFLUX_RETENTION := 720h
INFLUX_TOKEN := 7h2UjNBHN7ApaRrwz49uyRi6sySH-NaICaNLz4ZP5ROt2Jf8lDfJqtyU_e-45STGcnvD71x5sa9dRlgb9H2kKg==
HELM_RELEASE_NAME := influxdb-tsdb
HELM_NAMESPACE := default
HELM_CHART_PATH := helm

# Colors for output
BLUE := \033[0;34m
GREEN := \033[0;32m
YELLOW := \033[0;33m
NC := \033[0m # No Color

help:
	@echo "$(BLUE)InfluxDB TSDB High Availability Setup$(NC)"
	@echo ""
	@echo "$(GREEN)Local Development:$(NC)"
	@echo "  make docker-build       - Build InfluxDB Docker image"
	@echo "  make docker-run         - Start InfluxDB container (docker-compose)"
	@echo "  make docker-stop        - Stop InfluxDB container"
	@echo "  make docker-clean       - Remove containers and volumes"
	@echo "  make local-run          - Run InfluxDB locally (requires influxd binary)"
	@echo "  make local-stop         - Stop local InfluxDB process"
	@echo "  make init-influx        - Initialize InfluxDB (org, bucket, token)"
	@echo ""
	@echo "$(GREEN)Kubernetes/Helm:$(NC)"
	@echo "  make helm-install       - Install InfluxDB using Helm"
	@echo "  make helm-upgrade       - Upgrade InfluxDB Helm release"
	@echo "  make helm-uninstall     - Uninstall InfluxDB Helm release"
	@echo ""
	@echo "$(GREEN)Monitoring:$(NC)"
	@echo "  make status             - Show container/service status"
	@echo "  make logs               - Show InfluxDB logs"
	@echo "  make health-check       - Health check against InfluxDB"

# Docker targets
docker-build:
	@echo "$(BLUE)Building InfluxDB Docker image...$(NC)"
	docker build -f docker/Dockerfile -t $(DOCKER_IMAGE) .

docker-run: docker-build
	@echo "$(BLUE)Starting InfluxDB container with docker-compose...$(NC)"
	#docker-compose -f $(DOCKER_COMPOSE_FILE) up -d
	docker run -d \
  		--name influxdb-tsdb \
  		--network tsdb-network \
  		-p 8086:8086 \
  		-e DOCKER_INFLUXDB_INIT_MODE=setup \
  		-e DOCKER_INFLUXDB_INIT_USERNAME=admin \
  		-e DOCKER_INFLUXDB_INIT_PASSWORD=password1234 \
  		-e DOCKER_INFLUXDB_INIT_ORG=telemetry \
  		-e DOCKER_INFLUXDB_INIT_BUCKET=gpu_metrics_raw \
  		-e DOCKER_INFLUXDB_INIT_RETENTION=7200h \
  		-e DOCKER_INFLUXDB_INIT_ADMIN_TOKEN=7h2UjNBHN7ApaRrwz49uyRi6sySH-NaICaNLz4ZP5ROt2Jf8lDfJqtyU_e-45STGcnvD71x5sa9dRlgb9H2kKg== \
  		$(DOCKER_IMAGE)
	@echo "$(GREEN)InfluxDB started on http://localhost:$(INFLUX_PORT)$(NC)"
	@echo "$(YELLOW)Waiting for InfluxDB to be ready...$(NC)"
	@sleep 5
	@$(MAKE) health-check
	@$(MAKE) init-influx

docker-stop:
	@echo "$(BLUE)Stopping InfluxDB container...$(NC)"
	docker-compose -f $(DOCKER_COMPOSE_FILE) down

docker-clean:
	@echo "$(BLUE)Cleaning up InfluxDB containers and volumes...$(NC)"
	docker-compose -f $(DOCKER_COMPOSE_FILE) down -v
	@echo "$(GREEN)Cleanup complete$(NC)"

# Local development
local-run:
	@echo "$(BLUE)Starting InfluxDB locally...$(NC)"
	@mkdir -p ./data
	influxd --bolt-path ./data/bolt.db --engine-path ./data/engine &
	@echo "$(GREEN)InfluxDB started (PID: $$!)$(NC)"
	@echo "$(YELLOW)Waiting for InfluxDB to be ready...$(NC)"
	@sleep 3
	@$(MAKE) health-check
	@$(MAKE) init-influx

local-stop:
	@echo "$(BLUE)Stopping local InfluxDB...$(NC)"
	pkill -f influxd || true
	@echo "$(GREEN)InfluxDB stopped$(NC)"

# Initialization
init-influx:
	@echo "$(BLUE)Initializing InfluxDB...$(NC)"
	@scripts/init-influx.sh "$(INFLUX_ORG)" "$(INFLUX_BUCKET)" "$(INFLUX_TOKEN)"

# Helm targets
helm-install:
	@echo "$(BLUE)Installing InfluxDB with Helm...$(NC)"
	helm install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) \
		--namespace $(HELM_NAMESPACE) \
		--set influxdb.adminUser.password=admin123 \
		--set influxdb.persistence.size=10Gi
	@echo "$(GREEN)Helm installation complete$(NC)"

helm-upgrade:
	@echo "$(BLUE)Upgrading InfluxDB Helm release...$(NC)"
	helm upgrade $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) \
		--namespace $(HELM_NAMESPACE)
	@echo "$(GREEN)Helm upgrade complete$(NC)"

helm-uninstall:
	@echo "$(BLUE)Uninstalling InfluxDB Helm release...$(NC)"
	helm uninstall $(HELM_RELEASE_NAME) --namespace $(HELM_NAMESPACE)
	@echo "$(GREEN)Helm uninstall complete$(NC)"

# Monitoring
status:
	@echo "$(BLUE)Container Status:$(NC)"
	@docker-compose -f $(DOCKER_COMPOSE_FILE) ps

logs:
	@echo "$(BLUE)InfluxDB Logs:$(NC)"
	@docker-compose -f $(DOCKER_COMPOSE_FILE) logs -f influxdb

health-check:
	@echo "$(BLUE)Checking InfluxDB health...$(NC)"
	@curl -s http://localhost:$(INFLUX_PORT)/health | jq . || echo "$(YELLOW)InfluxDB not ready yet$(NC)"

.PHONY: docker-build docker-run docker-stop docker-clean local-run local-stop init-influx helm-install helm-upgrade helm-uninstall status logs health-check help
