package broker

import "errors"

// Error definitions
var (
	ErrTopicExists       = errors.New("topic already exists")
	ErrTopicNotFound     = errors.New("topic not found")
	ErrPartitionNotFound = errors.New("partition not found")
	ErrInvalidOffset     = errors.New("invalid offset")
	ErrBrokerNotHealthy  = errors.New("broker is not healthy")
	ErrReplicationFailed = errors.New("replication failed")
	ErrLeaderNotFound    = errors.New("leader not found")
)
