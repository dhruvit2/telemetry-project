#!/bin/bash

set -e

# Configuration
INFLUX_ORG="${1:-telemetry}"
INFLUX_BUCKET="${2:-gpu_metrics_raw}"
INFLUX_TOKEN="${3:-my-super-secret-token}"
INFLUX_URL="http://localhost:8086"
INFLUX_USERNAME="admin"
INFLUX_PASSWORD="admin123"

echo "=========================================="
echo "Initializing InfluxDB"
echo "=========================================="
echo "Organization: $INFLUX_ORG"
echo "Bucket: $INFLUX_BUCKET"
echo "URL: $INFLUX_URL"
echo ""

# Wait for InfluxDB to be ready
echo "Waiting for InfluxDB to be ready..."
max_attempts=30
attempt=0
until [ $attempt -ge $max_attempts ]; do
  if curl -s "$INFLUX_URL/health" > /dev/null 2>&1; then
    echo "✓ InfluxDB is ready"
    break
  fi
  attempt=$((attempt+1))
  echo "  Attempt $attempt/$max_attempts..."
  sleep 2
done

if [ $attempt -ge $max_attempts ]; then
  echo "✗ InfluxDB failed to start"
  exit 1
fi

# Check if already initialized
echo "Checking if already initialized..."
ORG_CHECK=$(curl -s -H "Authorization: Token $INFLUX_TOKEN" \
  "$INFLUX_URL/api/v2/orgs?org=$INFLUX_ORG" 2>/dev/null | grep -c "$INFLUX_ORG" || true)

if [ "$ORG_CHECK" -gt 0 ]; then
  echo "✓ InfluxDB already initialized"
  echo ""
  echo "Organization: $INFLUX_ORG"
  echo "Bucket: $INFLUX_BUCKET"
  echo "Token: $INFLUX_TOKEN"
  exit 0
fi

# Create organization if needed
echo "Creating organization: $INFLUX_ORG"
ORG_ID=$(curl -s -X POST \
  -H "Authorization: Token $INFLUX_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$INFLUX_ORG\"}" \
  "$INFLUX_URL/api/v2/orgs" | grep -o '"id":"[^"]*' | head -1 | cut -d'"' -f4)

if [ -z "$ORG_ID" ]; then
  echo "✗ Failed to create organization"
  exit 1
fi
echo "✓ Organization created: $ORG_ID"

# Create bucket
echo "Creating bucket: $INFLUX_BUCKET"
BUCKET_RESULT=$(curl -s -X POST \
  -H "Authorization: Token $INFLUX_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$INFLUX_BUCKET\",\"orgID\":\"$ORG_ID\",\"retentionRules\":[{\"type\":\"expire\",\"everySeconds\":2592000}]}" \
  "$INFLUX_URL/api/v2/buckets")

echo "$BUCKET_RESULT" | grep -q "$INFLUX_BUCKET" && echo "✓ Bucket created" || echo "✗ Bucket creation may have failed"

# Create API token for data writing
echo "Creating API token for data writing..."
TOKEN_RESULT=$(curl -s -X POST \
  -H "Authorization: Token $INFLUX_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"description\":\"Token for telemetry data writing\",\"orgID\":\"$ORG_ID\",\"permissions\":[{\"action\":\"write\",\"resource\":{\"type\":\"buckets\",\"orgID\":\"$ORG_ID\"}}]}" \
  "$INFLUX_URL/api/v2/authorizations")

echo "✓ API token created"

echo ""
echo "=========================================="
echo "InfluxDB Initialization Complete!"
echo "=========================================="
echo ""
echo "Connection Details:"
echo "  URL: $INFLUX_URL"
echo "  Organization: $INFLUX_ORG"
echo "  Bucket: $INFLUX_BUCKET"
echo "  Token: $INFLUX_TOKEN"
echo "  Username: $INFLUX_USERNAME"
echo "  Password: $INFLUX_PASSWORD"
echo ""
echo "Service Configuration:"
echo "  TSDB_URL: http://influxdb:8086 (in Docker)"
echo "  TSDB_ORG: $INFLUX_ORG"
echo "  TSDB_BUCKET: $INFLUX_BUCKET"
echo "  TSDB_TOKEN: $INFLUX_TOKEN"
echo ""
