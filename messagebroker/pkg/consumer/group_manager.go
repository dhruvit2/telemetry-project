package consumer

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/dhruvit2/messagebroker/pkg/coordinator"
)

// ConsumerGroupState represents the state of a consumer group
type ConsumerGroupState string

const (
	StateEmpty       ConsumerGroupState = "Empty"
	StatePreparing   ConsumerGroupState = "Preparing"
	StateStable      ConsumerGroupState = "Stable"
	StateRebalancing ConsumerGroupState = "Rebalancing"
	StateDead        ConsumerGroupState = "Dead"
)

// AssignmentStrategy defines how partitions are assigned to members
type AssignmentStrategy string

const (
	StrategyRoundRobin AssignmentStrategy = "RoundRobin"
	StrategyRange      AssignmentStrategy = "Range"
	StrategySticky     AssignmentStrategy = "Sticky"
)

// ConsumerGroupManager manages consumer groups and handles rebalancing
type ConsumerGroupManager struct {
	coordinator      coordinator.Coordinator
	groups           map[string]*GroupState
	mu               sync.RWMutex
	rebalanceTimeout time.Duration
	sessionTimeout   time.Duration
	heartbeatTicker  *time.Ticker
	running          bool
	cancel           context.CancelFunc
}

// GroupState represents the internal state of a consumer group
type GroupState struct {
	GroupID             string
	State               ConsumerGroupState
	Topic               string
	Members             map[string]*MemberState
	CurrentAssignments  map[string][]int32 // memberID -> assigned partitions
	PreviousAssignments map[string][]int32
	GenerationID        int32
	LeaderID            string
	LastRebalanceTime   time.Time
	mu                  sync.RWMutex
}

// MemberState represents the state of a consumer group member
type MemberState struct {
	MemberID         string
	ClientID         string
	Host             string
	Topics           []string
	SessionTimeout   time.Duration
	RebalanceTimeout time.Duration
	JoinedAt         time.Time
	LastHeartbeat    time.Time
	GenerationID     int32
	State            string // "JoiningGroup", "SyncingGroup", "Stable"
}

// NewConsumerGroupManager creates a new consumer group manager
func NewConsumerGroupManager(
	coord coordinator.Coordinator,
	rebalanceTimeout,
	sessionTimeout time.Duration,
) *ConsumerGroupManager {
	return &ConsumerGroupManager{
		coordinator:      coord,
		groups:           make(map[string]*GroupState),
		rebalanceTimeout: rebalanceTimeout,
		sessionTimeout:   sessionTimeout,
		running:          false,
	}
}

// Start initializes the consumer group manager
func (cgm *ConsumerGroupManager) Start(ctx context.Context) error {
	if cgm.running {
		return fmt.Errorf("consumer group manager already running")
	}

	cgm.running = true
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	cgm.cancel = cancel

	// Start heartbeat and rebalance monitor
	go cgm.monitorLoop(ctx)

	return nil
}

// Stop shuts down the consumer group manager
func (cgm *ConsumerGroupManager) Stop(ctx context.Context) error {
	cgm.mu.Lock()
	defer cgm.mu.Unlock()

	if !cgm.running {
		return nil
	}

	cgm.running = false
	if cgm.cancel != nil {
		cgm.cancel()
	}

	if cgm.heartbeatTicker != nil {
		cgm.heartbeatTicker.Stop()
	}

	return nil
}

// JoinConsumerGroup adds a member to a consumer group
func (cgm *ConsumerGroupManager) JoinConsumerGroup(
	ctx context.Context,
	groupID,
	memberID,
	clientID,
	host string,
	topics []string,
	sessionTimeout,
	rebalanceTimeout time.Duration,
) (int32, []string, error) {
	cgm.mu.Lock()
	defer cgm.mu.Unlock()

	if !cgm.running {
		return 0, nil, fmt.Errorf("consumer group manager not running")
	}

	// Get or create group
	groupState, exists := cgm.groups[groupID]
	if !exists {
		groupState = &GroupState{
			GroupID:             groupID,
			State:               StateEmpty,
			Members:             make(map[string]*MemberState),
			CurrentAssignments:  make(map[string][]int32),
			PreviousAssignments: make(map[string][]int32),
			GenerationID:        0,
		}
		cgm.groups[groupID] = groupState
	}

	groupState.mu.Lock()
	defer groupState.mu.Unlock()

	// Add member to group
	member := &MemberState{
		MemberID:         memberID,
		ClientID:         clientID,
		Host:             host,
		Topics:           topics,
		SessionTimeout:   sessionTimeout,
		RebalanceTimeout: rebalanceTimeout,
		JoinedAt:         time.Now(),
		LastHeartbeat:    time.Now(),
		State:            "JoiningGroup",
	}

	groupState.Members[memberID] = member
	groupState.Topic = topics[0] // For simplicity, assume single topic

	// Set as leader if first member
	if len(groupState.Members) == 1 {
		groupState.LeaderID = memberID
	}

	// Register with coordinator
	if err := cgm.coordinator.JoinConsumerGroup(ctx, groupID, memberID, topics); err != nil {
		delete(groupState.Members, memberID)
		return 0, nil, fmt.Errorf("failed to join consumer group: %w", err)
	}

	// Trigger rebalance
	oldGenerationID := groupState.GenerationID
	groupState.GenerationID++
	groupState.State = StateRebalancing

	// Get all members
	members := make([]string, 0, len(groupState.Members))
	for m := range groupState.Members {
		members = append(members, m)
	}

	// Perform rebalancing
	if err := cgm.rebalanceGroup(ctx, groupState); err != nil {
		groupState.GenerationID = oldGenerationID
		groupState.State = StateStable
		return oldGenerationID, nil, fmt.Errorf("failed to rebalance group: %w", err)
	}

	groupState.State = StateStable
	groupState.LastRebalanceTime = time.Now()

	return groupState.GenerationID, members, nil
}

// LeaveConsumerGroup removes a member from a consumer group
func (cgm *ConsumerGroupManager) LeaveConsumerGroup(
	ctx context.Context,
	groupID,
	memberID string,
) error {
	cgm.mu.Lock()
	defer cgm.mu.Unlock()

	groupState, exists := cgm.groups[groupID]
	if !exists {
		return fmt.Errorf("consumer group %s not found", groupID)
	}

	groupState.mu.Lock()
	defer groupState.mu.Unlock()

	if _, exists := groupState.Members[memberID]; !exists {
		return fmt.Errorf("member %s not in group %s", memberID, groupID)
	}

	// Remove member
	delete(groupState.Members, memberID)
	delete(groupState.CurrentAssignments, memberID)

	// Deregister with coordinator
	_ = cgm.coordinator.LeaveConsumerGroup(ctx, groupID, memberID)

	// If group is empty, mark as dead
	if len(groupState.Members) == 0 {
		groupState.State = StateEmpty
		return nil
	}

	// Elect new leader if needed
	if groupState.LeaderID == memberID {
		for m := range groupState.Members {
			groupState.LeaderID = m
			break
		}
	}

	// Trigger rebalance
	groupState.State = StateRebalancing
	groupState.GenerationID++

	if err := cgm.rebalanceGroup(ctx, groupState); err != nil {
		return fmt.Errorf("failed to rebalance after member removal: %w", err)
	}

	groupState.State = StateStable
	groupState.LastRebalanceTime = time.Now()

	return nil
}

// FetchAssignments returns the assigned partitions for a member
func (cgm *ConsumerGroupManager) FetchAssignments(
	ctx context.Context,
	groupID,
	memberID string,
) ([]int32, int32, error) {
	cgm.mu.RLock()
	groupState, exists := cgm.groups[groupID]
	cgm.mu.RUnlock()

	if !exists {
		return nil, 0, fmt.Errorf("consumer group %s not found", groupID)
	}

	groupState.mu.RLock()
	defer groupState.mu.RUnlock()

	if _, exists := groupState.Members[memberID]; !exists {
		return nil, 0, fmt.Errorf("member %s not in group %s", memberID, groupID)
	}

	assignments := groupState.CurrentAssignments[memberID]
	return assignments, groupState.GenerationID, nil
}

// HeartbeatMember updates the heartbeat for a group member
func (cgm *ConsumerGroupManager) HeartbeatMember(
	ctx context.Context,
	groupID,
	memberID string,
) (int32, error) {
	cgm.mu.RLock()
	groupState, exists := cgm.groups[groupID]
	cgm.mu.RUnlock()

	if !exists {
		return 0, fmt.Errorf("consumer group %s not found", groupID)
	}

	groupState.mu.Lock()
	defer groupState.mu.Unlock()

	member, exists := groupState.Members[memberID]
	if !exists {
		return 0, fmt.Errorf("member %s not in group %s", memberID, groupID)
	}

	member.LastHeartbeat = time.Now()
	return groupState.GenerationID, nil
}

// CommitOffset commits an offset for a member
func (cgm *ConsumerGroupManager) CommitOffset(
	ctx context.Context,
	groupID,
	topic string,
	partition int32,
	offset int64,
) error {
	if err := cgm.coordinator.CommitOffset(ctx, groupID, topic, partition, offset); err != nil {
		return fmt.Errorf("failed to commit offset: %w", err)
	}
	return nil
}

// FetchOffset retrieves the committed offset for a partition
func (cgm *ConsumerGroupManager) FetchOffset(
	ctx context.Context,
	groupID,
	topic string,
	partition int32,
) (int64, error) {
	offset, err := cgm.coordinator.GetCommittedOffset(ctx, groupID, topic, partition)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch offset: %w", err)
	}
	return offset, nil
}

// DescribeConsumerGroup returns detailed information about a consumer group
func (cgm *ConsumerGroupManager) DescribeConsumerGroup(
	ctx context.Context,
	groupID string,
) (map[string]interface{}, error) {
	cgm.mu.RLock()
	groupState, exists := cgm.groups[groupID]
	cgm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("consumer group %s not found", groupID)
	}

	groupState.mu.RLock()
	defer groupState.mu.RUnlock()

	members := make([]map[string]interface{}, 0)
	for memberID, member := range groupState.Members {
		members = append(members, map[string]interface{}{
			"member_id":  memberID,
			"client_id":  member.ClientID,
			"host":       member.Host,
			"partitions": groupState.CurrentAssignments[memberID],
			"joined_at":  member.JoinedAt,
		})
	}

	return map[string]interface{}{
		"group_id":       groupID,
		"state":          groupState.State,
		"generation_id":  groupState.GenerationID,
		"topic":          groupState.Topic,
		"members_count":  len(groupState.Members),
		"members":        members,
		"last_rebalance": groupState.LastRebalanceTime,
	}, nil
}

// ListConsumerGroups returns all consumer groups
func (cgm *ConsumerGroupManager) ListConsumerGroups(ctx context.Context) []string {
	cgm.mu.RLock()
	defer cgm.mu.RUnlock()

	groups := make([]string, 0, len(cgm.groups))
	for groupID := range cgm.groups {
		groups = append(groups, groupID)
	}
	return groups
}

// rebalanceGroup performs partition rebalancing for a consumer group
func (cgm *ConsumerGroupManager) rebalanceGroup(ctx context.Context, groupState *GroupState) error {
	if len(groupState.Members) == 0 {
		groupState.CurrentAssignments = make(map[string][]int32)
		return nil
	}

	// Save previous assignments
	groupState.PreviousAssignments = make(map[string][]int32)
	for memberID, partitions := range groupState.CurrentAssignments {
		groupState.PreviousAssignments[memberID] = partitions
	}

	// Get topic metadata from coordinator (for consumer group coordination)
	topicMetadata, err := cgm.coordinator.GetTopicMetadata(ctx, groupState.Topic)
	if err != nil {
		fmt.Printf("rebalanceGroup WARNING: coordinator GetTopicMetadata failed: %v (topic may not be registered yet)\n", err)
		// Fallback: Try to get partition count from coordinator's group metadata
		// If coordinator is completely unavailable, assign 0 partitions and retry later
		numPartitions := 0

		members := make([]string, 0)
		for memberID := range groupState.Members {
			members = append(members, memberID)
		}

		// Assign 0 partitions (topic metadata not available yet)
		assignments := cgm.assignPartitionsRoundRobin(members, numPartitions)
		groupState.CurrentAssignments = assignments
		fmt.Printf("rebalanceGroup: assigned 0 partitions to %d members (producer may need to create topic or coordinator may be unavailable)\n", len(members))
		return nil
	}

	// Get members from coordinator to ensure global view across all brokers
	members, err := cgm.coordinator.GetGroupMembers(ctx, groupState.GroupID)
	if err != nil || len(members) == 0 {
		fmt.Printf("rebalanceGroup WARNING: coordinator GetGroupMembers failed or empty, using local members: %v\n", err)
		members = make([]string, 0)
		for memberID := range groupState.Members {
			members = append(members, memberID)
		}
	}

	// Sort members deterministically so all brokers calculate the exact same assignments
	sort.Strings(members)

	numPartitions := int(topicMetadata.NumPartitions)
	fmt.Printf("rebalanceGroup: topic=%s, numPartitions=%d, numMembers=%d (global)\n",
		groupState.Topic, numPartitions, len(members))

	// Perform round-robin assignment
	assignments := cgm.assignPartitionsRoundRobin(members, numPartitions)

	// Update assignments
	groupState.CurrentAssignments = assignments

	// Commit assignments to coordinator
	for memberID, partitions := range assignments {
		for _, partition := range partitions {
			// Partitions are automatically tracked by coordinator via group membership
			_ = partition
		}
		_ = memberID
	}

	return nil
}

// assignPartitionsRoundRobin assigns partitions using round-robin strategy
func (cgm *ConsumerGroupManager) assignPartitionsRoundRobin(members []string, numPartitions int) map[string][]int32 {
	assignments := make(map[string][]int32)

	// Initialize empty partition lists
	for _, memberID := range members {
		assignments[memberID] = make([]int32, 0)
	}

	// Assign partitions round-robin
	for partition := 0; partition < numPartitions; partition++ {
		memberIdx := partition % len(members)
		assignments[members[memberIdx]] = append(assignments[members[memberIdx]], int32(partition))
	}

	return assignments
}

// assignPartitionsRange assigns partitions using range strategy
func (cgm *ConsumerGroupManager) assignPartitionsRange(members []string, numPartitions int) map[string][]int32 {
	assignments := make(map[string][]int32)

	// Initialize empty partition lists
	for _, memberID := range members {
		assignments[memberID] = make([]int32, 0)
	}

	// Calculate partitions per member
	partitionsPerMember := numPartitions / len(members)
	remainder := numPartitions % len(members)

	partitionIdx := 0
	for i, memberID := range members {
		// First 'remainder' members get one extra partition
		count := partitionsPerMember
		if i < remainder {
			count++
		}

		for j := 0; j < count; j++ {
			assignments[memberID] = append(assignments[memberID], int32(partitionIdx))
			partitionIdx++
		}
	}

	return assignments
}

// monitorLoop monitors group membership and triggers rebalancing
func (cgm *ConsumerGroupManager) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cgm.checkMembersHealth()
		}
	}
}

// checkMembersHealth checks if members are still alive and triggers rebalance if needed
func (cgm *ConsumerGroupManager) checkMembersHealth() {
	cgm.mu.Lock()
	defer cgm.mu.Unlock()

	now := time.Now()

	for groupID, groupState := range cgm.groups {
		groupState.mu.Lock()

		deadMembers := make([]string, 0)

		// Check each member's heartbeat
		for memberID, member := range groupState.Members {
			if now.Sub(member.LastHeartbeat) > member.SessionTimeout {
				deadMembers = append(deadMembers, memberID)
			}
		}

		// Remove dead members and trigger rebalance
		if len(deadMembers) > 0 {
			for _, memberID := range deadMembers {
				delete(groupState.Members, memberID)
				delete(groupState.CurrentAssignments, memberID)
			}

			if len(groupState.Members) > 0 {
				// Trigger rebalance
				groupState.State = StateRebalancing
				groupState.GenerationID++

				if err := cgm.rebalanceGroup(context.Background(), groupState); err != nil {
					fmt.Printf("failed to rebalance group %s after member failure: %v\n", groupID, err)
				}

				groupState.State = StateStable
			} else {
				groupState.State = StateEmpty
			}
		}

		groupState.mu.Unlock()
	}
}
