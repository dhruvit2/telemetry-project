package broker

import (
	"context"
	"sync"
	"time"
)

// Broker interface defines the broker API
type IBroker interface {
	// Topic management
	CreateTopic(ctx context.Context, topic *Topic) error
	DeleteTopic(ctx context.Context, topicName string) error
	GetTopic(ctx context.Context, topicName string) (*Topic, error)
	ListTopics(ctx context.Context) ([]*Topic, error)

	// Message operations
	ProduceMessage(ctx context.Context, message *Message) (int64, error)
	ConsumeMessages(ctx context.Context, topic string, partition int32, offset int64, maxMessages int32) ([]*Message, error)

	// Partition management
	GetPartition(ctx context.Context, topic string, partition int32) (*Partition, error)
	AssignPartitions(ctx context.Context, brokerID int32) error
	RebalancePartitions(ctx context.Context) error

	// Metadata
	GetBrokerMetadata(ctx context.Context) (*BrokerMetadata, error)
	UpdateBrokerMetadata(ctx context.Context, metadata *BrokerMetadata) error

	// Health
	IsHealthy() bool
	GetStatus() *BrokerStatus
	Start() error
	Stop() error
}

// BrokerMetadata holds broker cluster metadata
type BrokerMetadata struct {
	BrokerID      int32
	Host          string
	Port          int
	Racks         []string
	Timestamp     int64
	LeaderEpoch   int32
	ControllerID  int32
	IsController  bool
	IsLeader      bool
	ClustersSize  int32
	AvailableSize int64
}

// BrokerImpl implements the Broker interface
type BrokerImpl struct {
	config           *BrokerConfig
	metadata         *BrokerMetadata
	topics           map[string]*Topic
	partitions       map[string]*Partition // "topic-partition" -> Partition
	consumerGroups   map[string]*ConsumerGroup
	replicationState map[string]*ReplicationState
	mu               sync.RWMutex
	healthy          bool
	started          bool
}

// NewBroker creates a new broker instance
func NewBroker(config *BrokerConfig) *BrokerImpl {
	return &BrokerImpl{
		config:           config,
		topics:           make(map[string]*Topic),
		partitions:       make(map[string]*Partition),
		consumerGroups:   make(map[string]*ConsumerGroup),
		replicationState: make(map[string]*ReplicationState),
		healthy:          true,
		started:          false,
	}
}

// Start initializes and starts the broker
func (b *BrokerImpl) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.started {
		return nil
	}

	// Initialize metadata
	b.metadata = &BrokerMetadata{
		BrokerID:     b.config.ID,
		Host:         b.config.Host,
		Port:         b.config.Port,
		IsLeader:     false,
		IsController: false,
	}

	b.started = true
	return nil
}

// Stop gracefully stops the broker
func (b *BrokerImpl) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.started = false
	return nil
}

// CreateTopic creates a new topic
func (b *BrokerImpl) CreateTopic(ctx context.Context, topic *Topic) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.topics[topic.Name]; exists {
		return ErrTopicExists
	}

	b.topics[topic.Name] = topic

	// Create partitions for this topic
	for i := int32(0); i < topic.NumPartitions; i++ {
		partition := &Partition{
			ID:          i,
			Topic:       topic.Name,
			Leader:      b.config.ID,
			Replicas:    []int32{b.config.ID},
			ISR:         []int32{b.config.ID},
			Offset:      0,
			messages:    make([]*Message, 0),
			maxMessages: 1000000,
		}
		b.partitions[topic.Name+"-"+string(rune(i))] = partition
	}

	return nil
}

// DeleteTopic deletes a topic
func (b *BrokerImpl) DeleteTopic(ctx context.Context, topicName string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.topics[topicName]; !exists {
		return ErrTopicNotFound
	}

	topic := b.topics[topicName]
	delete(b.topics, topicName)

	// Delete associated partitions
	for i := int32(0); i < topic.NumPartitions; i++ {
		delete(b.partitions, topicName+"-"+string(rune(i)))
	}

	return nil
}

// GetTopic retrieves topic metadata
func (b *BrokerImpl) GetTopic(ctx context.Context, topicName string) (*Topic, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	topic, exists := b.topics[topicName]
	if !exists {
		return nil, ErrTopicNotFound
	}

	return topic, nil
}

// ListTopics returns all topics
func (b *BrokerImpl) ListTopics(ctx context.Context) ([]*Topic, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	topics := make([]*Topic, 0, len(b.topics))
	for _, topic := range b.topics {
		topics = append(topics, topic)
	}

	return topics, nil
}

// ProduceMessage adds a message to a partition
func (b *BrokerImpl) ProduceMessage(ctx context.Context, message *Message) (int64, error) {
	b.mu.RLock()
	partition := b.partitions[message.Topic+"-"+string(rune(message.Partition))]
	b.mu.RUnlock()

	if partition == nil {
		return -1, ErrPartitionNotFound
	}

	partition.mu.Lock()
	defer partition.mu.Unlock()

	message.Offset = partition.Offset
	partition.messages = append(partition.messages, message)
	partition.Offset++

	// Keep only maxMessages
	if int64(len(partition.messages)) > partition.maxMessages {
		partition.messages = partition.messages[1:]
	}

	return message.Offset, nil
}

// ConsumeMessages retrieves messages from a partition
func (b *BrokerImpl) ConsumeMessages(ctx context.Context, topic string, partition int32, offset int64, maxMessages int32) ([]*Message, error) {
	b.mu.RLock()
	p := b.partitions[topic+"-"+string(rune(partition))]
	b.mu.RUnlock()

	if p == nil {
		return nil, ErrPartitionNotFound
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	messages := make([]*Message, 0)
	for i, msg := range p.messages {
		if msg.Offset >= offset && int32(len(messages)) < maxMessages {
			messages = append(messages, msg)
		}
		if int32(len(messages)) >= maxMessages {
			break
		}
		_ = i
	}

	return messages, nil
}

// GetPartition retrieves a specific partition
func (b *BrokerImpl) GetPartition(ctx context.Context, topic string, partition int32) (*Partition, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	p, exists := b.partitions[topic+"-"+string(rune(partition))]
	if !exists {
		return nil, ErrPartitionNotFound
	}

	return p, nil
}

// AssignPartitions assigns partitions to this broker
func (b *BrokerImpl) AssignPartitions(ctx context.Context, brokerID int32) error {
	// Coordinator would call this during rebalancing
	return nil
}

// RebalancePartitions rebalances partitions across brokers
func (b *BrokerImpl) RebalancePartitions(ctx context.Context) error {
	// Implemented by coordinator
	return nil
}

// GetBrokerMetadata returns broker metadata
func (b *BrokerImpl) GetBrokerMetadata(ctx context.Context) (*BrokerMetadata, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.metadata, nil
}

// UpdateBrokerMetadata updates broker metadata
func (b *BrokerImpl) UpdateBrokerMetadata(ctx context.Context, metadata *BrokerMetadata) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.metadata = metadata
	return nil
}

// IsHealthy checks if broker is healthy
func (b *BrokerImpl) IsHealthy() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.healthy && b.started
}

// GetStatus returns broker status
func (b *BrokerImpl) GetStatus() *BrokerStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	status := &BrokerStatus{
		ID:              b.config.ID,
		IsHealthy:       b.healthy,
		LastHeartbeat:   time.Now(),
		PartitionLeader: make(map[int32]int32),
		OffsetLag:       make(map[string]int64),
	}

	for _, partition := range b.partitions {
		status.PartitionLeader[partition.ID] = partition.Leader
	}

	return status
}
