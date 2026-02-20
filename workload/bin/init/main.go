package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/lifecycle"
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
			time.Sleep(time.Second)
			continue
		}
		_ = conn.Close()
		return
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
