package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
)

var (
	PrometheusMetrics = false
	TotalAddWS        = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_add_websocket_session",
			Help:      "Total Count of adding websocket session",
		},
		[]string{"clientkey"})
	TotalRemoveWS = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_remove_websocket_session",
			Help:      "Total Count of removing websocket session",
		},
		[]string{"clientkey"})
	TotalAddConnectionsForWS = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_add_connections",
			Help:      "Total count of adding connection",
		},
		[]string{"clientkey", "proto", "addr"},
	)
	TotalRemoveConnectionsForWS = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_remove_connections",
			Help:      "Total count of removing connection",
		},
		[]string{"clientkey", "proto", "addr"},
	)
	TotalTransmitBytesOnWS = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_transmit_bytes",
			Help:      "Total bytes of transmiting",
		},
		[]string{"clientkey"},
	)
	TotalTransmitErrorBytesOnWS = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_transmit_error_bytes",
			Help:      "Total bytes of transmiting error",
		},
		[]string{"clientkey"},
	)
	TotalReceiveBytesOnWS = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "session_server",
			Name:      "total_receive_bytes",
			Help:      "Total bytes of receiving",
		},
		[]string{"clientkey"},
	)
	GCTargetMetricsByNameForClientKey = []interface{}{
		TotalAddWS, TotalRemoveWS, TotalAddConnectionsForWS, TotalRemoveConnectionsForWS,
		TotalTransmitBytesOnWS, TotalTransmitErrorBytesOnWS, TotalReceiveBytesOnWS,
	}
)

func Register(context *config.ScaledContext) {
	PrometheusMetrics = true
	prometheus.MustRegister(TotalAddWS)
	prometheus.MustRegister(TotalRemoveWS)
	prometheus.MustRegister(TotalAddConnectionsForWS)
	prometheus.MustRegister(TotalRemoveConnectionsForWS)
	prometheus.MustRegister(TotalTransmitBytesOnWS)
	prometheus.MustRegister(TotalTransmitErrorBytesOnWS)
	prometheus.MustRegister(TotalReceiveBytesOnWS)

	go func() {
		for {
			time.Sleep(60 * time.Second)
			MetricGarbageCollector(context)
		}
	}()
}

func MetricGarbageCollector(context *config.ScaledContext) {
	logrus.Infof("MetricGarbageCollector Start")

	clusterLister := context.Management.Clusters("").Controller().Lister()
	nodeLister := context.Management.Nodes("").Controller().Lister()

	// Existing resources
	observedResourceNames := map[string]bool{}
	clusters, err := clusterLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("MetricGarbageCollector failed to list clusters: %s", err)
		return
	}
	logrus.Infof("MetricGarbageCollector saw %d clusters", len(clusters))
	for _, cluster := range clusters {
		if _, ok := observedResourceNames[cluster.Name]; !ok {
			observedResourceNames[cluster.Name] = true
		}
	}

	nodes, err := nodeLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("MetricGarbageCollector failed to list nodes: %s", err)
	}
	logrus.Infof("MetricGarbageCollector saw %d nodes", len(nodes))
	for _, node := range nodes {
		if _, ok := observedResourceNames[node.Name]; !ok {
			observedResourceNames[node.Name] = true
		}
	}
	logrus.Infof("MetricGarbageCollector saw %d resources(node, cluster)", len(observedResourceNames))

	observedClientKey := map[string]bool{}
	for _, collector := range GCTargetMetricsByNameForClientKey {
		metricChan := make(chan prometheus.Metric)
		metricFrame := &dto.Metric{}
		go func() { collector.(prometheus.Collector).Collect(metricChan); close(metricChan) }()
		for metric := range metricChan {
			metric.Write(metricFrame)
			for _, label := range metricFrame.Label {
				if label.GetName() == "clientkey" {
					if _, ok := observedClientKey[label.GetValue()]; !ok {
						observedClientKey[label.GetValue()] = true
					}
				}
			}
		}
	}
	logrus.Infof("MetricGarbageCollector saw %d clientkeys", len(observedClientKey))

	removedCount := 0
	for clientKey := range observedClientKey {
		if _, ok := observedResourceNames[clientKey]; !ok {
			for _, collector := range GCTargetMetricsByNameForClientKey {
				switch v := collector.(type) {
				case *prometheus.CounterVec:
					if v.Delete(prometheus.Labels{"clientkey": clientKey}) {
						removedCount++
						logrus.Infof(
							"MetricGarbageCollector remove %T metrics with clientkey=%s", v, clientKey)
					}
				default:
					logrus.Errorf("MetricGarbageCollector saw unknown Metric definition %T", v)
				}
			}

		}
	}
	logrus.Infof("MetricGarbageCollector removed %d metrics with clientkeys", removedCount)
	logrus.Infof("MetricGarbageCollector Finished")
}
