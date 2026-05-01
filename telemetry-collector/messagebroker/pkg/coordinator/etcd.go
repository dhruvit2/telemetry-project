package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dhruvit2/messagebroker/pkg/broker"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdCoordinator implements the Coordinator interface using etcd
type EtcdCoordinator struct {
	client            *clientv3.Client
	brokerID          int32
	leaseID           clientv3.LeaseID
	leaseGrantResp    *clientv3.LeaseGrantResponse
	heartbeatInterval time.Duration
	sessionTimeout    time.Duration
	running           bool
	cancel            context.CancelFunc
}

const (
	// etcd key prefixes
	BrokerPrefix    = "/messagebroker/brokers/"
	TopicPrefix     = "/messagebroker/topics/"
	PartitionPrefix = "/messagebroker/partitions/"
	LeaderPrefix    = "/messagebroker/leaders/"
	ISRPrefix       = "/messagebroker/isr/"
	ConsumerPrefix  = "/messagebroker/consumers/"
	OffsetPrefix    = "/messagebroker/offsets/"
)

// NewEtcdCoordinator creates a new etcd-based coordinator
func NewEtcdCoordinator(endpoints []string, brokerID int32, heartbeatInterval, sessionTimeout time.Duration, username, password string) (*EtcdCoordinator, error) {
	cfg := clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	}

	// Add authentication if credentials provided
	if username != "" {
		cfg.Username = username
		cfg.Password = password
	}

	client, err := clientv3.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to etcd: %w", err)
	}

	return &EtcdCoordinator{
		client:            client,
		brokerID:          brokerID,
		heartbeatInterval: heartbeatInterval,
		sessionTimeout:    sessionTimeout,
		running:           false,
	}, nil
}

// Start initializes the etcd coordinator
func (ec *EtcdCoordinator) Start(ctx context.Context) error {
	if ec.running {
		return fmt.Errorf("coordinator already running")
	}

	// Create a lease for this broker
	leaseResp, err := ec.client.Grant(ctx, int64(ec.sessionTimeout.Seconds()))
	if err != nil {
		return fmt.Errorf("failed to create lease: %w", err)
	}

	ec.leaseID = leaseResp.ID
	ec.leaseGrantResp = leaseResp
	ec.running = true

	// Create context with cancel
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	ec.cancel = cancel

	// Start heartbeat loop
	go ec.heartbeatLoop(ctx)

	return nil
}

// Put stores a key-value pair in etcd
func (ec *EtcdCoordinator) Put(ctx context.Context, key string, value string) error {
	_, err := ec.client.Put(ctx, key, value)
	if err != nil {
		return fmt.Errorf("failed to put key %s: %w", key, err)
	}
	return nil
}

// Get retrieves a value from etcd by key
func (ec *EtcdCoordinator) Get(ctx context.Context, key string) (string, error) {
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("failed to get key %s: %w", key, err)
	}

	if len(resp.Kvs) == 0 {
		return "", fmt.Errorf("key not found: %s", key)
	}

	return string(resp.Kvs[0].Value), nil
}

// Stop shuts down the etcd coordinator
func (ec *EtcdCoordinator) Stop(ctx context.Context) error {
	if !ec.running {
		return nil
	}

	ec.running = false
	if ec.cancel != nil {
		ec.cancel()
	}

	// Revoke lease
	if ec.leaseID != 0 {
		_, _ = ec.client.Revoke(ctx, ec.leaseID)
	}

	return ec.client.Close()
}

// RegisterBroker registers a broker in etcd
func (ec *EtcdCoordinator) RegisterBroker(ctx context.Context, metadata *broker.BrokerMetadata) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal broker metadata: %w", err)
	}

	key := fmt.Sprintf("%s%d", BrokerPrefix, metadata.BrokerID)
	_, err = ec.client.Put(ctx, key, string(data), clientv3.WithLease(ec.leaseID))
	if err != nil {
		return fmt.Errorf("failed to register broker: %w", err)
	}

	return nil
}

// DeregisterBroker removes a broker from etcd
func (ec *EtcdCoordinator) DeregisterBroker(ctx context.Context, brokerID int32) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%d", BrokerPrefix, brokerID)
	_, err := ec.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to deregister broker: %w", err)
	}

	return nil
}

// GetBroker retrieves broker metadata from etcd
func (ec *EtcdCoordinator) GetBroker(ctx context.Context, brokerID int32) (*broker.BrokerMetadata, error) {
	key := fmt.Sprintf("%s%d", BrokerPrefix, brokerID)
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get broker: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("broker %d not found", brokerID)
	}

	var metadata broker.BrokerMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal broker metadata: %w", err)
	}

	return &metadata, nil
}

// ListBrokers returns all registered brokers from etcd
func (ec *EtcdCoordinator) ListBrokers(ctx context.Context) ([]*broker.BrokerMetadata, error) {
	resp, err := ec.client.Get(ctx, BrokerPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to list brokers: %w", err)
	}

	brokers := make([]*broker.BrokerMetadata, 0)
	for _, kv := range resp.Kvs {
		var metadata broker.BrokerMetadata
		if err := json.Unmarshal(kv.Value, &metadata); err != nil {
			continue // Skip invalid entries
		}
		brokers = append(brokers, &metadata)
	}

	return brokers, nil
}

// ElectLeader performs leader election for a partition in etcd
func (ec *EtcdCoordinator) ElectLeader(ctx context.Context, topic string, partition int32, replicas []int32) (int32, error) {
	if !ec.running {
		return 0, fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%s-%d", LeaderPrefix, topic, partition)

	// Try to get existing leader
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("failed to get leader: %w", err)
	}

	if len(resp.Kvs) > 0 {
		// Parse existing leader
		leader := int32(0)
		if err := json.Unmarshal(resp.Kvs[0].Value, &leader); err == nil {
			// Verify leader is still healthy
			brokerKey := fmt.Sprintf("%s%d", BrokerPrefix, leader)
			brokerResp, err := ec.client.Get(ctx, brokerKey)
			if err == nil && len(brokerResp.Kvs) > 0 {
				return leader, nil
			}
		}
	}

	// Elect new leader from ISR
	isr, err := ec.GetISR(ctx, topic, partition)
	if err == nil && len(isr) > 0 {
		// Pick first healthy broker from ISR
		for _, brokerID := range isr {
			brokerKey := fmt.Sprintf("%s%d", BrokerPrefix, brokerID)
			brokerResp, err := ec.client.Get(ctx, brokerKey)
			if err == nil && len(brokerResp.Kvs) > 0 {
				// Election using Compare-and-Swap
				newLeaderData, _ := json.Marshal(brokerID)
				txn := ec.client.Txn(ctx)
				txn = txn.If(clientv3.Compare(clientv3.Version(key), "=", 0))
				txn = txn.Then(clientv3.OpPut(key, string(newLeaderData)))
				txnResp, _ := txn.Commit()
				if txnResp.Succeeded {
					return brokerID, nil
				}
				return brokerID, nil // Even if CAS failed, this broker is the leader
			}
		}
	}

	// If no ISR candidate, pick from replicas
	if len(replicas) > 0 {
		for _, brokerID := range replicas {
			brokerKey := fmt.Sprintf("%s%d", BrokerPrefix, brokerID)
			brokerResp, err := ec.client.Get(ctx, brokerKey)
			if err == nil && len(brokerResp.Kvs) > 0 {
				newLeaderData, _ := json.Marshal(brokerID)
				_, _ = ec.client.Put(ctx, key, string(newLeaderData))
				return brokerID, nil
			}
		}
	}

	return 0, fmt.Errorf("no healthy broker available for leader election")
}

// GetLeader retrieves the leader for a partition from etcd
func (ec *EtcdCoordinator) GetLeader(ctx context.Context, topic string, partition int32) (int32, error) {
	key := fmt.Sprintf("%s%s-%d", LeaderPrefix, topic, partition)
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("failed to get leader: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return 0, fmt.Errorf("no leader for partition %s-%d", topic, partition)
	}

	var leader int32
	if err := json.Unmarshal(resp.Kvs[0].Value, &leader); err != nil {
		return 0, fmt.Errorf("failed to unmarshal leader: %w", err)
	}

	return leader, nil
}

// IsLeader checks if a broker is the leader for a partition
func (ec *EtcdCoordinator) IsLeader(ctx context.Context, topic string, partition int32, brokerID int32) (bool, error) {
	leader, err := ec.GetLeader(ctx, topic, partition)
	if err != nil {
		return false, err
	}

	return leader == brokerID, nil
}

// UpdateISR updates the ISR for a partition in etcd
func (ec *EtcdCoordinator) UpdateISR(ctx context.Context, topic string, partition int32, isr []int32) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%s-%d", ISRPrefix, topic, partition)
	data, err := json.Marshal(isr)
	if err != nil {
		return fmt.Errorf("failed to marshal ISR: %w", err)
	}

	_, err = ec.client.Put(ctx, key, string(data))
	if err != nil {
		return fmt.Errorf("failed to update ISR: %w", err)
	}

	return nil
}

// GetISR retrieves the ISR for a partition from etcd
func (ec *EtcdCoordinator) GetISR(ctx context.Context, topic string, partition int32) ([]int32, error) {
	key := fmt.Sprintf("%s%s-%d", ISRPrefix, topic, partition)
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get ISR: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("ISR not found for partition %s-%d", topic, partition)
	}

	var isr []int32
	if err := json.Unmarshal(resp.Kvs[0].Value, &isr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ISR: %w", err)
	}

	return isr, nil
}

// CreateTopicMetadata creates metadata for a topic in etcd
func (ec *EtcdCoordinator) CreateTopicMetadata(ctx context.Context, topicName string, numPartitions int32, replicationFactor int32) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%s", TopicPrefix, topicName)

	metadata := &TopicMetadata{
		Name:              topicName,
		NumPartitions:     numPartitions,
		ReplicationFactor: replicationFactor,
		Partitions:        make(map[int32]*PartitionMetadata),
		CreatedAt:         time.Now().Unix(),
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal topic metadata: %w", err)
	}

	// Use Compare-and-Swap to prevent duplicates
	txn := ec.client.Txn(ctx)
	txn = txn.If(clientv3.Compare(clientv3.Version(key), "=", 0))
	txn = txn.Then(clientv3.OpPut(key, string(data)))
	txnResp, err := txn.Commit()
	if err != nil {
		return fmt.Errorf("failed to create topic metadata: %w", err)
	}

	if !txnResp.Succeeded {
		return fmt.Errorf("topic %s already exists", topicName)
	}

	return nil
}

// GetTopicMetadata retrieves metadata for a topic from etcd
func (ec *EtcdCoordinator) GetTopicMetadata(ctx context.Context, topicName string) (*TopicMetadata, error) {
	key := fmt.Sprintf("%s%s", TopicPrefix, topicName)
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get topic metadata: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("topic %s not found", topicName)
	}

	var metadata TopicMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal topic metadata: %w", err)
	}

	return &metadata, nil
}

// ListTopics returns all topics from etcd
func (ec *EtcdCoordinator) ListTopics(ctx context.Context) ([]string, error) {
	resp, err := ec.client.Get(ctx, TopicPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to list topics: %w", err)
	}

	topics := make([]string, 0)
	for _, kv := range resp.Kvs {
		// Extract topic name from key
		key := string(kv.Key)
		topicName := strings.TrimPrefix(key, TopicPrefix)
		topics = append(topics, topicName)
	}

	return topics, nil
}

// JoinConsumerGroup adds a consumer to a group in etcd
func (ec *EtcdCoordinator) JoinConsumerGroup(ctx context.Context, groupID string, memberID string, topics []string) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%s/members/%s", ConsumerPrefix, groupID, memberID)
	memberData := map[string]interface{}{
		"id":        memberID,
		"topics":    topics,
		"joined_at": time.Now().Unix(),
	}

	data, err := json.Marshal(memberData)
	if err != nil {
		return fmt.Errorf("failed to marshal member data: %w", err)
	}

	_, err = ec.client.Put(ctx, key, string(data), clientv3.WithLease(ec.leaseID))
	if err != nil {
		return fmt.Errorf("failed to join consumer group: %w", err)
	}

	return nil
}

// LeaveConsumerGroup removes a consumer from a group in etcd
func (ec *EtcdCoordinator) LeaveConsumerGroup(ctx context.Context, groupID string, memberID string) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%s/members/%s", ConsumerPrefix, groupID, memberID)
	_, err := ec.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to leave consumer group: %w", err)
	}

	return nil
}

// GetGroupMembers returns members of a consumer group from etcd
func (ec *EtcdCoordinator) GetGroupMembers(ctx context.Context, groupID string) ([]string, error) {
	prefix := fmt.Sprintf("%s%s/members/", ConsumerPrefix, groupID)
	resp, err := ec.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to get group members: %w", err)
	}

	members := make([]string, 0)
	for _, kv := range resp.Kvs {
		key := string(kv.Key)
		memberID := strings.TrimPrefix(key, prefix)
		members = append(members, memberID)
	}

	return members, nil
}

// CommitOffset commits an offset for a consumer group in etcd
func (ec *EtcdCoordinator) CommitOffset(ctx context.Context, groupID string, topic string, partition int32, offset int64) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%s/%s-%d", OffsetPrefix, groupID, topic, partition)
	offsetData, err := json.Marshal(offset)
	if err != nil {
		return fmt.Errorf("failed to marshal offset: %w", err)
	}

	_, err = ec.client.Put(ctx, key, string(offsetData))
	if err != nil {
		return fmt.Errorf("failed to commit offset: %w", err)
	}

	return nil
}

// GetCommittedOffset retrieves the committed offset for a consumer group from etcd
func (ec *EtcdCoordinator) GetCommittedOffset(ctx context.Context, groupID string, topic string, partition int32) (int64, error) {
	key := fmt.Sprintf("%s%s/%s-%d", OffsetPrefix, groupID, topic, partition)
	resp, err := ec.client.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("failed to get committed offset: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return 0, nil // Return 0 if not found
	}

	var offset int64
	if err := json.Unmarshal(resp.Kvs[0].Value, &offset); err != nil {
		return 0, fmt.Errorf("failed to unmarshal offset: %w", err)
	}

	return offset, nil
}

// HeartbeatBroker updates broker heartbeat in etcd
func (ec *EtcdCoordinator) HeartbeatBroker(ctx context.Context, brokerID int32) error {
	if !ec.running {
		return fmt.Errorf("coordinator not running")
	}

	key := fmt.Sprintf("%s%d", BrokerPrefix, brokerID)
	metadata, err := ec.GetBroker(ctx, brokerID)
	if err != nil {
		return err
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal broker metadata: %w", err)
	}

	_, err = ec.client.Put(ctx, key, string(data), clientv3.WithLease(ec.leaseID))
	if err != nil {
		return fmt.Errorf("failed to heartbeat broker: %w", err)
	}

	return nil
}

// WatchBrokerHealth watches for broker health changes in etcd
func (ec *EtcdCoordinator) WatchBrokerHealth(ctx context.Context) (<-chan BrokerHealthEvent, error) {
	if !ec.running {
		return nil, fmt.Errorf("coordinator not running")
	}

	eventCh := make(chan BrokerHealthEvent, 10)

	// Watch broker keys for changes
	watchCh := ec.client.Watch(ctx, BrokerPrefix, clientv3.WithPrefix())

	go func() {
		for watchResp := range watchCh {
			for _, event := range watchResp.Events {
				brokerID := parseBrokerID(string(event.Kv.Key))
				if brokerID > 0 {
					isHealthy := event.Type == clientv3.EventTypePut
					eventCh <- BrokerHealthEvent{
						BrokerID:  brokerID,
						IsHealthy: isHealthy,
						Timestamp: time.Now(),
					}
				}
			}
		}
		close(eventCh)
	}()

	return eventCh, nil
}

// heartbeatLoop periodically renews the lease
func (ec *EtcdCoordinator) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(ec.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := ec.client.KeepAliveOnce(ctx, ec.leaseID)
			if err != nil {
				fmt.Printf("failed to keep alive lease: %v\n", err)
			}
		}
	}
}

// parseBrokerID extracts broker ID from etcd key
func parseBrokerID(key string) int32 {
	parts := strings.Split(strings.TrimPrefix(key, BrokerPrefix), "/")
	if len(parts) > 0 {
		var id int32
		if _, err := fmt.Sscanf(parts[0], "%d", &id); err == nil {
			return id
		}
	}
	return 0
}
