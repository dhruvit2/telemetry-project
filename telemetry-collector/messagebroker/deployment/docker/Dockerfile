FROM golang:1.26-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make protobuf-dev gcc musl-dev

WORKDIR /build

# Generate proto files with pinned versions for consistency
# Using versions that match go.mod dependencies
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

# Copy go mod and sum files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

#RUN make proto

# Build broker binary
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo \
    -o /build/bin/broker ./cmd/broker

# Build admin CLI binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -o /build/bin/admin ./cmd/admin

# Build producer binary
#RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo \
#    -o /build/bin/producer ./cmd/producer

# Build consumer binary
#RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo \
#    -o /build/bin/consumer ./cmd/consumer

# Runtime stage
FROM alpine:3.18

RUN apk add --no-cache ca-certificates tzdata curl

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /build/bin/broker /app/broker
COPY --from=builder /build/bin/admin /usr/local/bin/admin
#COPY --from=builder /build/bin/producer /app/producer
#COPY --from=builder /build/bin/consumer /app/consumer

RUN chmod +x /usr/local/bin/admin
# Create data directory
RUN mkdir -p /app/data

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:9092/health || exit 1

# Expose ports
EXPOSE 9092

# Set environment variables
ENV BROKER_HOST=0.0.0.0 \
    BROKER_PORT=9092 \
    BROKER_ID=1 \
    COORDINATOR_URL=etcd:2379 \
    DATA_DIR=/app/data

# Run broker by default
ENTRYPOINT ["/app/broker"]
CMD ["--host", "0.0.0.0", "--port", "9092", "--id", "1"]
