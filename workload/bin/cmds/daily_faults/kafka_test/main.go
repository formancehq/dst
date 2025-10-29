package main

import (
	"fmt"
	"os"

	"github.com/confluentinc/confluent-kafka-go/kafka"
)


func main() {
	err := CheckLogs()
	if err != nil {
		fmt.Printf("%v\n", err)
	}
}

func CheckLogs() error {
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		// "bootstrap.servers": "my-cluster-kafka-bootstrap:9092",
		"bootstrap.servers": "localhost:41597",
		"group.id":         "foo",
		"auto.offset.reset": "smallest",
	})
	if err != nil {
		return err
	}

	err = consumer.SubscribeTopics([]string{"otel_logs"}, nil)
	if err != nil {
		return err
	}
	
	run := true

	for run {
		ev := consumer.Poll(1000)
		switch e := ev.(type) {
		case *kafka.Message:
			// application-specific processing
			fmt.Printf("%v\n")
		case kafka.Error:
			fmt.Fprintf(os.Stderr, "%% Error: %v\n", e)
			run = false
		default:
			fmt.Printf("Ignored %v\n", e)
		}
	}

	consumer.Close()

	return nil
}
