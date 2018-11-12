package prbuild

import (
	"time"
)

// Metric represents a metric to send
type Metric struct {
	Name  string
	Path  string
	Value interface{}
}

// TODO: drain on shutdown
var metricsChannel = make(chan *Metric, 100)

type metricsSender interface {
	Send(metric *Metric) error
}

func sendMetrics(s metricsSender, stop chan struct{}) {
outer:
	for {
		select {
		case <-stop:
			Info("Task shutdown requested, stopping metrics gathering")
			break outer
		default:

			for metric := range metricsChannel {
				if err := s.Send(metric); err != nil {
					// A problem sending the metric, put it back for later
					// TODO: is this appropriate - perhaps we just drop the metric?
					// If we put this back often enough we will fill the channel and block for ever...
					metricsChannel <- metric
				}
			}

			time.Sleep(1)
		}
	}
}
