# Multi-stage build for InfluxDB with custom initialization
FROM influxdb:2.7-alpine

# Install curl for health checks
RUN apk add --no-cache curl bash

# Expose InfluxDB port
EXPOSE 8086

# Health check
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD curl -f http://localhost:8086/health || exit 1

# Default entrypoint and command
CMD ["influxd"]
