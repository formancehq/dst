package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/formancehq/dst/workload/internal"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	// get latest version
	latest_tag, err := os.ReadFile("/ledger_latest_tag")
	if err != nil {
		log.Fatal(err)
	}

	// build dynamic client
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
		Resource: "ledgers",
	}

	// fetch the previous Ledger resource
	res, err := dyn.Resource(gvr).Get(context.Background(), "stack0-ledger", metav1.GetOptions{})
	if err != nil {
		panic(err)
	}

	// set the version to the latest tag
	unstructured.SetNestedField(res.Object, string(latest_tag), "spec", "version")

	_, err = dyn.Resource(gvr).Update(context.Background(), res, metav1.UpdateOptions{})

	assert.Sometimes(err == nil, "stack0-ledger should successfully be updated", internal.Details{
		"error": err,
	})

	if err == nil {
		path, ok := os.LookupEnv("ANTITHESIS_STOP_FAULTS")
		if !ok {
			log.Fatal("failed to find fault pausing executable")
		}
		cmd := exec.Command(path, fmt.Sprintf("%v", internal.FAULT_PAUSING_DURATION))
		fmt.Printf("stopping faults: %v\n", cmd)
		err := cmd.Run()
		if err != nil {
			return
		}
		flagFautsPaused()
	}
}

func flagFautsPaused() {
	etcdClient, err := internal.NewEtcdClient()
	if err != nil {
		return
	}
	defer etcdClient.Close()

	time := fmt.Sprintf("%v", time.Now().Unix())
	fmt.Printf("/last_paused set to %s\n", time)
	etcdClient.Put(context.Background(), "/last_pause", time)
}
