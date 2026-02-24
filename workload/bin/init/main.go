package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/lifecycle"
	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/formancehq/dst/workload/internal"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"k8s.io/client-go/tools/clientcmd"
)

func waitForPostgres() {
	for {
		conn, err := net.DialTimeout("tcp", "postgres:5432", 500*time.Millisecond)
		if err != nil {
			log.Print("waiting for postgres...")
			time.Sleep(time.Second)
			continue
		}
		_ = conn.Close()
		return
	}
}

func isKafkaReady() bool {
	admin, err := kafka.NewAdminClient(&kafka.ConfigMap{
		"bootstrap.servers": internal.KAFKA_BOOTSTRAP_SERVERS,
	})
	if err != nil {
		return false
	}
	defer admin.Close()
	_, err = admin.GetMetadata(nil, false, 5000)
	return err == nil
}

func waitForKafka() {
	for !isKafkaReady() {
		log.Print("waiting for kafka...")
		time.Sleep(time.Second)
	}
}

func setupStack() {
	config, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		panic(err)
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "formance.com",
		Version:  "v1beta1",
		Resource: "stacks",
	}

	_, err = dyn.Resource(gvr).Create(context.Background(), &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "formance.com/v1beta1",
			"kind":       "Stack",
			"metadata": map[string]any{
				"name": "stack0",
			},
			"spec": map[string]any{
				"versionsFromFile": "v2.0",
			},
		},
	}, v1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			fmt.Printf("Stack already exists\n")
			return
		}
		panic(err)
	}
}

func main() {
	ctx := context.Background()
	client := internal.NewClient()

	waitForPostgres()

	waitForKafka()

	setupStack()

	for {
		time.Sleep(time.Second)

		_, err := client.Ledger.V2.ListLedgers(ctx, operations.V2ListLedgersRequest{})
		if err != nil {
			fmt.Printf("Not ready: %s\n", err)
			continue
		}
		break
	}

	lifecycle.SetupComplete(map[string]any{})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	<-sigs
}
