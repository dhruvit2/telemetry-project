package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	broker "github.com/dhruvit2/messagebroker/pkg/broker"
	consumer "github.com/dhruvit2/messagebroker/pkg/consumer"
	coordinator "github.com/dhruvit2/messagebroker/pkg/coordinator"
	pb "github.com/dhruvit2/messagebroker/pkg/pb"
	replication "github.com/dhruvit2/messagebroker/pkg/replication"
	storage "github.com/dhruvit2/messagebroker/pkg/storage"
	"google.golang.org/grpc"
)

// Server implements the gRPC message broker server
type Server struct {
	pb.UnimplementedMessageBrokerServer
	broker         *broker.BrokerImpl
	replicationMgr *replication.ReplicationManager
	storage        *storage.Storage
	groupManager   *consumer.ConsumerGroupManager
	coordinator    coordinator.Coordinator
	grpcServer     *grpc.Server
}

func main() {
	// Parse flags (with environment variable fallbacks)
	id := flag.Int("id", getEnvInt("BROKER_ID", -1), "Broker ID")
	host := flag.String("host", getEnv("BROKER_HOST", "localhost"), "Broker host")
	port := flag.Int("port", getEnvInt("BROKER_PORT", 9092), "Broker port")
	coordinatorURL := flag.String("coordinator", getEnv("COORDINATOR_URL", "localhost:2379"), "Coordinator (etcd) URL")
	coordinatorType := flag.String("coordinator-type", getEnv("COORDINATOR_TYPE", "etcd"), "Coordinator type: 'etcd' or 'mock'")
	etcdUsername := flag.String("etcd-username", getEnv("ETCD_USERNAME", ""), "etcd username for authentication")
	etcdPassword := flag.String("etcd-password", getEnv("ETCD_PASSWORD", ""), "etcd password for authentication")
	dataDir := flag.String("data-dir", getEnv("DATA_DIR", "/tmp/messagebroker"), "Data directory")
	flag.Parse()

	// If broker ID not set, try to extract from pod name (for StatefulSet)
	if *id == -1 {
		log.Println("[STARTUP] BROKER_ID not set, attempting to extract from POD_NAME")
		podName := getEnv("POD_NAME", "")
		log.Printf("[STARTUP] POD_NAME=%s", podName)

		if podName != "" {
			// Pod name format: messagebroker-0, messagebroker-1, etc
			// Extract the ordinal suffix
			parts := strings.Split(podName, "-")
			log.Printf("[STARTUP] Split pod name into %d parts: %v", len(parts), parts)

			if len(parts) > 0 {
				lastPart := parts[len(parts)-1]
				log.Printf("[STARTUP] Attempting to parse ordinal from: %s", lastPart)

				if ordinal, err := strconv.Atoi(lastPart); err == nil {
					*id = ordinal
					log.Printf("[STARTUP] ✓ Successfully extracted broker ID %d from pod name %s", *id, podName)
				} else {
					log.Printf("[STARTUP] ✗ Failed to parse ordinal from '%s': %v", lastPart, err)
				}
			}
		} else {
			log.Println("[STARTUP] ✗ POD_NAME env var not set")
		}

		// Default to 1 if still not set
		if *id == -1 {
			*id = 1
			log.Println("[STARTUP] Using default broker ID: 1")
		}
	} else {
		log.Printf("[STARTUP] Using configured broker ID: %d", *id)
	}

	// Create broker config
	config := &broker.BrokerConfig{
		ID:                int32(*id),
		Host:              *host,
		Port:              *port,
		CoordinatorURL:    *coordinatorURL,
		MaxPartitions:     1000,
		ReplicationFactor: 3,
		MinISR:            2,
		RetentionMs:       604800000,  // 7 days
		SegmentMs:         86400000,   // 1 day
		SegmentBytes:      1073741824, // 1GB
		DataDir:           *dataDir,
		MetadataDir:       *dataDir + "/meta",
	}

	// Create broker instance
	b := broker.NewBroker(config)
	if err := b.Start(); err != nil {
		log.Fatalf("Failed to start broker: %v", err)
	}

	// Create replication manager
	replMgr := replication.NewReplicationManager(*id, config.ReplicationFactor, config.MinISR)

	// Create storage
	storageInstance := storage.NewStorage(*dataDir, config.SegmentBytes, config.RetentionMs)

	// Create coordinator (etcd-based or mock)
	var coord coordinator.Coordinator
	var err error

	if *coordinatorType == "mock" {
		log.Println("Using in-memory Mock Coordinator")
		coord = coordinator.NewMockCoordinator(
			5*time.Second,  // heartbeat interval
			30*time.Second, // session timeout
		)
	} else {
		log.Printf("Using etcd Coordinator at %s\n", *coordinatorURL)
		// Parse coordinator URL - split by comma for multiple endpoints
		endpoints := []string{*coordinatorURL}
		coord, err = coordinator.NewEtcdCoordinator(
			endpoints,
			int32(*id),
			5*time.Second,  // heartbeat interval
			30*time.Second, // session timeout
			*etcdUsername,
			*etcdPassword,
		)
		if err != nil {
			log.Fatalf("Failed to create coordinator: %v", err)
		}
	}

	// Start coordinator
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := coord.Start(ctx); err != nil {
		log.Fatalf("Failed to start coordinator: %v", err)
	}
	log.Println("Coordinator started successfully")

	// Verify coordinator is reachable (critical for topic metadata coordination)
	// Add timeout to prevent startup from hanging
	healthCtx, healthCancel := context.WithTimeout(ctx, 10*time.Second)
	defer healthCancel()

	// Try to get coordinator state - if this fails, log warning but continue
	// (coordinator may recover after startup)
	if err := coord.RegisterBroker(healthCtx, &broker.BrokerMetadata{
		BrokerID: int32(*id),
		Host:     *host,
		Port:     int(*port),
	}); err != nil {
		log.Printf("WARNING: Failed to register broker with coordinator on startup: %v\n", err)
		log.Println("NOTE: Ensure etcd is running and reachable at: " + *coordinatorURL)
		log.Println("NOTE: If etcd is unavailable, consider using -coordinator-type=mock for development")
		// Don't fail - coordinator may come online later
	} else {
		log.Printf("Broker %d verified with coordinator", *id)
	}

	// Create consumer group manager
	groupMgr := consumer.NewConsumerGroupManager(
		coord,
		60*time.Second, // rebalance timeout
		30*time.Second, // session timeout
	)
	if err := groupMgr.Start(ctx); err != nil {
		log.Fatalf("Failed to start consumer group manager: %v", err)
	}

	// Create gRPC server
	server := &Server{
		broker:         b,
		replicationMgr: replMgr,
		storage:        storageInstance,
		groupManager:   groupMgr,
		coordinator:    coord,
	}

	// Start gRPC server
	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *host, *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	server.grpcServer = grpc.NewServer()
	pb.RegisterMessageBrokerServer(server.grpcServer, server)

	log.Printf("Broker %d listening on %s:%d", *id, *host, *port)

	// Run server in goroutine
	go func() {
		if err := server.grpcServer.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	// Wait for signal to shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down broker...")
	server.grpcServer.GracefulStop()
	b.Stop()

	// Deregister broker from coordinator
	coord.DeregisterBroker(ctx, int32(*id))
	coord.Stop(ctx)
	log.Println("Broker stopped")
}

// CreateTopic creates a new topic with partitions distributed across brokers
func (s *Server) CreateTopic(ctx context.Context, req *pb.CreateTopicRequest) (*pb.CreateTopicResponse, error) {
	log.Printf("CreateTopic called: topic=%s, partitions=%d, replicationFactor=%d",
		req.Topic, req.NumPartitions, req.ReplicationFactor)

	// Get list of all brokers for partition distribution and replication
	brokers, err := s.coordinator.ListBrokers(ctx)
	if err != nil {
		log.Printf("Warning: Could not list brokers from coordinator: %v. Creating topic on local broker only.", err)
		// Fallback: create with only this broker
		brokers = []*broker.BrokerMetadata{{
			BrokerID: int32(0),
			Host:     "localhost",
			Port:     9092,
		}}
	}

	if len(brokers) == 0 {
		return nil, fmt.Errorf("no brokers available in cluster")
	}

	log.Printf("Distributing %d partitions across %d brokers with RF=%d",
		req.NumPartitions, len(brokers), req.ReplicationFactor)

	// Create partition assignments: round-robin distribution
	partitionAssignments := make(map[int32][]int32) // partition ID -> broker IDs
	for i := int32(0); i < req.NumPartitions; i++ {
		leader := brokers[i%int32(len(brokers))].BrokerID
		replicas := []int32{leader}

		// Add replicas on other brokers (if RF > 1)
		for j := int32(1); j < req.ReplicationFactor && j < int32(len(brokers)); j++ {
			replica := brokers[(i+j)%int32(len(brokers))].BrokerID
			if replica != leader {
				replicas = append(replicas, replica)
			}
		}

		partitionAssignments[i] = replicas
		log.Printf("Partition %d assigned to brokers: %v (leader: %d)", i, replicas, leader)
	}

	// Create topic locally
	topic := &broker.Topic{
		Name:              req.Topic,
		NumPartitions:     req.NumPartitions,
		ReplicationFactor: req.ReplicationFactor,
		CreatedAt:         time.Now(),
	}

	if err := s.broker.CreateTopic(ctx, topic); err != nil {
		log.Printf("Error creating topic in broker: %v", err)
		return nil, err
	}
	log.Printf("Topic %s created in broker successfully", req.Topic)

	// Register topic metadata with coordinator with partition assignments
	if err := s.coordinator.CreateTopicMetadata(ctx, req.Topic, req.NumPartitions, req.ReplicationFactor); err != nil {
		log.Printf("ERROR: Failed to register topic metadata with coordinator: %v", err)
		// Rollback local topic creation
		s.broker.DeleteTopic(ctx, req.Topic)
		return nil, fmt.Errorf("failed to register topic with coordinator (may be unavailable): %w", err)
	}

	// Store partition-to-broker assignments in coordinator
	for partID, replicas := range partitionAssignments {
		key := fmt.Sprintf("/messagebroker/partitions/%s/%d", req.Topic, partID)
		value, _ := json.Marshal(map[string]interface{}{
			"partition": partID,
			"topic":     req.Topic,
			"replicas":  replicas,
			"leader":    replicas[0],
		})
		if err := s.coordinator.Put(ctx, key, string(value)); err != nil {
			log.Printf("Warning: Could not store partition assignment in coordinator: %v", err)
		}
	}

	log.Printf("Topic %s registered with coordinator and partition assignments stored", req.Topic)

	return &pb.CreateTopicResponse{
		Topic:      req.Topic,
		Partitions: req.NumPartitions,
		Success:    true,
	}, nil
}

// syncTopicLocally tries to fetch topic metadata from the coordinator and create it locally if missing
func (s *Server) syncTopicLocally(ctx context.Context, topicName string) {
	if _, err := s.broker.GetTopic(ctx, topicName); err != nil {
		// Topic not found locally, fetch from coordinator
		coordMeta, err := s.coordinator.GetTopicMetadata(ctx, topicName)
		if err == nil {
			localTopic := &broker.Topic{
				Name:              coordMeta.Name,
				NumPartitions:     coordMeta.NumPartitions,
				ReplicationFactor: coordMeta.ReplicationFactor,
				CreatedAt:         time.Unix(coordMeta.CreatedAt, 0),
			}
			// Ignore error, it might have been created concurrently
			_ = s.broker.CreateTopic(ctx, localTopic)
			log.Printf("Synced topic %s from coordinator to local broker", topicName)
		}
	}
}

// ProduceMessage sends a message to a topic
func (s *Server) ProduceMessage(ctx context.Context, req *pb.ProduceRequest) (*pb.ProduceResponse, error) {
	// Ensure topic exists locally
	s.syncTopicLocally(ctx, req.Topic)

	// Determine partition
	partition := req.Partition
	if partition < 0 {
		// Select partition based on key
		partition = 0 // Simplified
	}

	message := &broker.Message{
		ID:        generateMessageID(),
		Topic:     req.Topic,
		Partition: partition,
		Key:       req.Key,
		Value:     req.Value,
		Timestamp: time.Now(),
	}

	offset, err := s.broker.ProduceMessage(ctx, message)
	if err != nil {
		return nil, err
	}

	// Store message
	s.storage.WriteMessage(req.Topic, partition, offset, map[string]interface{}{
		"key":   req.Key,
		"value": req.Value,
	})

	return &pb.ProduceResponse{
		Topic:     req.Topic,
		Partition: partition,
		Offset:    offset,
	}, nil
}

// ConsumeMessages fetches messages from a topic partition
func (s *Server) ConsumeMessages(ctx context.Context, req *pb.ConsumeRequest) (*pb.ConsumeResponse, error) {
	// Ensure topic exists locally
	s.syncTopicLocally(ctx, req.Topic)

	messages, err := s.broker.ConsumeMessages(ctx, req.Topic, req.Partition, req.Offset, req.MaxMessages)
	if err != nil {
		return nil, err
	}

	resp := &pb.ConsumeResponse{
		Topic:     req.Topic,
		Partition: req.Partition,
		Messages:  make([]*pb.Message, 0),
	}

	for _, msg := range messages {
		resp.Messages = append(resp.Messages, &pb.Message{
			Key:    msg.Key,
			Value:  msg.Value,
			Offset: msg.Offset,
		})
	}

	return resp, nil
}

// GetTopicMetadata returns metadata for a topic
func (s *Server) GetTopicMetadata(ctx context.Context, req *pb.GetTopicMetadataRequest) (*pb.TopicMetadata, error) {
	// Ensure topic exists locally
	s.syncTopicLocally(ctx, req.Topic)

	topic, err := s.broker.GetTopic(ctx, req.Topic)
	if err != nil {
		return nil, err
	}

	metadata := &pb.TopicMetadata{
		Topic:      topic.Name,
		Partitions: make([]*pb.PartitionMetadata, 0),
	}

	for i := int32(0); i < topic.NumPartitions; i++ {
		partition, _ := s.broker.GetPartition(ctx, topic.Name, i)
		if partition != nil {
			metadata.Partitions = append(metadata.Partitions, &pb.PartitionMetadata{
				Id:       partition.ID,
				Leader:   partition.Leader,
				Replicas: partition.Replicas,
				Isr:      partition.ISR,
			})
		}
	}

	return metadata, nil
}

// BrokerMetadata returns this broker's metadata
func (s *Server) BrokerMetadata(ctx context.Context, req *pb.BrokerMetadataRequest) (*pb.BrokerMetadataResponse, error) {
	brokerMeta, err := s.broker.GetBrokerMetadata(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.BrokerMetadataResponse{
		Id:        brokerMeta.BrokerID,
		Host:      brokerMeta.Host,
		Port:      int32(brokerMeta.Port),
		IsHealthy: s.broker.IsHealthy(),
	}, nil
}

// generateMessageID generates a unique message ID
func generateMessageID() string {
	return fmt.Sprintf("msg-%d", time.Now().UnixNano())
}

// JoinConsumerGroup handles consumer group join requests
func (s *Server) JoinConsumerGroup(ctx context.Context, req *pb.JoinConsumerGroupRequest) (*pb.JoinConsumerGroupResponse, error) {
	genID, members, err := s.groupManager.JoinConsumerGroup(
		ctx,
		req.GroupId,
		req.MemberId,
		req.MemberId, // clientID same as memberID for simplicity
		"localhost",
		req.Topics,
		30*time.Second,
		60*time.Second,
	)
	if err != nil {
		return nil, err
	}

	return &pb.JoinConsumerGroupResponse{
		GroupId:       req.GroupId,
		MemberId:      req.MemberId,
		GenerationId:  genID,
		Members:       members,
		NeedRebalance: true,
	}, nil
}

// LeaveConsumerGroup handles consumer group leave requests
func (s *Server) LeaveConsumerGroup(ctx context.Context, req *pb.LeaveConsumerGroupRequest) (*pb.LeaveConsumerGroupResponse, error) {
	err := s.groupManager.LeaveConsumerGroup(ctx, req.GroupId, req.MemberId)
	if err != nil {
		return nil, err
	}

	return &pb.LeaveConsumerGroupResponse{
		GroupId: req.GroupId,
		Success: true,
	}, nil
}

// FetchAssignments returns partition assignments for a consumer group member
func (s *Server) FetchAssignments(ctx context.Context, req *pb.FetchAssignmentsRequest) (*pb.FetchAssignmentsResponse, error) {
	assignments, genID, err := s.groupManager.FetchAssignments(ctx, req.GroupId, req.MemberId)
	if err != nil {
		return nil, err
	}

	// Convert partition IDs to PartitionAssignment objects
	partitionAssignments := make([]*pb.PartitionAssignment, len(assignments))
	for i, partID := range assignments {
		partitionAssignments[i] = &pb.PartitionAssignment{
			Partition: partID,
		}
	}

	return &pb.FetchAssignmentsResponse{
		GroupId:      req.GroupId,
		GenerationId: genID,
		Topic:        "", // Topic is already known from JoinConsumerGroup
		Assignments:  partitionAssignments,
	}, nil
}

// CommitOffset handles offset commit requests
func (s *Server) CommitOffset(ctx context.Context, req *pb.CommitOffsetRequest) (*pb.CommitOffsetResponse, error) {
	err := s.groupManager.CommitOffset(ctx, req.GroupId, req.Topic, req.Partition, req.Offset)
	if err != nil {
		return nil, err
	}

	return &pb.CommitOffsetResponse{
		GroupId:   req.GroupId,
		Topic:     req.Topic,
		Partition: req.Partition,
		Success:   true,
	}, nil
}

// FetchOffset returns the last committed offset for a consumer group partition
func (s *Server) FetchOffset(ctx context.Context, req *pb.FetchOffsetRequest) (*pb.FetchOffsetResponse, error) {
	offset, err := s.groupManager.FetchOffset(ctx, req.GroupId, req.Topic, req.Partition)
	if err != nil {
		// If no offset found, return 0
		offset = 0
	}

	return &pb.FetchOffsetResponse{
		GroupId:   req.GroupId,
		Topic:     req.Topic,
		Partition: req.Partition,
		Offset:    offset,
	}, nil
}

// ListConsumerGroups lists all consumer groups
func (s *Server) ListConsumerGroups(ctx context.Context, req *pb.ListConsumerGroupsRequest) (*pb.ListConsumerGroupsResponse, error) {
	groupIDs := s.groupManager.ListConsumerGroups(ctx)

	summaries := make([]*pb.ConsumerGroupSummary, len(groupIDs))
	for i, groupID := range groupIDs {
		summaries[i] = &pb.ConsumerGroupSummary{
			GroupId: groupID,
			State:   "Stable",
			Members: 1,
			Topics:  []string{"telemetry-data"}, // Placeholder
		}
	}

	return &pb.ListConsumerGroupsResponse{
		Groups: summaries,
	}, nil
}

// DescribeConsumerGroup describes a consumer group
func (s *Server) DescribeConsumerGroup(ctx context.Context, req *pb.DescribeConsumerGroupRequest) (*pb.DescribeConsumerGroupResponse, error) {
	info, err := s.groupManager.DescribeConsumerGroup(ctx, req.GroupId)
	if err != nil {
		return nil, err
	}

	// info is a map[string]interface{} returned by DescribeConsumerGroup
	var state string
	var membersData []map[string]interface{}

	// Handle state - it might be ConsumerGroupState type or string
	if v, ok := info["state"]; ok {
		// ConsumerGroupState is a type alias for string, so convert it safely
		state = fmt.Sprintf("%v", v)
	}
	if v, ok := info["members"]; ok {
		if members, ok := v.([]map[string]interface{}); ok {
			membersData = members
		}
	}

	members := make([]*pb.MemberDescription, len(membersData))
	for i, memberMap := range membersData {
		partitions := []int32{}
		if v, ok := memberMap["partitions"]; ok {
			if p, ok := v.([]int32); ok {
				partitions = p
			}
		}

		memberID := ""
		if v, ok := memberMap["member_id"]; ok {
			memberID = v.(string)
		}
		clientID := ""
		if v, ok := memberMap["client_id"]; ok {
			clientID = v.(string)
		}
		host := ""
		if v, ok := memberMap["host"]; ok {
			host = v.(string)
		}

		members[i] = &pb.MemberDescription{
			MemberId:           memberID,
			ClientId:           clientID,
			Host:               host,
			AssignedPartitions: partitions,
		}
	}

	return &pb.DescribeConsumerGroupResponse{
		GroupId:      req.GroupId,
		State:        state,
		ProtocolType: "consumer",
		Protocol:     "RoundRobin",
		Members:      members,
	}, nil
}

// getEnv returns environment variable or default value
func getEnv(key, defaultVal string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultVal
}

// getEnvInt returns environment variable as int or default value
func getEnvInt(key string, defaultVal int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultVal
}
