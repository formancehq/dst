package main

import (
	"fmt"
	"os"

	"github.com/confluentinc/confluent-kafka-go/kafka"
	// "github.com/formancehq/go-libs/v3/otlp/otlpmetrics"
	// "github.com/formancehq/go-libs/v3/otlp/otlptraces"
	// metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)


func main() {
	err := CheckTraces()
	if err != nil {
		fmt.Printf("%v\n", err)
	}
}

func CheckTraces() error {
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		// "bootstrap.servers": "my-cluster-kafka-bootstrap:9092",
		// "bootstrap.servers": "my-cluster-kafka-bootstrap.kafka.svc.cluster.local:9092",
		"bootstrap.servers": "localhost:9092",
		"group.id":         "foo",
		"auto.offset.reset": "smallest",
	})
	if err != nil {
		return err
	}

	err = consumer.SubscribeTopics([]string{"otlp_logs"}, nil)
	if err != nil {
		return err
	}
	
	run := true

	for run {
		ev := consumer.Poll(1000)
		switch e := ev.(type) {
		case *kafka.Message:
			// application-specific processing
			// fmt.Printf("Received:\n%v %v %v\n", e.Timestamp, e.Key, e.Va)
			processMessage(e)
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

func processMessage(msg *kafka.Message) error {
	var traceData tracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(msg.Value, &traceData); err != nil {
		return fmt.Errorf("failed to unmarshal metrics: %w", err)
	}

	for _, resourceSpans := range traceData.GetResourceSpans() {
		// resource := resourceMetrics.Resource

		for _, spans := range resourceSpans.ScopeSpans {
            for _, span := range spans.Spans {
                fmt.Printf("Span: %s\n", span.Name)
            }
        }
	}
	return nil
}
