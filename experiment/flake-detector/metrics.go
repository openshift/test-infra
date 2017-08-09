package main

import (
	"fmt"
	"io/ioutil"

	"github.com/ghodss/yaml"
	"github.com/prometheus/client_golang/prometheus"
)

type Config struct {
	Metrics Metrics `json:"metrics"`
}

type Metric struct {
	Name  string           `json:"name"`
	Gauge prometheus.Gauge `json:"-"`
}

type Metrics []Metric

func (m Metrics) Set(metricName string, value float64) {
	for i := range m {
		if m[i].Name == metricName {
			m[i].Gauge.Set(value)
			return
		}
	}
}

const total = "all"

// Load loads and parses the config at path.
func Load(path string) (*Config, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %v", path, err)
	}
	config := &Config{}
	if err := yaml.Unmarshal(b, config); err != nil {
		return nil, fmt.Errorf("error unmarshaling %s: %v", path, err)
	}
	if err := parseConfig(config); err != nil {
		return nil, err
	}
	return config, nil
}

func parseConfig(c *Config) error {
	help := "Chance that a PR hits a flake"
	for i := range c.Metrics {
		metric := c.Metrics[i]
		labels := make(prometheus.Labels)
		labels["test_job"] = metric.Name
		c.Metrics[i].Gauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flake_percentage", ConstLabels: labels, Help: help,
		})
	}
	labels := make(prometheus.Labels)
	labels["test_job"] = total
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "flake_percentage", ConstLabels: labels, Help: help,
	})
	c.Metrics = append(c.Metrics, Metric{Name: total, Gauge: gauge})
	return nil
}
