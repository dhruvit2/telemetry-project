package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	client "github.com/dhruvit2/messagebroker/pkg/client"
)

func main() {
	// Parse flags
	brokers := flag.String("brokers", "localhost:9092", "Broker addresses (comma-separated)")
	topics := flag.String("topics", "test-topic", "Topics to consume (comma-separated)")
	groupID := flag.String("group", "consumer-group-1", "Consumer group ID")
	maxMessages := flag.Int("max-messages", 100, "Max messages per poll")
	autoCommit := flag.Bool("auto-commit", true, "Enable auto-commit")
	flag.Parse()

	// Create consumer config
	config := &client.ConsumerConfig{
		BrokerAddresses:      []string{*brokers},
		GroupID:              *groupID,
		Topics:               []string{*topics},
		MaxPollRecords:       *maxMessages,
		SessionTimeoutMs:     6000,
		HeartbeatIntervalMs:  3000,
		FetchMinBytes:        1,
		FetchMaxWaitMs:       500,
		RequestTimeoutMs:     40000,
		EnableAutoCommit:     *autoCommit,
		AutoCommitIntervalMs: 5000,
		AutoOffsetReset:      "earliest",
	}

	// Create consumer
	consumer, err := client.NewConsumer(config)
	if err != nil {
		log.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.Close()

	log.Printf("Consumer created. Subscribed to topics: %s in group: %s", *topics, *groupID)

	// Channel for shutdown signal
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	// Consume messages
	ctx := context.Background()
	messageCount := 0

	for {
		select {
		case <-done:
			log.Println("Shutdown signal received")
			goto shutdown
		default:
		}

		// Poll for messages
		messages, err := consumer.Poll(ctx, 1000)
		if err != nil {
			log.Printf("Poll error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		if len(messages) == 0 {
			log.Println("No messages available, waiting...")
			time.Sleep(2 * time.Second)
			continue
		}

		// Process messages
		for _, msg := range messages {
			messageCount++
			log.Printf("[Message %d] Topic: %s, Partition: %d, Offset: %d, Value: %s",
				messageCount,
				msg.Topic,
				msg.Partition,
				msg.Offset,
				string(msg.Value),
			)
		}

		// Commit offsets if auto-commit is disabled
		if !*autoCommit {
			if err := consumer.CommitSync(); err != nil {
				log.Printf("Failed to commit: %v", err)
			}
		}
	}

shutdown:
	log.Printf("Consumed total %d messages", messageCount)

	// Print metrics
	metrics := consumer.GetMetrics()
	log.Printf("Consumer Metrics: Consumed=%d, Commits=%d",
		metrics.TotalMessagesConsumed,
		metrics.TotalCommits,
	)
}
