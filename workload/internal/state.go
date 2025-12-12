package internal

import (
	"context"
	"strconv"
	"time"

	etcd "go.etcd.io/etcd/client/v3"
)

const FAULT_PAUSING_DURATION int64 = 60

func NewEtcdClient() (*etcd.Client, error) {
	return etcd.New(etcd.Config{
		Endpoints: []string{
			"http://etcd-0.etcd.default.svc.cluster.local:2379",
			"http://etcd-1.etcd.default.svc.cluster.local:2379",
			"http://etcd-2.etcd.default.svc.cluster.local:2379",
		},
		DialTimeout: 5 * time.Second,
	})
}

const AVAILABILITY_ASSERTIONS_SAFETY_MARGIN int64 = 5

func FaultsActive(ctx context.Context) bool {

	etcdClient, err := NewEtcdClient()
	if err != nil {
		return true
	}
	defer etcdClient.Close()

	lastPause, err := etcdClient.Get(ctx, "/last_pause")
	if err != nil {
		return true
	}

	if len(lastPause.Kvs) == 0 {
		return true
	}
	lastPauseUnix, err := strconv.ParseInt(string(lastPause.Kvs[0].Value), 10, 64)
	if err != nil {
		return true
	}
	sinceLastPause := time.Now().Unix() - lastPauseUnix
	return sinceLastPause < AVAILABILITY_ASSERTIONS_SAFETY_MARGIN || sinceLastPause > FAULT_PAUSING_DURATION-AVAILABILITY_ASSERTIONS_SAFETY_MARGIN
}
