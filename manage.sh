#!/usr/bin/env bash
# =============================================================================
# manage.sh — Unified deploy / clean script for the Telemetry Project
#
# Usage:
#   ./manage.sh deploy          — Deploy ALL components in correct order
#   ./manage.sh clean           — Remove ALL components (reverse order)
#   ./manage.sh status          — Show status of all deployed components
#   ./manage.sh deploy --dry-run — Print what would be deployed (no changes)
#
# Requirements: kubectl, helm, bitnami helm repo added
# =============================================================================

set -euo pipefail

# ── Colours ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

log()     { echo -e "${CYAN}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }
banner()  { echo -e "\n${BOLD}${CYAN}══════════════════════════════════════${NC}"; \
            echo -e "${BOLD}${CYAN}  $*${NC}"; \
            echo -e "${BOLD}${CYAN}══════════════════════════════════════${NC}\n"; }

# ── Configuration ─────────────────────────────────────────────────────────────
NAMESPACE="${NAMESPACE:-default}"
DRY_RUN=false

# Helm release names
ETCD_RELEASE="telemetry-etcd"
BROKER_RELEASE="messagebroker"
INFLUXDB_RELEASE="influxdb-tsdb"
COLLECTOR_RELEASE="telemetry-collector"
STREAMING_RELEASE="telemetry-streaming"
API_RELEASE="telemetry-api"

# Helm chart paths (relative to project root)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ETCD_VALUES="$SCRIPT_DIR/telemetry-etcd/values.yaml"
BROKER_CHART="$SCRIPT_DIR/messagebroker/deployment/helm/messagebroker"
INFLUXDB_CHART="$SCRIPT_DIR/tsdb-influxdb/helm"
COLLECTOR_CHART="$SCRIPT_DIR/telemetry-collector/deployment/helm/telemetry-collector"
STREAMING_CHART="$SCRIPT_DIR/telemetry-streaming/deployment/helm/telemetry-streaming"
API_CHART="$SCRIPT_DIR/telemetry-api/helm/deployment"

# Wait timeouts (seconds)
ETCD_WAIT=120
BROKER_WAIT=90
INFLUX_WAIT=120
COLLECTOR_WAIT=90
STREAMING_WAIT=60
API_WAIT=60

# ── Helpers ───────────────────────────────────────────────────────────────────
helm_install_or_upgrade() {
    local release="$1"; shift
    local chart="$1"; shift
    local extra_args=("$@")

    if $DRY_RUN; then
        log "[DRY-RUN] helm upgrade --install $release $chart --namespace $NAMESPACE ${extra_args[*]}"
        return
    fi

    if helm status "$release" --namespace "$NAMESPACE" &>/dev/null; then
        log "Upgrading existing release: $release"
        helm upgrade "$release" "$chart" \
            --namespace "$NAMESPACE" \
            "${extra_args[@]}"
    else
        log "Installing new release: $release"
        helm install "$release" "$chart" \
            --namespace "$NAMESPACE" \
            --create-namespace \
            "${extra_args[@]}"
    fi
    success "Helm release '$release' applied."
}

wait_for_pods() {
    local label="$1"
    local timeout="$2"
    if $DRY_RUN; then
        log "[DRY-RUN] Would wait for pods with label: $label (timeout: ${timeout}s)"
        return
    fi
    log "Waiting for pods ($label) to be ready (timeout: ${timeout}s)..."
    if kubectl wait pod \
        --selector="$label" \
        --for=condition=Ready \
        --timeout="${timeout}s" \
        --namespace "$NAMESPACE" 2>/dev/null; then
        success "Pods ($label) are ready."
    else
        warn "Pods ($label) did not become ready within ${timeout}s. Continuing anyway."
    fi
}

helm_uninstall() {
    local release="$1"
    if $DRY_RUN; then
        log "[DRY-RUN] helm uninstall $release --namespace $NAMESPACE"
        return
    fi
    if helm status "$release" --namespace "$NAMESPACE" &>/dev/null; then
        log "Uninstalling release: $release"
        helm uninstall "$release" --namespace "$NAMESPACE"
        success "Release '$release' uninstalled."
    else
        warn "Release '$release' not found in namespace '$NAMESPACE'. Skipping."
    fi
}

check_prerequisites() {
    local missing=()
    command -v kubectl &>/dev/null || missing+=("kubectl")
    command -v helm    &>/dev/null || missing+=("helm")
    if [[ ${#missing[@]} -gt 0 ]]; then
        error "Missing required tools: ${missing[*]}"
        exit 1
    fi

    # Check cluster connectivity
    if ! kubectl cluster-info &>/dev/null; then
        error "Cannot reach Kubernetes cluster. Check your kubeconfig."
        exit 1
    fi
}

check_bitnami_repo() {
    if ! helm repo list 2>/dev/null | grep -q 'bitnami'; then
        warn "bitnami helm repo not found. Adding it now..."
        helm repo add bitnami https://charts.bitnami.com/bitnami
        helm repo update
        success "bitnami repo added."
    fi
}

# ── Deploy ────────────────────────────────────────────────────────────────────
deploy_all() {
    banner "Deploying Telemetry Pipeline"
    check_prerequisites
    check_bitnami_repo

    # ── 1. etcd (coordination backbone — must come first) ─────────────────────
    log "Step 1/6 — Deploying etcd cluster ($ETCD_RELEASE)..."
    if $DRY_RUN; then
        log "[DRY-RUN] helm upgrade --install $ETCD_RELEASE bitnami/etcd --namespace $NAMESPACE -f $ETCD_VALUES"
    else
        if helm status "$ETCD_RELEASE" --namespace "$NAMESPACE" &>/dev/null; then
            helm upgrade "$ETCD_RELEASE" bitnami/etcd \
                --namespace "$NAMESPACE" \
                -f "$ETCD_VALUES"
        else
            helm install "$ETCD_RELEASE" bitnami/etcd \
                --namespace "$NAMESPACE" \
                --create-namespace \
                -f "$ETCD_VALUES"
        fi
        success "etcd release applied."
    fi
    wait_for_pods "app.kubernetes.io/name=etcd" "$ETCD_WAIT"

    # ── 2. InfluxDB TSDB (storage — before collector) ─────────────────────────
    log "Step 2/6 — Deploying InfluxDB TSDB ($INFLUXDB_RELEASE)..."
    helm_install_or_upgrade "$INFLUXDB_RELEASE" "$INFLUXDB_CHART" \
        --values "$INFLUXDB_CHART/values.yaml"
    wait_for_pods "app.kubernetes.io/name=influxdb-tsdb" "$INFLUX_WAIT"

    # ── 3. MessageBroker (before streaming and collector) ─────────────────────
    log "Step 3/6 — Deploying MessageBroker ($BROKER_RELEASE)..."
    helm_install_or_upgrade "$BROKER_RELEASE" "$BROKER_CHART" \
        --values "$BROKER_CHART/values.yaml"
    wait_for_pods "app.kubernetes.io/name=messagebroker" "$BROKER_WAIT"

    # ── 4. Telemetry Collector (consumer from broker, writes to influx) ────────
    log "Step 4/6 — Deploying Telemetry Collector ($COLLECTOR_RELEASE)..."
    helm_install_or_upgrade "$COLLECTOR_RELEASE" "$COLLECTOR_CHART" \
        --values "$COLLECTOR_CHART/values.yaml"
    wait_for_pods "app.kubernetes.io/name=telemetry-collector" "$COLLECTOR_WAIT"

    # ── 5. Telemetry Streaming (producer to broker) ───────────────────────────
    log "Step 5/6 — Deploying Telemetry Streaming ($STREAMING_RELEASE)..."
    helm_install_or_upgrade "$STREAMING_RELEASE" "$STREAMING_CHART" \
        --values "$STREAMING_CHART/values.yaml"
    wait_for_pods "app.kubernetes.io/name=telemetry-streaming" "$STREAMING_WAIT"

    # ── 6. Telemetry API (query layer) ───────────────────────────────────────
    log "Step 6/6 — Deploying Telemetry API ($API_RELEASE)..."
    helm_install_or_upgrade "$API_RELEASE" "$API_CHART" \
        --values "$API_CHART/values.yaml"
    wait_for_pods "app.kubernetes.io/name=telemetry-api" "$API_WAIT"

    # ── Summary ───────────────────────────────────────────────────────────────
    banner "Deployment Complete ✓"
    if ! $DRY_RUN; then
        show_status
    fi
}

# ── Clean ─────────────────────────────────────────────────────────────────────
clean_all() {
    banner "Cleaning Telemetry Pipeline"
    check_prerequisites

    warn "This will uninstall ALL telemetry Helm releases from namespace: $NAMESPACE"
    if ! $DRY_RUN; then
        read -r -p "Are you sure? (yes/no): " confirm
        [[ "$confirm" == "yes" ]] || { log "Aborted."; exit 0; }
    fi

    # Reverse order: API → Streaming → Collector → Broker → InfluxDB → etcd
    log "Step 1/6 — Removing Telemetry API ($API_RELEASE)..."
    helm_uninstall "$API_RELEASE"

    log "Step 2/6 — Removing Telemetry Streaming ($STREAMING_RELEASE)..."
    helm_uninstall "$STREAMING_RELEASE"

    log "Step 3/6 — Removing Telemetry Collector ($COLLECTOR_RELEASE)..."
    helm_uninstall "$COLLECTOR_RELEASE"

    log "Step 4/6 — Removing MessageBroker ($BROKER_RELEASE)..."
    helm_uninstall "$BROKER_RELEASE"

    log "Step 5/6 — Removing InfluxDB TSDB ($INFLUXDB_RELEASE)..."
    helm_uninstall "$INFLUXDB_RELEASE"

    log "Step 6/6 — Removing etcd ($ETCD_RELEASE)..."
    helm_uninstall "$ETCD_RELEASE"

    if ! $DRY_RUN; then
        echo ""
        warn "Do you also want to delete all PersistentVolumeClaims (PVCs)?"
        warn "This will result in PERMANENT DATA LOSS for etcd, messagebroker, and influxdb."
        read -r -p "Delete PVCs? (yes/no): " confirm_pvc
        if [[ "$confirm_pvc" == "yes" ]]; then
            log "Deleting PVCs..."
            kubectl delete pvc -n "$NAMESPACE" -l app.kubernetes.io/instance=$ETCD_RELEASE || true
            kubectl delete pvc -n "$NAMESPACE" -l app.kubernetes.io/instance=$BROKER_RELEASE || true
            kubectl delete pvc -n "$NAMESPACE" -l app.kubernetes.io/instance=$INFLUXDB_RELEASE || true
            kubectl delete pvc -n "$NAMESPACE" -l app.kubernetes.io/instance=$COLLECTOR_RELEASE || true
            success "PVCs deleted."
        else
            log "Skipping PVC deletion."
            warn "To delete PVCs manually later, run:"
            warn "  kubectl delete pvc -n $NAMESPACE -l app.kubernetes.io/instance=$ETCD_RELEASE"
            warn "  kubectl delete pvc -n $NAMESPACE -l app.kubernetes.io/instance=$BROKER_RELEASE"
            warn "  kubectl delete pvc -n $NAMESPACE -l app.kubernetes.io/instance=$INFLUXDB_RELEASE"
            warn "  kubectl delete pvc -n $NAMESPACE -l app.kubernetes.io/instance=$COLLECTOR_RELEASE"
        fi
        echo ""
    fi

    banner "Cleanup Complete ✓"
}

# ── Status ────────────────────────────────────────────────────────────────────
show_status() {
    banner "Telemetry Pipeline Status"
    check_prerequisites

    echo -e "${BOLD}Helm Releases (namespace: $NAMESPACE)${NC}"
    helm list --namespace "$NAMESPACE" \
        --filter "^($ETCD_RELEASE|$BROKER_RELEASE|$INFLUXDB_RELEASE|$COLLECTOR_RELEASE|$STREAMING_RELEASE|$API_RELEASE)$" \
        2>/dev/null || warn "Could not list helm releases."

    echo ""
    echo -e "${BOLD}Pods${NC}"
    kubectl get pods --namespace "$NAMESPACE" \
        --selector="app.kubernetes.io/instance in ($ETCD_RELEASE,$BROKER_RELEASE,$INFLUXDB_RELEASE,$COLLECTOR_RELEASE,$STREAMING_RELEASE,$API_RELEASE)" \
        -o wide 2>/dev/null || \
    kubectl get pods --namespace "$NAMESPACE" 2>/dev/null || \
        warn "Could not list pods."

    echo ""
    echo -e "${BOLD}Services${NC}"
    kubectl get svc --namespace "$NAMESPACE" 2>/dev/null || \
        warn "Could not list services."

    echo ""
    echo -e "${BOLD}PersistentVolumeClaims${NC}"
    kubectl get pvc --namespace "$NAMESPACE" 2>/dev/null || \
        warn "Could not list PVCs."
}

# ── Entrypoint ────────────────────────────────────────────────────────────────
print_usage() {
    echo ""
    echo -e "${BOLD}Usage:${NC}"
    echo "  $0 deploy   [--dry-run]   Deploy all components (ordered)"
    echo "  $0 clean    [--dry-run]   Remove all components (reverse order)"
    echo "  $0 status                 Show status of all components"
    echo ""
    echo -e "${BOLD}Environment variables:${NC}"
    echo "  NAMESPACE   Kubernetes namespace to deploy into (default: default)"
    echo ""
    echo -e "${BOLD}Examples:${NC}"
    echo "  NAMESPACE=telemetry ./manage.sh deploy"
    echo "  ./manage.sh deploy --dry-run"
    echo "  ./manage.sh clean"
    echo "  ./manage.sh status"
    echo ""
}

COMMAND="${1:-}"
[[ "$*" == *"--dry-run"* ]] && DRY_RUN=true

case "$COMMAND" in
    deploy)
        $DRY_RUN && warn "DRY-RUN mode: no changes will be made."
        deploy_all
        ;;
    clean)
        $DRY_RUN && warn "DRY-RUN mode: no changes will be made."
        clean_all
        ;;
    status)
        show_status
        ;;
    *)
        error "Unknown command: '$COMMAND'"
        print_usage
        exit 1
        ;;
esac
