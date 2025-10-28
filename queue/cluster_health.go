package queue

import (
	"context"
	"log"
	"net/http"
	"time"
)

type ClusterHealthChecker struct {
	queue *Queue
}

func NewClusterHealthChecker(q *Queue) *ClusterHealthChecker {
	return &ClusterHealthChecker{queue: q}
}

func (c *ClusterHealthChecker) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[HealthChecker] Stopping health check loop")
			return
		case <-ticker.C:
			c.checkClusters(ctx)
		}
	}
}

func (c *ClusterHealthChecker) checkClusters(ctx context.Context) {
	clusters, err := c.queue.ListClusters(ctx)
	if err != nil {
		log.Printf("[HealthChecker] Failed to list clusters: %v", err)
		return
	}

	for _, cluster := range clusters {
		resp, err := http.Get(cluster.ClusterURL + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			log.Printf("[HealthChecker] Cluster unhealthy: %s (%v)", cluster.ClusterURL, err)
			continue
		}
		c.queue.UpdateClusterHeartbeat(ctx, cluster.ClusterURL)
	}
}
