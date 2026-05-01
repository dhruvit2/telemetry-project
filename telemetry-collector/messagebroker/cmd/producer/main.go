package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	client "github.com/dhruvit2/messagebroker/pkg/client"
)

func main() {
	// Parse flags
	brokers := flag.String("brokers", "localhost:9092", "Broker addresses (comma-separated)")
	topic := flag.String("topic", "test-topic", "Topic to produce to")
	messages := flag.Int("messages", 10, "Number of messages to send")
	flag.Parse()

	// Create producer config
	config := &client.ProducerConfig{
		BrokerAddresses:     []string{*brokers},
		CompressionType:     "snappy",
		Retries:             3,
		RetryBackoffMs:      100,
		RequestTimeoutMs:    5000,
		BatchSize:           100,
		LingerMs:            10,
		MaxInFlightRequests: 5,
		Acks:                "all",
		AutoCreateTopics:    true,
		NumPartitions:       3,
		ReplicationFactor:   1,
	}

	// Create producer
	producer, err := client.NewProducer(config)
	if err != nil {
		log.Fatalf("Failed to create producer: %v", err)
	}
	defer producer.Close()

	log.Printf("Producer created. Sending %d messages to topic '%s'", *messages, *topic)

	// Send messages
	for i := 0; i < *messages; i++ {
		record := &client.ProducerRecord{
			Topic: *topic,
			Key:   []byte(fmt.Sprintf("key-%d", i)),
			Value: []byte(fmt.Sprintf("message-%d: Hello from MessageBroker at %v", i, time.Now())),
		}

		offset, err := producer.SendSync(record)
		if err != nil {
			log.Printf("Failed to send message %d: %v", i, err)
			continue
		}

		log.Printf("Message %d sent successfully at offset %d", i, offset)
		time.Sleep(100 * time.Millisecond)
	}

	// Flush remaining messages
	if err := producer.Flush(); err != nil {
		log.Printf("Failed to flush: %v", err)
	}

	log.Println("All messages sent successfully!")

	// Print metrics
	metrics := producer.GetMetrics()
	log.Printf("Producer Metrics: Sent=%d, Failed=%d, Bytes=%d",
		metrics.TotalMessagesSent,
		metrics.TotalMessagesFailed,
		metrics.TotalBytes,
	)
}
