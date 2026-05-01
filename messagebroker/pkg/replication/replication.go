package replication

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ReplicaSync handles replication between brokers
type ReplicaSync struct {
	PartitionID         int32
	Topic               string
	Leader              int32
	Followers           []int32
	ISR                 []int32
	LastReplicationTime time.Time
	mu                  sync.RWMutex
}

// LeaderElection manages leader election for partitions
type LeaderElection struct {
	PartitionID   int32
	Topic         string
	CurrentLeader int32
	Candidates    []int32
	Epoch         int32
	ElectionTime  time.Time
	mu            sync.RWMutex
}

// ReplicationManager handles all replication logic
type ReplicationManager struct {
	brokerID          int
	replicaSyncs      map[string]*ReplicaSync    // "topic-partition" -> ReplicaSync
	leaderElections   map[string]*LeaderElection // "topic-partition" -> LeaderElection
	inSyncReplicas    map[string][]int32         // "topic-partition" -> ISR list
	followerOffsets   map[string]map[int32]int64 // "topic-partition" -> brokerID -> offset
	replicationFactor int32
	minISR            int32
	mu                sync.RWMutex
}

// NewReplicationManager creates a new replication manager
func NewReplicationManager(brokerID int, replicationFactor int32, minISR int32) *ReplicationManager {
	return &ReplicationManager{
		brokerID:          brokerID,
		replicaSyncs:      make(map[string]*ReplicaSync),
		leaderElections:   make(map[string]*LeaderElection),
		inSyncReplicas:    make(map[string][]int32),
		followerOffsets:   make(map[string]map[int32]int64),
		replicationFactor: replicationFactor,
		minISR:            minISR,
	}
}

// InitiateReplication sets up replication for a partition
func (rm *ReplicationManager) InitiateReplication(ctx context.Context, topic string, partitionID int32, leader int32, replicas []int32) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	key := topic + "-" + string(rune(partitionID))

	rs := &ReplicaSync{
		PartitionID:         partitionID,
		Topic:               topic,
		Leader:              leader,
		Followers:           removeElement(replicas, leader),
		ISR:                 replicas,
		LastReplicationTime: time.Now(),
	}

	rm.replicaSyncs[key] = rs
	rm.inSyncReplicas[key] = replicas
	rm.followerOffsets[key] = make(map[int32]int64)

	for _, replica := range replicas {
		rm.followerOffsets[key][replica] = 0
	}

	return nil
}

// RecordReplication records that a replica has synced up to offset
func (rm *ReplicationManager) RecordReplication(ctx context.Context, topic string, partitionID int32, brokerID int32, offset int64) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	key := topic + "-" + string(rune(partitionID))

	if offsets, exists := rm.followerOffsets[key]; exists {
		offsets[brokerID] = offset
		rm.followerOffsets[key] = offsets
	}

	// Update ISR if replica is caught up
	rs, exists := rm.replicaSyncs[key]
	if exists && rs.Leader != brokerID {
		// Check if replica is in sync (within threshold)
		const offsetThreshold = 10
		if offset >= rs.LastReplicationTime.Unix()-offsetThreshold {
			// Add to ISR if not already there
			found := false
			for _, id := range rm.inSyncReplicas[key] {
				if id == brokerID {
					found = true
					break
				}
			}
			if !found {
				rm.inSyncReplicas[key] = append(rm.inSyncReplicas[key], brokerID)
			}
		}
	}

	return nil
}

// DetectFailure detects broker failure and handles it
func (rm *ReplicationManager) DetectFailure(ctx context.Context, failedBrokerID int32) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	for key, rs := range rm.replicaSyncs {
		// Remove failed broker from ISR
		newISR := make([]int32, 0)
		for _, id := range rm.inSyncReplicas[key] {
			if id != failedBrokerID {
				newISR = append(newISR, id)
			}
		}

		// Check if ISR size is below minimum
		if int32(len(newISR)) < rm.minISR {
			// Log warning but continue
			_ = newISR
		} else {
			rm.inSyncReplicas[key] = newISR
		}

		// If leader failed, trigger leader election
		if rs.Leader == failedBrokerID {
			rm.triggerLeaderElection(key, rs)
		}
	}

	return nil
}

// triggerLeaderElection initiates leader election
func (rm *ReplicationManager) triggerLeaderElection(key string, rs *ReplicaSync) {
	le := &LeaderElection{
		PartitionID:   rs.PartitionID,
		Topic:         rs.Topic,
		CurrentLeader: rs.Leader,
		Candidates:    rm.inSyncReplicas[key],
		Epoch:         1,
		ElectionTime:  time.Now(),
	}

	rm.leaderElections[key] = le

	// Select new leader (first in ISR)
	if len(rm.inSyncReplicas[key]) > 0 {
		newLeader := rm.inSyncReplicas[key][0]
		rs.Leader = newLeader
	}
}

// GetISR returns current in-sync replicas
func (rm *ReplicationManager) GetISR(ctx context.Context, topic string, partitionID int32) ([]int32, error) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	key := topic + "-" + string(rune(partitionID))
	if isr, exists := rm.inSyncReplicas[key]; exists {
		return isr, nil
	}

	return nil, ErrPartitionNotFound
}

// GetLeader returns current leader for partition
func (rm *ReplicationManager) GetLeader(ctx context.Context, topic string, partitionID int32) (int32, error) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	key := topic + "-" + string(rune(partitionID))
	if rs, exists := rm.replicaSyncs[key]; exists {
		return rs.Leader, nil
	}

	return -1, ErrPartitionNotFound
}

// Helper function to remove element from slice
func removeElement(slice []int32, elem int32) []int32 {
	result := make([]int32, 0)
	for _, v := range slice {
		if v != elem {
			result = append(result, v)
		}
	}
	return result
}

// Error definitions
var (
	ErrPartitionNotFound = errors.New("partition not found")
	ErrNotLeader         = errors.New("broker is not leader")
)
