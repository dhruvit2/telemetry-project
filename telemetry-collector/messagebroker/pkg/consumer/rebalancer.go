package consumer

import (
	"fmt"
	"sort"
)

// Rebalancer handles different partition assignment strategies
type Rebalancer interface {
	Assign(members []string, numPartitions int) map[string][]int32
	Name() string
}

// RoundRobinRebalancer assigns partitions in a round-robin fashion
type RoundRobinRebalancer struct{}

func (r *RoundRobinRebalancer) Name() string {
	return "RoundRobin"
}

func (r *RoundRobinRebalancer) Assign(members []string, numPartitions int) map[string][]int32 {
	assignments := make(map[string][]int32)

	// Initialize empty partition lists
	for _, memberID := range members {
		assignments[memberID] = make([]int32, 0)
	}

	if len(members) == 0 {
		return assignments
	}

	// Assign partitions round-robin
	for partition := 0; partition < numPartitions; partition++ {
		memberIdx := partition % len(members)
		assignments[members[memberIdx]] = append(assignments[members[memberIdx]], int32(partition))
	}

	return assignments
}

// RangeRebalancer assigns consecutive partitions to members
type RangeRebalancer struct{}

func (r *RangeRebalancer) Name() string {
	return "Range"
}

func (r *RangeRebalancer) Assign(members []string, numPartitions int) map[string][]int32 {
	assignments := make(map[string][]int32)

	// Initialize empty partition lists
	for _, memberID := range members {
		assignments[memberID] = make([]int32, 0)
	}

	if len(members) == 0 {
		return assignments
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

// StickyRebalancer minimizes partition movement during rebalancing
type StickyRebalancer struct {
	previousAssignments map[string][]int32
}

func NewStickyRebalancer(previousAssignments map[string][]int32) *StickyRebalancer {
	return &StickyRebalancer{
		previousAssignments: previousAssignments,
	}
}

func (s *StickyRebalancer) Name() string {
	return "Sticky"
}

func (s *StickyRebalancer) Assign(members []string, numPartitions int) map[string][]int32 {
	assignments := make(map[string][]int32)

	// Initialize with previous assignments for existing members
	memberSet := make(map[string]bool)
	for _, member := range members {
		memberSet[member] = true
		if previous, exists := s.previousAssignments[member]; exists {
			assignments[member] = make([]int32, len(previous))
			copy(assignments[member], previous)
		} else {
			assignments[member] = make([]int32, 0)
		}
	}

	// Reclaim partitions from members no longer in the group
	reclaimedPartitions := make([]int32, 0)
	for memberID, partitions := range s.previousAssignments {
		if !memberSet[memberID] {
			reclaimedPartitions = append(reclaimedPartitions, partitions...)
		}
	}

	// Find unassigned partitions
	assignedPartitions := make(map[int32]bool)
	for _, partitions := range assignments {
		for _, partition := range partitions {
			assignedPartitions[partition] = true
		}
	}

	unassignedPartitions := make([]int32, 0)
	for partition := 0; partition < numPartitions; partition++ {
		if !assignedPartitions[int32(partition)] {
			unassignedPartitions = append(unassignedPartitions, int32(partition))
		}
	}

	// Combine reclaimed and unassigned partitions
	partitionsToAssign := append(reclaimedPartitions, unassignedPartitions...)

	// Sort members by current partition count (ascending) for balanced distribution
	type memberLoad struct {
		memberID string
		count    int
	}

	loads := make([]memberLoad, 0)
	for _, member := range members {
		loads = append(loads, memberLoad{member, len(assignments[member])})
	}

	sort.Slice(loads, func(i, j int) bool {
		return loads[i].count < loads[j].count
	})

	// Assign partitions to least-loaded members
	for _, partition := range partitionsToAssign {
		minIdx := 0
		for i := 1; i < len(loads); i++ {
			if len(assignments[loads[i].memberID]) < len(assignments[loads[minIdx].memberID]) {
				minIdx = i
			}
		}
		assignments[loads[minIdx].memberID] = append(assignments[loads[minIdx].memberID], partition)
	}

	return assignments
}

// RebalancerFactory creates rebalancer instances based on strategy name
type RebalancerFactory struct{}

func (f *RebalancerFactory) Create(strategy string, previousAssignments map[string][]int32) (Rebalancer, error) {
	switch strategy {
	case "RoundRobin":
		return &RoundRobinRebalancer{}, nil
	case "Range":
		return &RangeRebalancer{}, nil
	case "Sticky":
		return NewStickyRebalancer(previousAssignments), nil
	default:
		return nil, fmt.Errorf("unknown rebalancing strategy: %s", strategy)
	}
}

// RebalanceMetrics tracks rebalancing statistics
type RebalanceMetrics struct {
	Strategy            string
	Timestamp           int64
	PartitionsMoved     int
	PreviousAssignments map[string][]int32
	NewAssignments      map[string][]int32
	Duration            int64 // milliseconds
	SkewBefore          float64
	SkewAfter           float64
}

// CalculateAssignmentSkew calculates how balanced the assignments are
func CalculateAssignmentSkew(assignments map[string][]int32) float64 {
	if len(assignments) == 0 {
		return 0
	}

	counts := make([]int, 0)
	for _, partitions := range assignments {
		counts = append(counts, len(partitions))
	}

	// Calculate mean
	sum := 0
	for _, count := range counts {
		sum += count
	}
	mean := float64(sum) / float64(len(counts))

	// Calculate variance
	variance := 0.0
	for _, count := range counts {
		diff := float64(count) - mean
		variance += diff * diff
	}
	variance /= float64(len(counts))

	// Return standard deviation as skew measure
	if variance < 0 {
		return 0
	}
	return variance // sqrt would give stddev
}

// CountPartitionMoves counts how many partitions moved between assignments
func CountPartitionMoves(previous, current map[string][]int32) int {
	moves := 0

	// Create maps for easier lookup
	prevMap := make(map[int32]string)
	for memberID, partitions := range previous {
		for _, partition := range partitions {
			prevMap[partition] = memberID
		}
	}

	currMap := make(map[int32]string)
	for memberID, partitions := range current {
		for _, partition := range partitions {
			currMap[partition] = memberID
		}
	}

	// Count changes
	for partition, newMember := range currMap {
		if oldMember, exists := prevMap[partition]; exists && oldMember != newMember {
			moves++
		}
	}

	return moves
}
