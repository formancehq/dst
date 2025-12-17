package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/formancehq/dst/workload/internal"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"k8s.io/client-go/tools/clientcmd"
)

const FAULTS_PAUSING_SAFETY_MARGIN int64 = 5

// The upgrade process goes as follows:
// - Pause the fault injector for 30s
// - Wait 2 seconds for it to actually clear all faults
// - Flag faults as paused to enable availability assertions
// - Upgrade the CRD
// - If successfull, set `/upgraded` to "true"
func main() {
	if !canUpgrade() {
		fmt.Printf("already up to date, nothing to do")
		return
	}

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
		flagUpgradeDone()
	}
}

// only returns true if we were able to verify that no upgrade has taken place already
func canUpgrade() bool {
	etcdClient, err := internal.NewEtcdClient()
	if err != nil {
		return false
	}
	defer etcdClient.Close()

	lastPause, err := etcdClient.Get(context.Background(), "/upgraded")
	if err != nil {
		return false
	}

	return len(lastPause.Kvs) == 0
}

func flagUpgradeDone() {
	etcdClient, err := internal.NewEtcdClient()
	if err != nil {
		return
	}
	defer etcdClient.Close()
	etcdClient.Put(context.Background(), "/upgraded", "true")
}

func flagFaultsPaused() {
	etcdClient, err := internal.NewEtcdClient()
	if err != nil {
		return
	}
	defer etcdClient.Close()

	pause_time := fmt.Sprintf("%v", time.Now().Unix())
	fmt.Printf("/last_pause set to %s\n", pause_time)
	etcdClient.Put(context.Background(), "/last_pause", pause_time)
}
