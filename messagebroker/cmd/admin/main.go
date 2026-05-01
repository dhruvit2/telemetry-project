package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	pb "github.com/dhruvit2/messagebroker/pkg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	brokerAddr := flag.String("broker", "localhost:9092", "Broker address (e.g., localhost:9092)")
	command := flag.String("cmd", "help", "Command: topics, group, lag, offsets, help")
	topic := flag.String("topic", "", "Topic name")
	group := flag.String("group", "", "Consumer group name")
	flag.Parse()

	conn, err := grpc.NewClient(
		*brokerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(10*1024*1024)),
	)
	if err != nil {
		log.Fatalf("Failed to connect to broker: %v", err)
	}
	defer conn.Close()

	client := pb.NewMessageBrokerClient(conn)

	switch *command {
	case "topics":
		listTopics(client)

	case "partitions":
		if *topic == "" {
			fmt.Println("Error: -topic required for 'partitions' command")
			os.Exit(1)
		}
		describePartitions(client, *topic)

	case "group":
		if *group == "" {
			fmt.Println("Error: -group required for 'group' command")
			os.Exit(1)
		}
		describeGroup(client, *group)

	case "lag":
		if *group == "" || *topic == "" {
			fmt.Println("Error: -group and -topic required for 'lag' command")
			os.Exit(1)
		}
		showConsumerLag(client, *group, *topic)

	case "offsets":
		if *group == "" || *topic == "" {
			fmt.Println("Error: -group and -topic required for 'offsets' command")
			os.Exit(1)
		}
		showOffsets(client, *group, *topic)

	case "groups":
		listGroups(client)

	case "help", "":
		printHelp()

	default:
		fmt.Printf("Unknown command: %s\n", *command)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`
MessageBroker Admin CLI - Diagnostic Tool

Usage: admin -broker <addr> -cmd <command> [options]

Commands:
  topics                    List all topics
  partitions               Describe topic partitions (-topic <name>)
  groups                   List all consumer groups
  group                    Describe consumer group (-group <name>)
  lag                      Show consumer lag (-group <name> -topic <topic>)
  offsets                  Show committed offsets (-group <name> -topic <topic>)

Examples:
  admin -broker localhost:9092 -cmd topics
  admin -broker localhost:9092 -cmd partitions -topic telemetry-data
  admin -broker localhost:9092 -cmd groups
  admin -broker localhost:9092 -cmd group -group telemetry-collectors
  admin -broker localhost:9092 -cmd lag -group telemetry-collectors -topic telemetry-data
  admin -broker localhost:9092 -cmd offsets -group telemetry-collectors -topic telemetry-data
`)
}

func listTopics(client pb.MessageBrokerClient) {
	fmt.Println("Topics:")
	fmt.Println("--------")
	// Note: ListTopics is not implemented yet, so we'll skip this
	fmt.Println("(Use 'partitions' command with a known topic name)")
}

func describePartitions(client pb.MessageBrokerClient, topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.GetTopicMetadata(ctx, &pb.GetTopicMetadataRequest{
		Topic: topic,
	})
	if err != nil {
		fmt.Printf("Error getting topic metadata: %v\n", err)
		return
	}

	fmt.Printf("\nTopic: %s\n", resp.Topic)
	fmt.Printf("Partitions: %d\n\n", len(resp.Partitions))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Partition\tLeader\tReplicas\tISR")
	fmt.Fprintln(w, "---------\t------\t--------\t---")

	for _, p := range resp.Partitions {
		replicas := fmt.Sprintf("%v", p.Replicas)
		isr := fmt.Sprintf("%v", p.Isr)
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\n", p.Id, p.Leader, replicas, isr)
	}
	w.Flush()
}

func listGroups(client pb.MessageBrokerClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.ListConsumerGroups(ctx, &pb.ListConsumerGroupsRequest{})
	if err != nil {
		fmt.Printf("Error listing consumer groups: %v\n", err)
		return
	}

	fmt.Printf("\nConsumer Groups (%d total):\n", len(resp.Groups))
	fmt.Println("----------------------------")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Group ID\tState\tMembers\tTopics")
	fmt.Fprintln(w, "--------\t-----\t-------\t------")

	for _, group := range resp.Groups {
		topics := fmt.Sprintf("%v", group.Topics)
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", group.GroupId, group.State, group.Members, topics)
	}
	w.Flush()
}

func describeGroup(client pb.MessageBrokerClient, group string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.DescribeConsumerGroup(ctx, &pb.DescribeConsumerGroupRequest{
		GroupId: group,
	})
	if err != nil {
		fmt.Printf("Error describing consumer group: %v\n", err)
		return
	}

	fmt.Printf("\nConsumer Group: %s\n", resp.GroupId)
	fmt.Printf("State: %s\n", resp.State)
	fmt.Printf("Protocol Type: %s\n", resp.ProtocolType)
	fmt.Printf("Protocol: %s\n", resp.Protocol)
	fmt.Printf("Members: %d\n\n", len(resp.Members))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Member ID\tClient ID\tHost\tAssigned Partitions")
	fmt.Fprintln(w, "---------\t---------\t----\t-------------------")

	for _, member := range resp.Members {
		partitions := fmt.Sprintf("%v", member.AssignedPartitions)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", member.MemberId, member.ClientId, member.Host, partitions)
	}
	w.Flush()
}

func showOffsets(client pb.MessageBrokerClient, group string, topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get topic metadata to know partitions
	metaResp, err := client.GetTopicMetadata(ctx, &pb.GetTopicMetadataRequest{
		Topic: topic,
	})
	if err != nil {
		fmt.Printf("Error getting topic metadata: %v\n", err)
		return
	}

	fmt.Printf("\nCommitted Offsets - Group: %s, Topic: %s\n", group, topic)
	fmt.Println("----------------------------------------------")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Partition\tCommitted Offset")
	fmt.Fprintln(w, "---------\t----------------")

	for _, partition := range metaResp.Partitions {
		offsetResp, err := client.FetchOffset(ctx, &pb.FetchOffsetRequest{
			GroupId:   group,
			Topic:     topic,
			Partition: partition.Id,
		})
		if err != nil {
			fmt.Fprintf(w, "%d\tError: %v\n", partition.Id, err)
			continue
		}
		fmt.Fprintf(w, "%d\t%d\n", partition.Id, offsetResp.Offset)
	}
	w.Flush()
}

func showConsumerLag(client pb.MessageBrokerClient, group string, topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get topic metadata to know partitions
	metaResp, err := client.GetTopicMetadata(ctx, &pb.GetTopicMetadataRequest{
		Topic: topic,
	})
	if err != nil {
		fmt.Printf("Error getting topic metadata: %v\n", err)
		return
	}

	fmt.Printf("\nConsumer Lag - Group: %s, Topic: %s\n", group, topic)
	fmt.Println("------------------------------------")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Partition\tLatest Offset\tCommitted Offset\tLag")
	fmt.Fprintln(w, "---------\t--------------\t----------------\t---")

	totalLag := int64(0)

	// Sort partitions for consistent output
	partitions := metaResp.Partitions
	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].Id < partitions[j].Id
	})

	for _, partition := range partitions {
		// Get committed offset
		offsetResp, err := client.FetchOffset(ctx, &pb.FetchOffsetRequest{
			GroupId:   group,
			Topic:     topic,
			Partition: partition.Id,
		})
		if err != nil {
			fmt.Fprintf(w, "%d\tError\t\t\t\t\n", partition.Id)
			continue
		}

		// Get latest offset by consuming from end
		// Try to consume from the end to get latest offset
		latestOffset := offsetResp.Offset // Default to committed offset

		// Try to get latest by consuming 1 message from a high offset
		consumeResp, err := client.ConsumeMessages(ctx, &pb.ConsumeRequest{
			Topic:       topic,
			Partition:   partition.Id,
			Offset:      latestOffset,
			MaxMessages: 1000,
		})
		if err == nil && len(consumeResp.Messages) > 0 {
			// Get the last message offset
			lastMsg := consumeResp.Messages[len(consumeResp.Messages)-1]
			latestOffset = lastMsg.Offset + 1
		}

		lag := latestOffset - offsetResp.Offset
		totalLag += lag

		fmt.Fprintf(w, "%d\t%d\t\t%d\t\t%d\n",
			partition.Id,
			latestOffset,
			offsetResp.Offset,
			lag)
	}

	w.Flush()
	fmt.Printf("\nTotal Lag: %d messages\n", totalLag)

	if totalLag == 0 {
		fmt.Println("✓ Consumer is caught up (no lag)")
	} else if totalLag < 100 {
		fmt.Println("⚠ Low lag - consumer is catching up")
	} else {
		fmt.Println("✗ High lag - consumer is behind")
	}
}
