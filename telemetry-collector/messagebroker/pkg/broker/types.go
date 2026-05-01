package broker

import (
	"sync"
	"time"
)

// Message represents a single message in the queue
type Message struct {
	ID        string            // Unique message ID
	Topic     string            // Topic name
	Partition int32             // Partition ID
	Key       []byte            // Message key (for routing)
	Value     []byte            // Message payload
	Headers   map[string][]byte // Optional headers
	Timestamp time.Time         // Message timestamp
	Offset    int64             // Message offset in partition
}

// Topic represents a topic configuration
type Topic struct {
	Name              string
	NumPartitions     int32
	ReplicationFactor int32
	Config            map[string]string
	CreatedAt         time.Time
}

// Partition represents a topic partition
type Partition struct {
	ID          int32
	Topic       string
	Leader      int32        // Broker ID of leader
	Replicas    []int32      // List of replica broker IDs
	ISR         []int32      // In-sync replicas
	Offset      int64        // Current offset
	mu          sync.RWMutex // Synchronization
	messages    []*Message   // In-memory message log
	maxMessages int64        // Maximum messages to keep
}

// BrokerConfig holds broker configuration
type BrokerConfig struct {
	ID                int32
	Host              string
	Port              int
	CoordinatorURL    string
	MaxPartitions     int32
	ReplicationFactor int32
	MinISR            int32
	RetentionMs       int64
	SegmentMs         int64
	SegmentBytes      int64
	DataDir           string
	MetadataDir       string
	HeartbeatInterval time.Duration
	SessionTimeout    time.Duration
}

// ConsumerGroup represents a group of consumers
type ConsumerGroup struct {
	ID               string
	Topic            string
	Members          map[string]*ConsumerMember
	PartitionMapping map[int32]string // Partition -> Consumer ID mapping
	mu               sync.RWMutex
}

// ConsumerMember represents a consumer in a group
type ConsumerMember struct {
	ID              string
	Subscriptions   []string
	AssignedOffsets map[int32]int64 // Partition -> Offset mapping
	JoinedAt        time.Time
	LastHeartbeat   time.Time
}

// ReplicationState tracks replication state
type ReplicationState struct {
	PartitionID    int32
	Leader         int32
	Replicas       []int32
	ISR            []int32
	OffsetReplicas map[int32]int64 // Broker ID -> Last replicated offset
	mu             sync.RWMutex
}

// BrokerStatus represents broker health status
type BrokerStatus struct {
	ID              int32
	IsLeader        bool
	IsHealthy       bool
	LastHeartbeat   time.Time
	PartitionLeader map[int32]int32  // Topic+Partition -> Leader
	OffsetLag       map[string]int64 // Topic+Partition -> Offset lag
}
