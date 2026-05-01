package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dhruvit2/messagebroker/pkg/broker"
)

// Coordinator manages distributed coordination via etcd
type Coordinator interface {
	// Broker management
	RegisterBroker(ctx context.Context, metadata *broker.BrokerMetadata) error
	DeregisterBroker(ctx context.Context, brokerID int32) error
	GetBroker(ctx context.Context, brokerID int32) (*broker.BrokerMetadata, error)
	ListBrokers(ctx context.Context) ([]*broker.BrokerMetadata, error)

	// Leader election
	ElectLeader(ctx context.Context, topic string, partition int32, replicas []int32) (int32, error)
	GetLeader(ctx context.Context, topic string, partition int32) (int32, error)
	IsLeader(ctx context.Context, topic string, partition int32, brokerID int32) (bool, error)

	// ISR management
	UpdateISR(ctx context.Context, topic string, partition int32, isr []int32) error
	GetISR(ctx context.Context, topic string, partition int32) ([]int32, error)

	// Topic metadata
	CreateTopicMetadata(ctx context.Context, topicName string, numPartitions int32, replicationFactor int32) error
	GetTopicMetadata(ctx context.Context, topicName string) (*TopicMetadata, error)
	ListTopics(ctx context.Context) ([]string, error)

	// Consumer group coordination
	JoinConsumerGroup(ctx context.Context, groupID string, memberID string, topics []string) error
	LeaveConsumerGroup(ctx context.Context, groupID string, memberID string) error
	GetGroupMembers(ctx context.Context, groupID string) ([]string, error)
	CommitOffset(ctx context.Context, groupID string, topic string, partition int32, offset int64) error
	GetCommittedOffset(ctx context.Context, groupID string, topic string, partition int32) (int64, error)

	// Health checks
	HeartbeatBroker(ctx context.Context, brokerID int32) error
	WatchBrokerHealth(ctx context.Context) (<-chan BrokerHealthEvent, error)

	// Key-value storage for flexible metadata
	Put(ctx context.Context, key string, value string) error
	Get(ctx context.Context, key string) (string, error)

	// Start/Stop
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// TopicMetadata holds topic-level metadata from etcd
type TopicMetadata struct {
	Name              string                       `json:"name"`
	NumPartitions     int32                        `json:"num_partitions"`
	ReplicationFactor int32                        `json:"replication_factor"`
	Partitions        map[int32]*PartitionMetadata `json:"partitions"`
	CreatedAt         int64                        `json:"created_at"`
}

// PartitionMetadata holds partition-level metadata from etcd
type PartitionMetadata struct {
	ID       int32   `json:"id"`
	Leader   int32   `json:"leader"`
	Replicas []int32 `json:"replicas"`
	ISR      []int32 `json:"isr"`
	Epoch    int32   `json:"epoch"`
}

// BrokerHealthEvent represents a broker health change
type BrokerHealthEvent struct {
	BrokerID  int32
	IsHealthy bool
	Timestamp time.Time
}

// MockCoordinator is an in-memory implementation for testing (until real etcd integration)
type MockCoordinator struct {
	brokers           map[int32]*broker.BrokerMetadata
	leaders           map[string]int32   // "topic-partition" -> leader
	isrMap            map[string][]int32 // "topic-partition" -> ISR
	topics            map[string]*TopicMetadata
	consumerGroups    map[string]*ConsumerGroup   // groupID -> ConsumerGroup
	consumerOffsets   map[string]map[string]int64 // groupID -> "topic-partition" -> offset
	brokerHeartbeats  map[int32]time.Time
	healthWatchers    []chan BrokerHealthEvent
	kvStore           map[string]string // generic key-value store for metadata
	mu                sync.RWMutex
	running           bool
	heartbeatInterval time.Duration
	sessionTimeout    time.Duration
	cancel            context.CancelFunc
}

// ConsumerGroup represents a consumer group in etcd
type ConsumerGroup struct {
	ID        string   `json:"id"`
	Topic     string   `json:"topic"`
	Members   []string `json:"members"`
	UpdatedAt int64    `json:"updated_at"`
}

// NewMockCoordinator creates a new mock coordinator
func NewMockCoordinator(heartbeatInterval, sessionTimeout time.Duration) *MockCoordinator {
	return &MockCoordinator{
		brokers:           make(map[int32]*broker.BrokerMetadata),
		leaders:           make(map[string]int32),
		isrMap:            make(map[string][]int32),
		topics:            make(map[string]*TopicMetadata),
		consumerGroups:    make(map[string]*ConsumerGroup),
		consumerOffsets:   make(map[string]map[string]int64),
		brokerHeartbeats:  make(map[int32]time.Time),
		healthWatchers:    make([]chan BrokerHealthEvent, 0),
		kvStore:           make(map[string]string),
		heartbeatInterval: heartbeatInterval,
		sessionTimeout:    sessionTimeout,
	}
}

// Start initializes the coordinator
func (mc *MockCoordinator) Start(ctx context.Context) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.running {
		return fmt.Errorf("coordinator already running")
	}

	mc.running = true
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	mc.cancel = cancel

	// Start health check loop
	go mc.healthCheckLoop(ctx)

	return nil
}

// Put stores a key-value pair
func (mc *MockCoordinator) Put(ctx context.Context, key string, value string) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	if mc.kvStore == nil {
		mc.kvStore = make(map[string]string)
	}

	mc.kvStore[key] = value
	return nil
}

// Get retrieves a value by key
func (mc *MockCoordinator) Get(ctx context.Context, key string) (string, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.kvStore == nil {
		return "", fmt.Errorf("key not found: %s", key)
	}

	if value, exists := mc.kvStore[key]; exists {
		return value, nil
	}

	return "", fmt.Errorf("key not found: %s", key)
}

// Stop shuts down the coordinator
func (mc *MockCoordinator) Stop(ctx context.Context) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return nil
	}

	mc.running = false
	if mc.cancel != nil {
		mc.cancel()
	}

	// Close all health watchers
	for _, watcher := range mc.healthWatchers {
		close(watcher)
	}
	mc.healthWatchers = make([]chan BrokerHealthEvent, 0)

	return nil
}

// RegisterBroker registers a broker in the cluster
func (mc *MockCoordinator) RegisterBroker(ctx context.Context, metadata *broker.BrokerMetadata) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	mc.brokers[metadata.BrokerID] = metadata
	mc.brokerHeartbeats[metadata.BrokerID] = time.Now()

	fmt.Printf("[COORDINATOR] RegisterBroker: brokerID=%d, host=%s:%d, total_brokers=%d\n",
		metadata.BrokerID, metadata.Host, metadata.Port, len(mc.brokers))

	return nil
}

// DeregisterBroker removes a broker from the cluster
func (mc *MockCoordinator) DeregisterBroker(ctx context.Context, brokerID int32) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	delete(mc.brokers, brokerID)
	delete(mc.brokerHeartbeats, brokerID)

	// Trigger failover for partitions led by this broker
	mc.triggerFailover(brokerID)

	return nil
}

// GetBroker retrieves broker metadata
func (mc *MockCoordinator) GetBroker(ctx context.Context, brokerID int32) (*broker.BrokerMetadata, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if metadata, exists := mc.brokers[brokerID]; exists {
		return metadata, nil
	}

	return nil, fmt.Errorf("broker %d not found", brokerID)
}

// ListBrokers returns all registered brokers
func (mc *MockCoordinator) ListBrokers(ctx context.Context) ([]*broker.BrokerMetadata, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	brokers := make([]*broker.BrokerMetadata, 0, len(mc.brokers))
	for _, metadata := range mc.brokers {
		brokers = append(brokers, metadata)
	}

	return brokers, nil
}

// ElectLeader performs leader election for a partition
func (mc *MockCoordinator) ElectLeader(ctx context.Context, topic string, partition int32, replicas []int32) (int32, error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return 0, fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s-%d", topic, partition)

	// Check if we already have a leader
	if leader, exists := mc.leaders[key]; exists {
		// Verify leader is still healthy
		if _, isBrokerExists := mc.brokers[leader]; isBrokerExists {
			return leader, nil
		}
	}

	// Find healthy broker from ISR
	var newLeader int32
	isr, isrExists := mc.isrMap[key]
	if isrExists && len(isr) > 0 {
		// Pick first healthy broker from ISR
		for _, brokerID := range isr {
			if _, exists := mc.brokers[brokerID]; exists {
				newLeader = brokerID
				break
			}
		}
	}

	// If no ISR candidate, pick from replicas
	if newLeader == 0 && len(replicas) > 0 {
		for _, brokerID := range replicas {
			if _, exists := mc.brokers[brokerID]; exists {
				newLeader = brokerID
				break
			}
		}
	}

	if newLeader == 0 {
		return 0, fmt.Errorf("no healthy broker available for leader election")
	}

	mc.leaders[key] = newLeader
	return newLeader, nil
}

// GetLeader retrieves the current leader for a partition
func (mc *MockCoordinator) GetLeader(ctx context.Context, topic string, partition int32) (int32, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	key := fmt.Sprintf("%s-%d", topic, partition)
	if leader, exists := mc.leaders[key]; exists {
		return leader, nil
	}

	return 0, fmt.Errorf("no leader for partition %s-%d", topic, partition)
}

// IsLeader checks if a broker is the leader for a partition
func (mc *MockCoordinator) IsLeader(ctx context.Context, topic string, partition int32, brokerID int32) (bool, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	key := fmt.Sprintf("%s-%d", topic, partition)
	leader, exists := mc.leaders[key]
	return exists && leader == brokerID, nil
}

// UpdateISR updates the ISR for a partition
func (mc *MockCoordinator) UpdateISR(ctx context.Context, topic string, partition int32, isr []int32) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s-%d", topic, partition)
	mc.isrMap[key] = isr

	return nil
}

// GetISR retrieves the ISR for a partition
func (mc *MockCoordinator) GetISR(ctx context.Context, topic string, partition int32) ([]int32, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	key := fmt.Sprintf("%s-%d", topic, partition)
	if isr, exists := mc.isrMap[key]; exists {
		return isr, nil
	}

	return nil, fmt.Errorf("ISR not found for partition %s-%d", topic, partition)
}

// CreateTopicMetadata creates metadata for a new topic (idempotent - returns success if topic exists)
func (mc *MockCoordinator) CreateTopicMetadata(ctx context.Context, topicName string, numPartitions int32, replicationFactor int32) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		fmt.Printf("[COORDINATOR] CreateTopicMetadata FAILED: coordinator not running\n")
		return fmt.Errorf("coordinator not running")
	}

	// If topic already exists, return success (idempotent operation)
	if existingMetadata, exists := mc.topics[topicName]; exists {
		// Verify it has the expected number of partitions
		if len(existingMetadata.Partitions) > 0 {
			fmt.Printf("[COORDINATOR] CreateTopicMetadata: topic=%s already exists with %d partitions (idempotent success)\n",
				topicName, len(existingMetadata.Partitions))
			return nil // Return success for idempotency
		}
		fmt.Printf("[COORDINATOR] CreateTopicMetadata: topic=%s exists but has no partitions, will recreate\n", topicName)
		// Fall through to recreate if topic has no partitions (shouldn't happen but handle gracefully)
	}

	metadata := &TopicMetadata{
		Name:              topicName,
		NumPartitions:     numPartitions,
		ReplicationFactor: replicationFactor,
		Partitions:        make(map[int32]*PartitionMetadata),
		CreatedAt:         time.Now().Unix(),
	}

	// Initialize partition metadata
	brokers := make([]int32, 0)
	for brokerID := range mc.brokers {
		brokers = append(brokers, brokerID)
	}

	// Log broker availability for debugging
	fmt.Printf("[COORDINATOR] CreateTopicMetadata: topic=%s, partitions=%d, replicationFactor=%d, available_brokers=%d",
		topicName, numPartitions, replicationFactor, len(brokers))
	if len(brokers) > 0 {
		fmt.Printf(" (brokerIDs: %v)", brokers)
	} else {
		fmt.Printf(" (CRITICAL: NO BROKERS REGISTERED - topic will have no replicas!)")
	}
	fmt.Println()

	for i := int32(0); i < numPartitions; i++ {
		replicas := mc.assignReplicas(brokers, replicationFactor)
		if len(replicas) == 0 {
			fmt.Printf("[COORDINATOR] CreateTopicMetadata ERROR: failed to assign replicas for partition %d (no brokers available)\n", i)
			return fmt.Errorf("failed to assign replicas for partition %d: no brokers available", i)
		}
		metadata.Partitions[i] = &PartitionMetadata{
			ID:       i,
			Leader:   replicas[0],
			Replicas: replicas,
			ISR:      replicas,
			Epoch:    0,
		}

		// Register in leaders and ISR maps
		key := fmt.Sprintf("%s-%d", topicName, i)
		mc.leaders[key] = replicas[0]
		mc.isrMap[key] = replicas
		fmt.Printf("[COORDINATOR] CreateTopicMetadata: partition %d assigned to leader=%d, replicas=%v\n", i, replicas[0], replicas)
	}

	mc.topics[topicName] = metadata
	fmt.Printf("[COORDINATOR] CreateTopicMetadata: topic=%s created successfully with %d partitions\n", topicName, numPartitions)
	return nil
}

// GetTopicMetadata retrieves metadata for a topic
func (mc *MockCoordinator) GetTopicMetadata(ctx context.Context, topicName string) (*TopicMetadata, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if metadata, exists := mc.topics[topicName]; exists {
		fmt.Printf("[COORDINATOR] GetTopicMetadata: topic=%s found with %d partitions\n", topicName, len(metadata.Partitions))
		return metadata, nil
	}

	fmt.Printf("[COORDINATOR] GetTopicMetadata: topic=%s NOT FOUND (available topics: %d)\n", topicName, len(mc.topics))
	return nil, fmt.Errorf("topic %s not found", topicName)
}

// ListTopics returns all topics
func (mc *MockCoordinator) ListTopics(ctx context.Context) ([]string, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	topics := make([]string, 0, len(mc.topics))
	for topicName := range mc.topics {
		topics = append(topics, topicName)
	}

	return topics, nil
}

// JoinConsumerGroup adds a consumer to a group
func (mc *MockCoordinator) JoinConsumerGroup(ctx context.Context, groupID string, memberID string, topics []string) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	// For simplicity, assuming single topic per group
	if len(topics) == 0 {
		return fmt.Errorf("must subscribe to at least one topic")
	}

	if _, exists := mc.consumerGroups[groupID]; !exists {
		mc.consumerGroups[groupID] = &ConsumerGroup{
			ID:        groupID,
			Topic:     topics[0],
			Members:   make([]string, 0),
			UpdatedAt: time.Now().Unix(),
		}
		mc.consumerOffsets[groupID] = make(map[string]int64)
	}

	group := mc.consumerGroups[groupID]
	group.Members = append(group.Members, memberID)
	group.UpdatedAt = time.Now().Unix()

	return nil
}

// LeaveConsumerGroup removes a consumer from a group
func (mc *MockCoordinator) LeaveConsumerGroup(ctx context.Context, groupID string, memberID string) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	if group, exists := mc.consumerGroups[groupID]; exists {
		// Remove member from group
		for i, member := range group.Members {
			if member == memberID {
				group.Members = append(group.Members[:i], group.Members[i+1:]...)
				break
			}
		}

		// If group is empty, delete it
		if len(group.Members) == 0 {
			delete(mc.consumerGroups, groupID)
			delete(mc.consumerOffsets, groupID)
		}
	}

	return nil
}

// GetGroupMembers returns members of a consumer group
func (mc *MockCoordinator) GetGroupMembers(ctx context.Context, groupID string) ([]string, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if group, exists := mc.consumerGroups[groupID]; exists {
		members := make([]string, len(group.Members))
		copy(members, group.Members)
		return members, nil
	}

	return nil, fmt.Errorf("consumer group %s not found", groupID)
}

// CommitOffset commits an offset for a consumer group
func (mc *MockCoordinator) CommitOffset(ctx context.Context, groupID string, topic string, partition int32, offset int64) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	if _, exists := mc.consumerOffsets[groupID]; !exists {
		mc.consumerOffsets[groupID] = make(map[string]int64)
	}

	key := fmt.Sprintf("%s-%d", topic, partition)
	mc.consumerOffsets[groupID][key] = offset

	return nil
}

// GetCommittedOffset retrieves the committed offset for a consumer group
func (mc *MockCoordinator) GetCommittedOffset(ctx context.Context, groupID string, topic string, partition int32) (int64, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if offsets, exists := mc.consumerOffsets[groupID]; exists {
		key := fmt.Sprintf("%s-%d", topic, partition)
		if offset, exists := offsets[key]; exists {
			return offset, nil
		}
	}

	return 0, nil // Return 0 if not found (start from beginning)
}

// HeartbeatBroker updates broker heartbeat
func (mc *MockCoordinator) HeartbeatBroker(ctx context.Context, brokerID int32) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return fmt.Errorf("coordinator not running")
	}

	if _, exists := mc.brokers[brokerID]; exists {
		mc.brokerHeartbeats[brokerID] = time.Now()
		return nil
	}

	return fmt.Errorf("broker %d not found", brokerID)
}

// WatchBrokerHealth watches for broker health changes
func (mc *MockCoordinator) WatchBrokerHealth(ctx context.Context) (<-chan BrokerHealthEvent, error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.running {
		return nil, fmt.Errorf("coordinator not running")
	}

	eventCh := make(chan BrokerHealthEvent, 10)
	mc.healthWatchers = append(mc.healthWatchers, eventCh)

	return eventCh, nil
}

// healthCheckLoop periodically checks broker health
func (mc *MockCoordinator) healthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(mc.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mc.checkBrokerHealth()
		}
	}
}

// checkBrokerHealth checks if brokers are still healthy
func (mc *MockCoordinator) checkBrokerHealth() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	now := time.Now()
	deadBrokers := make([]int32, 0)

	for brokerID, lastHeartbeat := range mc.brokerHeartbeats {
		if now.Sub(lastHeartbeat) > mc.sessionTimeout {
			deadBrokers = append(deadBrokers, brokerID)
		}
	}

	// Mark brokers as dead and trigger failover
	for _, brokerID := range deadBrokers {
		delete(mc.brokerHeartbeats, brokerID)

		// Notify watchers
		event := BrokerHealthEvent{
			BrokerID:  brokerID,
			IsHealthy: false,
			Timestamp: now,
		}

		for _, watcher := range mc.healthWatchers {
			select {
			case watcher <- event:
			default:
				// Channel full, skip
			}
		}

		mc.triggerFailover(brokerID)
	}
}

// triggerFailover triggers leader election for partitions led by a failed broker
func (mc *MockCoordinator) triggerFailover(brokerID int32) {
	for key, leader := range mc.leaders {
		if leader == brokerID {
			// Find healthy broker from ISR
			isr := mc.isrMap[key]
			var newLeader int32

			for _, candidate := range isr {
				if _, exists := mc.brokers[candidate]; exists && candidate != brokerID {
					newLeader = candidate
					break
				}
			}

			if newLeader != 0 {
				mc.leaders[key] = newLeader
			}
		}
	}
}

// assignReplicas assigns replicas for a partition (round-robin)
func (mc *MockCoordinator) assignReplicas(brokers []int32, replicationFactor int32) []int32 {
	if len(brokers) == 0 {
		return []int32{}
	}

	replicas := make([]int32, 0)
	for i := int32(0); i < replicationFactor && len(replicas) < len(brokers); i++ {
		brokerIdx := int(i) % len(brokers)
		replicas = append(replicas, brokers[brokerIdx])
	}

	return replicas
}

// String returns JSON representation of coordinator state (for debugging)
func (mc *MockCoordinator) String() string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	state := map[string]interface{}{
		"running":         mc.running,
		"brokers_count":   len(mc.brokers),
		"topics_count":    len(mc.topics),
		"consumer_groups": len(mc.consumerGroups),
		"leaders_count":   len(mc.leaders),
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	return string(data)
}
