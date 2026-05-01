package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	groupmanagerpkg "github.com/dhruvit2/messagebroker/pkg/consumer"
	coordinatorpkg "github.com/dhruvit2/messagebroker/pkg/coordinator"
	pb "github.com/dhruvit2/messagebroker/pkg/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// MessageBrokerConsumer consumes messages from messagebroker using consumer groups
type MessageBrokerConsumer struct {
	brokerAddr    string
	topic         string
	groupID       string
	memberID      string
	logger        *zap.Logger
	client        pb.MessageBrokerClient
	conn          *grpc.ClientConn
	groupManager  *groupmanagerpkg.ConsumerGroupManager
	coordinator   coordinatorpkg.Coordinator
	lock          sync.RWMutex
	offsets       map[int32]int64 // partition -> offset
	assignments   []int32         // assigned partitions
	generationID  int32           // current generation
	sessionTTL    time.Duration   // session timeout
	heartbeatTick time.Ticker
	stopHeartbeat chan struct{}
	lastReadIndex int             // tracks the last partition index read for round-robin
}

// NewMessageBrokerConsumer creates a new messagebroker consumer with consumer group support
func NewMessageBrokerConsumer(brokerAddr, topic, groupID string, logger *zap.Logger) (*MessageBrokerConsumer, error) {
	// Create unique member ID (pod-name + timestamp)
	memberID := fmt.Sprintf("%s-%d", groupID, time.Now().UnixNano())

	// Dial the messagebroker
	conn, err := grpc.NewClient(
		brokerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(10*1024*1024)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial messagebroker: %w", err)
	}

	client := pb.NewMessageBrokerClient(conn)

	consumer := &MessageBrokerConsumer{
		brokerAddr:    brokerAddr,
		topic:         topic,
		groupID:       groupID,
		memberID:      memberID,
		logger:        logger,
		client:        client,
		conn:          conn,
		offsets:       make(map[int32]int64),
		assignments:   []int32{},
		generationID:  -1,
		sessionTTL:    30 * time.Second,
		stopHeartbeat: make(chan struct{}),
	}

	logger.Info("MessageBrokerConsumer created",
		zap.String("member_id", memberID),
		zap.String("group_id", groupID),
		zap.String("topic", topic))

	// Wait for topic metadata to be available (producer may have just created it)
	// This prevents the consumer from getting 0 partitions due to metadata lag
	if err := consumer.waitForTopicMetadata(context.Background()); err != nil {
		logger.Warn("failed to verify topic metadata initially (will retry on first consume)",
			zap.Error(err),
			zap.String("topic", topic))
		// Continue anyway - will retry on first consume
	}

	// Join the consumer group
	// Note: joinConsumerGroup will try multiple times if the topic doesn't exist yet
	if err := consumer.joinConsumerGroup(context.Background()); err != nil {
		// Log warning but don't fail - topic might not exist yet (producer might not have started)
		// The first ConsumeMessage() call will retry
		logger.Warn("failed to join consumer group initially",
			zap.Error(err),
			zap.String("topic", topic),
			zap.String("group", groupID))
		// Continue anyway - will retry on first consume
	}

	// Start heartbeat goroutine
	go consumer.heartbeatLoop()

	return consumer, nil
}

// joinConsumerGroup joins the consumer group and gets partition assignments
func (c *MessageBrokerConsumer) joinConsumerGroup(ctx context.Context) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Join consumer group using gRPC
	resp, err := c.client.JoinConsumerGroup(ctx, &pb.JoinConsumerGroupRequest{
		GroupId:  c.groupID,
		MemberId: c.memberID,
		Topics:   []string{c.topic},
	})
	if err != nil {
		return fmt.Errorf("failed to join consumer group: %w", err)
	}

	c.generationID = resp.GenerationId

	// Fetch partition assignments
	assignResp, err := c.client.FetchAssignments(ctx, &pb.FetchAssignmentsRequest{
		GroupId:      c.groupID,
		MemberId:     c.memberID,
		GenerationId: c.generationID,
	})
	if err != nil {
		return fmt.Errorf("failed to fetch assignments: %w", err)
	}

	// Extract partition IDs from assignments
	c.assignments = make([]int32, len(assignResp.Assignments))
	for i, assignment := range assignResp.Assignments {
		c.assignments[i] = assignment.Partition
	}

	c.logger.Info("joined consumer group",
		zap.String("member_id", c.memberID),
		zap.String("group_id", c.groupID),
		zap.Int32("generation_id", c.generationID),
		zap.Int("assigned_partitions", len(c.assignments)),
		zap.Int32s("partitions", c.assignments))

	// Initialize offsets for assigned partitions and fetch committed offsets
	for _, partition := range c.assignments {
		commitResp, err := c.client.FetchOffset(context.Background(), &pb.FetchOffsetRequest{
			GroupId:   c.groupID,
			Topic:     c.topic,
			Partition: partition,
		})
		if err != nil {
			c.logger.Debug("failed to fetch committed offset, starting from 0",
				zap.Int32("partition", partition),
				zap.Error(err))
			c.offsets[partition] = 0
		} else {
			c.offsets[partition] = commitResp.Offset
		}
	}

	return nil
}

// heartbeatLoop placeholder - messagebroker doesn't currently have heartbeat RPC
// In future versions, this can be implemented with a dedicated heartbeat mechanism
func (c *MessageBrokerConsumer) heartbeatLoop() {
	ticker := time.NewTicker(30 * time.Second) // Periodic check interval
	defer ticker.Stop()

	for {
		select {
		case <-c.stopHeartbeat:
			return
		case <-ticker.C:
			// Currently no heartbeat mechanism in messagebroker
			// TODO: Implement when heartbeat RPC is added to messagebroker
		}
	}
}

// waitForTopicMetadata waits for topic metadata to be available on the broker
// This ensures the producer has created the topic and metadata has propagated before we join the group
// Retries with exponential backoff (500ms, 1s, 2s, 4s, 8s, ...) for up to ~60 seconds
func (c *MessageBrokerConsumer) waitForTopicMetadata(ctx context.Context) error {
	maxAttempts := 15 // ~60 seconds with exponential backoff
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 500ms, 1s, 2s, 4s, 8s, 16s, 16s, ...
			backoffMs := 500 * (1 << uint(attempt-1))
			if backoffMs > 16000 {
				backoffMs = 16000 // Cap at 16 seconds
			}
			backoff := time.Duration(backoffMs) * time.Millisecond
			c.logger.Debug("waiting for topic metadata, retrying...",
				zap.String("topic", c.topic),
				zap.Int("attempt", attempt+1),
				zap.Duration("backoff", backoff))
			select {
			case <-time.After(backoff):
				// Continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Fetch topic metadata from broker (not coordinator, which may not have it yet)
		// This directly queries the broker that has the topic data
		metadataResp, err := c.client.GetTopicMetadata(ctx, &pb.GetTopicMetadataRequest{
			Topic: c.topic,
		})
		if err != nil {
			c.logger.Debug("failed to fetch topic metadata from broker",
				zap.String("topic", c.topic),
				zap.Int("attempt", attempt+1),
				zap.Error(err))
			lastErr = err
			continue
		}

		// Check if topic has partitions
		if metadataResp.Topic == c.topic && len(metadataResp.Partitions) > 0 {
			c.logger.Info("topic metadata available from broker",
				zap.String("topic", c.topic),
				zap.Int("partitions", len(metadataResp.Partitions)))
			return nil
		}

		c.logger.Debug("topic exists in broker but has no partitions yet",
			zap.String("topic", c.topic),
			zap.Int("attempt", attempt+1))
		lastErr = fmt.Errorf("topic has 0 partitions")
	}

	// If we couldn't confirm metadata after retries, return the last error
	c.logger.Warn("could not confirm topic metadata after retries (producer may not have created topic yet)",
		zap.String("topic", c.topic),
		zap.Int("attempts", maxAttempts),
		zap.Error(lastErr))
	return fmt.Errorf("topic metadata unavailable after %d attempts: %w", maxAttempts, lastErr)
}

// rejoinConsumerGroup rejoins the group (called during rebalancing)
func (c *MessageBrokerConsumer) rejoinConsumerGroup(ctx context.Context) error {
	resp, err := c.client.JoinConsumerGroup(ctx, &pb.JoinConsumerGroupRequest{
		GroupId:  c.groupID,
		MemberId: c.memberID,
		Topics:   []string{c.topic},
	})
	if err != nil {
		return fmt.Errorf("failed to rejoin consumer group: %w", err)
	}

	c.generationID = resp.GenerationId

	// Fetch partition assignments
	assignResp, err := c.client.FetchAssignments(ctx, &pb.FetchAssignmentsRequest{
		GroupId:      c.groupID,
		MemberId:     c.memberID,
		GenerationId: c.generationID,
	})
	if err != nil {
		return fmt.Errorf("failed to fetch assignments: %w", err)
	}

	// Extract partition IDs from assignments
	c.assignments = make([]int32, len(assignResp.Assignments))
	for i, assignment := range assignResp.Assignments {
		c.assignments[i] = assignment.Partition
	}

	c.logger.Info("rejoined consumer group after rebalancing",
		zap.Int32("generation_id", c.generationID),
		zap.Int("assigned_partitions", len(c.assignments)),
		zap.Int32s("partitions", c.assignments))

	// Reinitialize offsets for new assignments
	for _, partition := range c.assignments {
		commitResp, err := c.client.FetchOffset(context.Background(), &pb.FetchOffsetRequest{
			GroupId:   c.groupID,
			Topic:     c.topic,
			Partition: partition,
		})
		if err != nil {
			c.offsets[partition] = 0
		} else {
			c.offsets[partition] = commitResp.Offset
		}
	}

	return nil
}

// ConsumeMessages consumes a batch of messages from assigned partitions
// Cycles through assigned partitions using round-robin
// Automatically retries joining group if not assigned to any partitions yet
func (c *MessageBrokerConsumer) ConsumeMessages(ctx context.Context) ([]map[string]interface{}, error) {
	c.lock.RLock()
	assignments := c.assignments
	offsets := make(map[int32]int64)
	for k, v := range c.offsets {
		offsets[k] = v
	}
	generationID := c.generationID
	c.lock.RUnlock()

	// If no partitions assigned, try to rejoin the group with retries
	// This handles the case where consumer joins before producer creates the topic
	if len(assignments) == 0 {
		c.logger.Debug("no partitions assigned, attempting to rejoin consumer group with retries")

		// Try rejoin up to 20 times with exponential backoff (total ~65 seconds)
		// This gives time for producer to create topic and metadata to propagate across brokers
		var lastErr error
		for attempt := 0; attempt < 20; attempt++ {
			if attempt > 0 {
				// Exponential backoff: 500ms, 1s, 2s, 4s, 8s, 16s, 16s, ...
				backoffMs := 500 * (1 << uint(attempt-1))
				if backoffMs > 16000 {
					backoffMs = 16000 // Cap at 16 seconds
				}
				backoff := time.Duration(backoffMs) * time.Millisecond
				c.logger.Debug("rejoin attempt delayed",
					zap.Int("attempt", attempt+1),
					zap.Duration("backoff", backoff))
				select {
				case <-time.After(backoff):
					// Continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}

			// Refresh metadata before each rejoin attempt
			// This ensures we get the latest partition information
			if attempt > 0 {
				metaResp, err := c.client.GetTopicMetadata(ctx, &pb.GetTopicMetadataRequest{
					Topic: c.topic,
				})
				if err == nil && len(metaResp.Partitions) > 0 {
					c.logger.Debug("metadata refreshed before rejoin",
						zap.String("topic", c.topic),
						zap.Int("partitions", len(metaResp.Partitions)),
						zap.Int("attempt", attempt+1))
				}
			}

			if err := c.rejoinConsumerGroup(ctx); err != nil {
				c.logger.Debug("failed to rejoin consumer group",
					zap.Int("attempt", attempt+1),
					zap.Error(err))
				lastErr = err
				continue
			}

			// Refresh assignments after rejoin
			c.lock.RLock()
			assignments = c.assignments
			offsets = make(map[int32]int64)
			for k, v := range c.offsets {
				offsets[k] = v
			}
			c.lock.RUnlock()

			if len(assignments) > 0 {
				c.logger.Info("successfully rejoined with assigned partitions",
					zap.Int("assigned_partitions", len(assignments)))
				break
			}
			c.logger.Debug("rejoin successful but still no partitions assigned (topic may not be created yet)",
				zap.Int("attempt", attempt+1))
		}

		// If still no partitions after retries, log and return nil (not an error - topic might not exist yet)
		if len(assignments) == 0 {
			c.logger.Warn("still no partitions assigned after rejoin attempts (producer may not have created topic yet)",
				zap.String("topic", c.topic),
				zap.String("group", c.groupID),
				zap.Error(lastErr))
			return nil, nil // Return nil instead of error to avoid breaking the collection loop
		}
	}

	// Try each assigned partition in order, starting from lastReadIndex to ensure fairness
	numAssignments := len(assignments)
	for i := 0; i < numAssignments; i++ {
		c.lock.RLock()
		idx := (c.lastReadIndex + i) % numAssignments
		c.lock.RUnlock()
		partitionID := assignments[idx]

		offset, ok := offsets[partitionID]
		if !ok {
			offset = 0
		}

		resp, err := c.client.ConsumeMessages(ctx, &pb.ConsumeRequest{
			Topic:       c.topic,
			Partition:   partitionID,
			Offset:      offset,
			MaxMessages: 100, // Fetch up to 100 messages at once
		})
		if err != nil {
			c.logger.Debug("failed to consume from partition",
				zap.Int32("partition", partitionID),
				zap.Int64("offset", offset),
				zap.Error(err))
			continue
		}

		if len(resp.Messages) == 0 {
			c.logger.Debug("no messages available in partition",
				zap.Int32("partition", partitionID),
				zap.Int64("offset", offset))
			continue
		}

		var parsedMsgs []map[string]interface{}
		var lastOffset int64

		for _, msg := range resp.Messages {
			// Parse message value
			var data map[string]interface{}
			if err := json.Unmarshal(msg.Value, &data); err != nil {
				c.logger.Warn("failed to unmarshal message",
					zap.Int32("partition", partitionID),
					zap.Int64("offset", msg.Offset),
					zap.Error(err))
				// Return raw data with metadata
				data = map[string]interface{}{
					"_raw": string(msg.Value),
				}
			}

			// Add source metadata
			data["_source_topic"] = c.topic
			data["_source_partition"] = partitionID
			data["_source_offset"] = msg.Offset
			data["_consumer_group"] = c.groupID
			data["_member_id"] = c.memberID
			data["_generation_id"] = generationID

			parsedMsgs = append(parsedMsgs, data)
			lastOffset = msg.Offset
		}

		// Update offset for this partition
		c.lock.Lock()
		c.offsets[partitionID] = lastOffset + 1
		c.lock.Unlock()

		// Update lastReadIndex to the next partition to ensure fair round-robin
		c.lock.Lock()
		c.lastReadIndex = (idx + 1) % numAssignments
		c.lock.Unlock()

		// Commit offset to coordinator using the LAST message offset
		_, err = c.client.CommitOffset(ctx, &pb.CommitOffsetRequest{
			GroupId:   c.groupID,
			Topic:     c.topic,
			Partition: partitionID,
			Offset:    lastOffset,
		})
		if err != nil {
			c.logger.Debug("failed to commit offset",
				zap.Int32("partition", partitionID),
				zap.Int64("offset", lastOffset),
				zap.Error(err))
		}

		return parsedMsgs, nil
	}

	// No messages found in any assigned partition
	return nil, fmt.Errorf("no messages available in assigned partitions")
}

// GetAssignedPartitions returns the list of partitions assigned to this consumer
func (c *MessageBrokerConsumer) GetAssignedPartitions() []int32 {
	c.lock.RLock()
	defer c.lock.RUnlock()
	result := make([]int32, len(c.assignments))
	copy(result, c.assignments)
	return result
}

// GetConsumerGroupInfo returns information about the consumer group
func (c *MessageBrokerConsumer) GetConsumerGroupInfo() map[string]interface{} {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return map[string]interface{}{
		"group_id":            c.groupID,
		"member_id":           c.memberID,
		"generation_id":       c.generationID,
		"assigned_partitions": c.assignments,
		"current_offsets":     c.offsets,
	}
}

// Close closes the connection and leaves the consumer group
func (c *MessageBrokerConsumer) Close() error {
	// Stop heartbeat loop
	close(c.stopHeartbeat)

	// Leave consumer group
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.client.LeaveConsumerGroup(ctx, &pb.LeaveConsumerGroupRequest{
		GroupId:  c.groupID,
		MemberId: c.memberID,
	})
	if err != nil {
		c.logger.Warn("failed to leave consumer group", zap.Error(err))
	}

	// Close gRPC connection
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
