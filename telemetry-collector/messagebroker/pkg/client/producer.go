package client

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	pb "github.com/dhruvit2/messagebroker/pkg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ProducerConfig holds producer configuration
type ProducerConfig struct {
	BrokerAddresses     []string
	CompressionType     string // "none", "snappy", "gzip"
	Retries             int
	RetryBackoffMs      int
	RequestTimeoutMs    int
	BatchSize           int
	LingerMs            int
	MaxInFlightRequests int
	Acks                string // "none", "leader", "all"
	AutoCreateTopics    bool   // Auto-create topics if they don't exist
	NumPartitions       int32  // Number of partitions for auto-created topics
	ReplicationFactor   int32  // Replication factor for auto-created topics
}

// ProducerRecord represents a message to be produced
type ProducerRecord struct {
	Topic     string
	Partition *int32
	Key       []byte
	Value     []byte
	Headers   map[string][]byte
	Timestamp time.Time
}

// ProducerCallback is called after a message is sent
type ProducerCallback func(topic string, partition int32, offset int64, err error)

// Producer sends messages to the broker
type Producer struct {
	config          *ProducerConfig
	brokers         map[int]string // Broker ID -> Address
	partitioner     Partitioner
	batch           []*ProducerRecord
	batchMu         sync.Mutex
	idempotentID    int32
	sequenceNumbers map[string]int32 // Topic-Partition -> Sequence
	seqMu           sync.RWMutex
	closed          bool
	grpcConn        *grpc.ClientConn
	grpcClient      pb.MessageBrokerClient
	createdTopics   map[string]bool // Track created topics
	topicMu         sync.Mutex
}

// Partitioner selects which partition a message should go to
type Partitioner interface {
	Partition(topic string, key []byte, numPartitions int32) int32
}

// DefaultPartitioner uses round-robin and key-based partitioning
type DefaultPartitioner struct {
	counter int64
	mu      sync.Mutex
}

// Partition returns partition ID
func (dp *DefaultPartitioner) Partition(topic string, key []byte, numPartitions int32) int32 {
	if key == nil {
		// Round-robin
		dp.mu.Lock()
		defer dp.mu.Unlock()
		partition := dp.counter % int64(numPartitions)
		dp.counter++
		return int32(partition)
	}

	// Hash-based for keyed messages
	hash := hashKey(key)
	return int32(hash % int64(numPartitions))
}

// NewProducer creates a new producer
func NewProducer(config *ProducerConfig) (*Producer, error) {
	if len(config.BrokerAddresses) == 0 {
		return nil, fmt.Errorf("no broker addresses provided")
	}

	// Set defaults
	if config.AutoCreateTopics && config.NumPartitions == 0 {
		config.NumPartitions = 3
	}
	if config.AutoCreateTopics && config.ReplicationFactor == 0 {
		config.ReplicationFactor = 1
	}

	// Connect to broker via gRPC
	brokerAddr := config.BrokerAddresses[0]
	conn, err := grpc.Dial(brokerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to broker: %v", err)
	}

	brokers := make(map[int]string)
	for i, addr := range config.BrokerAddresses {
		brokers[i] = addr
	}

	p := &Producer{
		config:          config,
		brokers:         brokers,
		partitioner:     &DefaultPartitioner{},
		batch:           make([]*ProducerRecord, 0),
		idempotentID:    rand.Int31(),
		sequenceNumbers: make(map[string]int32),
		closed:          false,
		grpcConn:        conn,
		grpcClient:      pb.NewMessageBrokerClient(conn),
		createdTopics:   make(map[string]bool),
	}

	return p, nil
}

// Send sends a message asynchronously and calls callback when done
func (p *Producer) Send(record *ProducerRecord, callback ProducerCallback) error {
	if p.closed {
		return fmt.Errorf("producer is closed")
	}

	// Ensure topic exists if auto-create is enabled
	if p.config.AutoCreateTopics {
		if err := p.ensureTopicExists(context.Background(), record.Topic); err != nil {
			callback(record.Topic, 0, -1, fmt.Errorf("failed to ensure topic exists: %v", err))
			return nil
		}
	}

	p.batchMu.Lock()
	p.batch = append(p.batch, record)
	batchSize := len(p.batch)
	p.batchMu.Unlock()

	// Determine partition
	if record.Partition == nil {
		partition := p.partitioner.Partition(record.Topic, record.Key, 1)
		record.Partition = &partition
	}

	// Send to broker via gRPC asynchronously
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.RequestTimeoutMs)*time.Millisecond)
		defer cancel()

		req := &pb.ProduceRequest{
			Topic:     record.Topic,
			Partition: *record.Partition,
			Key:       record.Key,
			Value:     record.Value,
		}

		resp, err := p.grpcClient.ProduceMessage(ctx, req)
		if err != nil {
			callback(record.Topic, *record.Partition, -1, err)
			return
		}

		callback(record.Topic, resp.Partition, resp.Offset, nil)
	}()

	if batchSize >= p.config.BatchSize {
		return p.Flush()
	}

	return nil
}

// SendSync sends a message synchronously
func (p *Producer) SendSync(record *ProducerRecord) (int64, error) {
	if p.closed {
		return -1, fmt.Errorf("producer is closed")
	}

	resultChan := make(chan int64, 1)
	errChan := make(chan error, 1)

	p.Send(record, func(topic string, partition int32, offset int64, err error) {
		if err != nil {
			errChan <- err
		} else {
			resultChan <- offset
		}
	})

	select {
	case offset := <-resultChan:
		return offset, nil
	case err := <-errChan:
		return -1, err
	case <-time.After(time.Duration(p.config.RequestTimeoutMs) * time.Millisecond):
		return -1, fmt.Errorf("send timeout")
	}
}

// Flush flushes pending messages
func (p *Producer) Flush() error {
	p.batchMu.Lock()
	batch := p.batch
	p.batch = make([]*ProducerRecord, 0)
	p.batchMu.Unlock()

	// In production, would send batch to broker
	_ = batch

	return nil
}

// Close closes the producer
func (p *Producer) Close() error {
	if p.closed {
		return nil
	}

	if err := p.Flush(); err != nil {
		return err
	}

	// Close gRPC connection
	if p.grpcConn != nil {
		p.grpcConn.Close()
	}

	p.closed = true
	return nil
}

// hashKey computes hash of key
func hashKey(key []byte) int64 {
	hash := int64(0)
	for _, b := range key {
		hash = hash*31 + int64(b)
	}
	if hash < 0 {
		hash = -hash
	}
	return hash
}

// ensureTopicExists creates a topic if it doesn't already exist
func (p *Producer) ensureTopicExists(ctx context.Context, topic string) error {
	p.topicMu.Lock()
	if p.createdTopics[topic] {
		p.topicMu.Unlock()
		return nil
	}
	p.topicMu.Unlock()

	// Try to create the topic
	if err := p.CreateTopic(ctx, topic, p.config.NumPartitions, p.config.ReplicationFactor); err != nil {
		// Silently ignore "topic already exists" errors (case-insensitive, substring match)
		errMsg := strings.ToLower(err.Error())
		if !strings.Contains(errMsg, "already exists") && !strings.Contains(errMsg, "topic already exists") {
			return err
		}
		// Topic already exists, which is fine
	}

	// Mark as created
	p.topicMu.Lock()
	p.createdTopics[topic] = true
	p.topicMu.Unlock()

	return nil
}

// CreateTopic creates a new topic on the broker
func (p *Producer) CreateTopic(ctx context.Context, topic string, numPartitions int32, replicationFactor int32) error {
	if numPartitions == 0 {
		numPartitions = 3
	}
	if replicationFactor == 0 {
		replicationFactor = 1
	}

	req := &pb.CreateTopicRequest{
		Topic:             topic,
		NumPartitions:     numPartitions,
		ReplicationFactor: replicationFactor,
	}

	resp, err := p.grpcClient.CreateTopic(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("failed to create topic %s", topic)
	}

	return nil
}

// ProducerMetrics holds producer statistics
type ProducerMetrics struct {
	TotalMessagesSent   int64
	TotalMessagesFailed int64
	TotalBytes          int64
	AverageLatency      time.Duration
}

// GetMetrics returns producer metrics
func (p *Producer) GetMetrics() *ProducerMetrics {
	return &ProducerMetrics{
		TotalMessagesSent:   0,
		TotalMessagesFailed: 0,
		TotalBytes:          0,
		AverageLatency:      0,
	}
}
