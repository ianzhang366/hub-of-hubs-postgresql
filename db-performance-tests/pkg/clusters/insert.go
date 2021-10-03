package clusters

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	sanitize "github.com/kennygrant/sanitize"
	cluster "github.com/open-cluster-management/api/cluster/v1"
)

const (
	columnSize               = 4
	clustersPerLeafHub       = 1000
	DefaultLeafHubsNumber    = 1000
	DefaultStartLeafHubIndex = 0
)

func RunInsert(ctx context.Context, dbConnectionPool *pgxpool.Pool, leafHubsNumber, startLeafHubIndex int) error {
	entry := time.Now()

	prototypeManagedCluster := &cluster.ManagedCluster{}
	err := unmarshalFile("cluster.json", &prototypeManagedCluster)
	if err != nil {
		return fmt.Errorf("Error in unmarshalling managed cluster: %w", err)
	}

	defer func() {
		now := time.Now()
		elapsed := now.Sub(entry)
		log.Printf("clusters RunInsert %d leaf hubs: elapsed %v\n", leafHubsNumber, elapsed)
	}()

	var wg sync.WaitGroup

	c := make(chan int, leafHubsNumber)

	for i := 0; i < leafHubsNumber; i++ {
		wg.Add(1)

		go insertRows(ctx, dbConnectionPool, c, &wg, prototypeManagedCluster)
	}

	for i := 0; i < leafHubsNumber; i++ {
		c <- startLeafHubIndex + i
	}
	close(c)

	wg.Wait()

	return nil
}

func insertRows(ctx context.Context, dbConnectionPool *pgxpool.Pool, c chan int, wg *sync.WaitGroup, prototypeManagedCluster *cluster.ManagedCluster) {
	defer wg.Done()

	for leafHubIndex := range c {
		err := insertRowsForLeafHub(ctx, dbConnectionPool, leafHubIndex, prototypeManagedCluster)
		if err != nil {
			log.Printf("failed to insert rows: %v\n", err)
			return
		}
	}
}

func flatten(rows [][]interface{}) []interface{} {
	resultRows := make([]interface{}, 0, len(rows)*columnSize)

	for _, row := range rows {
		resultRows = append(resultRows, row...)
	}

	return resultRows
}

func insertRowsForLeafHub(ctx context.Context, dbConnectionPool *pgxpool.Pool, leafHubIndex int, prototypeManagedCluster *cluster.ManagedCluster) error {
	rows := generateRowsForLeafHub(leafHubIndex, prototypeManagedCluster)

	sb := generateInsertByMultipleValues(len(rows))
	sb.WriteString(" ON CONFLICT DO NOTHING")

	_, err := dbConnectionPool.Exec(ctx, sb.String(), flatten(rows)...)
	if err != nil {
		return fmt.Errorf("insert into database failed: %w", err)
	}

	return nil
}

func generateRowsForLeafHub(leafHubIndex int, prototypeManagedCluster *cluster.ManagedCluster) [][]interface{} {
	rows := make([][]interface{}, 0, clustersPerLeafHub)

	beginClusterIndex, endClusterIndex := leafHubIndex*clustersPerLeafHub, (leafHubIndex+1)*clustersPerLeafHub
	for clusterIndex := beginClusterIndex; clusterIndex < endClusterIndex; clusterIndex++ {
		rows = append(rows, generateRow(leafHubIndex, clusterIndex, prototypeManagedCluster))
	}

	return rows
}

func generateInsertByMultipleValues(insertSize int) *strings.Builder {
	var sb strings.Builder

	sb.WriteString("INSERT INTO status.managed_clusters values")

	for i := 0; i < insertSize; i++ {
		sb.WriteString("(")

		for j := 0; j < columnSize; j++ {
			sb.WriteString("$")
			sb.WriteString(strconv.Itoa(i*columnSize + j + 1))

			if j < columnSize-1 {
				sb.WriteString(", ")
			}
		}

		sb.WriteString(")")

		if i < insertSize-1 {
			sb.WriteString(", ")
		}
	}

	return &sb
}

// from https://github.com/open-cluster-management/insights-client/blob/main/pkg/monitor/clustermonitor_test.go
func unmarshalFile(filepath string, resourceType interface{}) error {
	// open given filepath string
	rawBytes, err := ioutil.ReadFile("testdata/" + sanitize.Name(filepath))
	if err != nil {
		return fmt.Errorf("Unable to read test data: %w", err)
	}

	// unmarshal file into given resource type
	err = json.Unmarshal(rawBytes, resourceType)
	if err != nil {
		return fmt.Errorf("Unable to unmarshal json to type %T %w", resourceType, err)
	}

	return nil
}

func generateRow(leafHubIndex, clusterIndex int, prototypeManagedCluster *cluster.ManagedCluster) []interface{} {
	leafHubName := fmt.Sprintf("hub%d", leafHubIndex)
	clusterName := fmt.Sprintf("cluster%d", clusterIndex)

	managedCluster := &cluster.ManagedCluster{}
	prototypeManagedCluster.DeepCopyInto(managedCluster)
	managedCluster.ObjectMeta.SetName(clusterName)
	managedCluster.ObjectMeta.SetNamespace(clusterName)
	labels := managedCluster.ObjectMeta.GetLabels()
	labels["name"] = clusterName
	labels["leafHub"] = leafHubName
	managedCluster.ObjectMeta.SetLabels(labels)
	managedCluster.ObjectMeta.SetSelfLink("")
	for i, claim := range managedCluster.Status.ClusterClaims {
		if claim.Name == "id.k8s.io" {
			managedCluster.Status.ClusterClaims[i] = cluster.ManagedClusterClaim{Name: "id.k8s.io", Value: clusterName}
		}
	}
	return []interface{}{clusterName, leafHubName, managedCluster, "none"}
}