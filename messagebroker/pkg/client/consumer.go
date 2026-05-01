package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	pb "github.com/dhruvit2/messagebroker/pkg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ConsumerConfig holds consumer configuration
type ConsumerConfig struct {
	BrokerAddresses      []string
	GroupID              string
	Topics               []string
	MaxPollRecords       int
	SessionTimeoutMs     int
	HeartbeatIntervalMs  int
	FetchMinBytes        int
	FetchMaxWaitMs       int
	RequestTimeoutMs     int
	EnableAutoCommit     bool
	AutoCommitIntervalMs int
	AutoOffsetReset      string // "earliest", "latest"
}

// ConsumerRecord represents a received message
type ConsumerRecord struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Headers   map[string][]byte
	Timestamp time.Time
}

// Consumer receives messages from the broker
type Consumer struct {
	config          *ConsumerConfig
	brokers         map[int]string // Broker ID -> Address
	groupID         string
	consumerID      string
	subscriptions   map[string]bool            // Topic -> subscribed
	assignments     map[string]map[int32]int64 // Topic -> Partition -> Offset
	assignmentMu    sync.RWMutex
	offsets         map[string]map[int32]int64 // Topic -> Partition -> Current offset
	offsetMu        sync.RWMutex
	lastHeartbeat   time.Time
	closed          bool
	heartbeatTicker *time.Ticker
	commitTicker    *time.Ticker
	pollInterval    time.Duration
	grpcConn        *grpc.ClientConn
	grpcClient      pb.MessageBrokerClient
}

// NewConsumer creates a new consumer
func NewConsumer(config *ConsumerConfig) (*Consumer, error) {
	if len(config.BrokerAddresses) == 0 {
		return nil, fmt.Errorf("no broker addresses provided")
	}

	if config.GroupID == "" {
		return nil, fmt.Errorf("group ID is required")
	}

	brokers := make(map[int]string)
	for i, addr := range config.BrokerAddresses {
		brokers[i] = addr
	}

	// Connect to broker via gRPC
	brokerAddr := config.BrokerAddresses[0]
	conn, err := grpc.Dial(brokerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to broker: %v", err)
	}

	c := &Consumer{
		config:        config,
		brokers:       brokers,
		groupID:       config.GroupID,
		consumerID:    generateConsumerID(),
		subscriptions: make(map[string]bool),
		assignments:   make(map[string]map[int32]int64),
		offsets:       make(map[string]map[int32]int64),
		lastHeartbeat: time.Now(),
		closed:        false,
		pollInterval:  100 * time.Millisecond,
		grpcConn:      conn,
		grpcClient:    pb.NewMessageBrokerClient(conn),
	}

	// Subscribe to topics
	for _, topic := range config.Topics {
		c.subscriptions[topic] = true
	}

	// Fetch partition metadata and auto-assign partitions
	if err := c.fetchAndAssignPartitions(context.Background()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to fetch and assign partitions: %v", err)
	}

	// Start heartbeat
	c.startHeartbeat()

	// Start auto-commit if enabled
	if config.EnableAutoCommit {
		c.startAutoCommit()
	}

	return c, nil
}

// Poll retrieves messages from subscribed topics
func (c *Consumer) Poll(ctx context.Context, timeoutMs int) ([]*ConsumerRecord, error) {
	if c.closed {
		return nil, fmt.Errorf("consumer is closed")
	}

	records := make([]*ConsumerRecord, 0)

	c.assignmentMu.RLock()
	assignments := c.assignments
	c.assignmentMu.RUnlock()

	c.offsetMu.Lock()
	defer c.offsetMu.Unlock()

	// Fetch messages from each assigned partition via gRPC
	for topic, partitions := range assignments {
		for partitionID := range partitions {
			// Get current offset for this partition (not the initial assignment offset)
			var currentOffset int64 = 0
			if offsets, exists := c.offsets[topic]; exists {
				if offset, exists := offsets[partitionID]; exists {
					currentOffset = offset
				}
			}

			// Call broker to consume messages
			req := &pb.ConsumeRequest{
				Topic:       topic,
				Partition:   partitionID,
				Offset:      currentOffset,
				MaxMessages: int32(c.config.MaxPollRecords - len(records)),
			}

			resp, err := c.grpcClient.ConsumeMessages(ctx, req)
			if err != nil {
				fmt.Printf("Failed to consume from %s[%d]: %v\n", topic, partitionID, err)
				continue
			}

			// Convert proto messages to consumer records
			for _, msg := range resp.Messages {
				record := &ConsumerRecord{
					Topic:     resp.Topic,
					Partition: resp.Partition,
					Offset:    msg.Offset,
					Key:       msg.Key,
					Value:     msg.Value,
					Timestamp: time.Now(),
				}
				records = append(records, record)

				// Update offset for next poll
				if _, exists := c.offsets[topic]; !exists {
					c.offsets[topic] = make(map[int32]int64)
				}
				c.offsets[topic][partitionID] = msg.Offset + 1

				if len(records) >= c.config.MaxPollRecords {
					break
				}
			}

			if len(records) >= c.config.MaxPollRecords {
				break
			}
		}
		if len(records) >= c.config.MaxPollRecords {
			break
		}
	}

	return records, nil
}

// Assign assigns partitions to this consumer
func (c *Consumer) Assign(assignments map[string]map[int32]int64) error {
	c.assignmentMu.Lock()
	defer c.assignmentMu.Unlock()

	c.assignments = assignments

	// Initialize offsets
	for topic, partitions := range assignments {
		if _, exists := c.offsets[topic]; !exists {
			c.offsets[topic] = make(map[int32]int64)
		}
		for partitionID, offset := range partitions {
			c.offsets[topic][partitionID] = offset
		}
	}

	return nil
}

// CommitSync commits current offsets synchronously
func (c *Consumer) CommitSync() error {
	c.offsetMu.RLock()
	defer c.offsetMu.RUnlock()

	// In production, would send to broker
	// For now, just log
	_ = c.offsets

	return nil
}

// CommitAsync commits current offsets asynchronously
func (c *Consumer) CommitAsync() error {
	go c.CommitSync()
	return nil
}

// Seek seeks to a specific offset
func (c *Consumer) Seek(topic string, partition int32, offset int64) error {
	c.offsetMu.Lock()
	defer c.offsetMu.Unlock()

	if _, exists := c.offsets[topic]; !exists {
		c.offsets[topic] = make(map[int32]int64)
	}

	c.offsets[topic][partition] = offset
	return nil
}

// GetPosition returns current position
func (c *Consumer) GetPosition(topic string, partition int32) (int64, error) {
	c.offsetMu.RLock()
	defer c.offsetMu.RUnlock()

	if partitions, exists := c.offsets[topic]; exists {
		if offset, exists := partitions[partition]; exists {
			return offset, nil
		}
	}

	return -1, fmt.Errorf("partition not assigned")
}

// Close closes the consumer
func (c *Consumer) Close() error {
	c.assignmentMu.Lock()
	defer c.assignmentMu.Unlock()

	if c.closed {
		return nil
	}

	c.stopHeartbeat()
	c.stopAutoCommit()

	if c.config.EnableAutoCommit {
		c.CommitSync()
	}

	// Close gRPC connection
	if c.grpcConn != nil {
		c.grpcConn.Close()
	}

	c.closed = true
	return nil
}

// startHeartbeat starts the heartbeat goroutine
func (c *Consumer) startHeartbeat() {
	c.heartbeatTicker = time.NewTicker(time.Duration(c.config.HeartbeatIntervalMs) * time.Millisecond)

	go func() {
		for range c.heartbeatTicker.C {
			if c.closed {
				return
			}
			c.lastHeartbeat = time.Now()
			// In production, would send heartbeat to broker
		}
	}()
}

// stopHeartbeat stops the heartbeat goroutine
func (c *Consumer) stopHeartbeat() {
	if c.heartbeatTicker != nil {
		c.heartbeatTicker.Stop()
	}
}

// startAutoCommit starts the auto-commit goroutine
func (c *Consumer) startAutoCommit() {
	c.commitTicker = time.NewTicker(time.Duration(c.config.AutoCommitIntervalMs) * time.Millisecond)

	go func() {
		for range c.commitTicker.C {
			if c.closed {
				return
			}
			c.CommitSync()
		}
	}()
}

// stopAutoCommit stops the auto-commit goroutine
func (c *Consumer) stopAutoCommit() {
	if c.commitTicker != nil {
		c.commitTicker.Stop()
	}
}

// generateConsumerID generates a unique consumer ID
func generateConsumerID() string {
	return fmt.Sprintf("consumer-%d", time.Now().UnixNano())
}

// ConsumerMetrics holds consumer statistics
type ConsumerMetrics struct {
	TotalMessagesConsumed int64
	TotalCommits          int64
	LastCommitOffset      int64
	CommitFailures        int64
}

// GetMetrics returns consumer metrics
func (c *Consumer) GetMetrics() *ConsumerMetrics {
	return &ConsumerMetrics{
		TotalMessagesConsumed: 0,
		TotalCommits:          0,
		LastCommitOffset:      0,
		CommitFailures:        0,
	}
}

// fetchAndAssignPartitions fetches metadata for subscribed topics and auto-assigns partitions
// It retries if topics don't exist yet, allowing time for them to be created
func (c *Consumer) fetchAndAssignPartitions(ctx context.Context) error {
	const maxRetries = 30
	const retryInterval = 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := c.tryFetchAndAssignPartitions(ctx)
		if err == nil {
			return nil
		}

		// If it's a "topic not found" error, retry
		if strings.Contains(err.Error(), "topic not found") {
			if attempt < maxRetries-1 {
				fmt.Printf("Topics not found yet, retrying in %v (attempt %d/%d)\n", retryInterval, attempt+1, maxRetries)
				time.Sleep(retryInterval)
				continue
			}
		}

		// For other errors, fail immediately
		return err
	}

	return fmt.Errorf("failed to fetch topic metadata after %d retries", maxRetries)
}

// tryFetchAndAssignPartitions attempts to fetch metadata and assign partitions once
func (c *Consumer) tryFetchAndAssignPartitions(ctx context.Context) error {
	c.assignmentMu.Lock()
	defer c.assignmentMu.Unlock()

	// For each subscribed topic, fetch metadata and assign all partitions
	for topic := range c.subscriptions {
		req := &pb.GetTopicMetadataRequest{
			Topic: topic,
		}

		resp, err := c.grpcClient.GetTopicMetadata(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to get metadata for topic %s: %v", topic, err)
		}

		// Auto-assign all partitions for this topic
		if _, exists := c.assignments[topic]; !exists {
			c.assignments[topic] = make(map[int32]int64)
		}

		// Determine initial offset based on AutoOffsetReset
		var initialOffset int64 = 0
		if strings.ToLower(c.config.AutoOffsetReset) == "latest" {
			// For latest, we'd need to query the broker for end offset
			// For now, start from 0
			initialOffset = 0
		}

		for _, partition := range resp.Partitions {
			c.assignments[topic][partition.Id] = initialOffset
		}

		// Initialize offsets
		if _, exists := c.offsets[topic]; !exists {
			c.offsets[topic] = make(map[int32]int64)
		}
		for _, partition := range resp.Partitions {
			c.offsets[topic][partition.Id] = initialOffset
		}
	}

	return nil
}
