/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type prowJob struct {
	Type    string `json:"type"`
	Repo    string `json:"repo"`
	PullSHA string `json:"pull_sha"`
	Job     string `json:"job"`
	State   string `json:"state"`
}

type jobResults struct {
	flakeCount  int
	commitCount int
	chance      float64
	jobName     string
}

func (f jobResults) String() string {
	return fmt.Sprintf("%d/%d\t(%.2f%%)\t%s", f.flakeCount, f.commitCount, f.chance, f.jobName)
}

type byChance []jobResults

func (ft byChance) Len() int           { return len(ft) }
func (ft byChance) Swap(i, j int)      { ft[i], ft[j] = ft[j], ft[i] }
func (ft byChance) Less(i, j int) bool { return ft[i].chance > ft[j].chance }

type options struct {
	prowURL string
	repo    string
	path    string
	runOnce bool
}

func flagOptions() options {
	o := options{}
	flag.StringVar(&o.prowURL, "prow-url", "https://deck-ci.svc.ci.openshift.org", "Prow frontend base URL. Required for reading job results")
	flag.StringVar(&o.repo, "repo", "openshift/origin", "Repository to estimate test flakiness for")
	flag.StringVar(&o.path, "config-path", "/etc/config/config.yaml", "Register prometheus metrics from the provided config")
	flag.BoolVar(&o.runOnce, "once", false, "Run once and exit")
	flag.Parse()
	return o
}

func readHTTP(url string) ([]byte, error) {
	var err error
	retryDelay := time.Duration(2) * time.Second
	for retryCount := 0; retryCount < 5; retryCount++ {
		if retryCount > 0 {
			time.Sleep(retryDelay)
			retryDelay *= time.Duration(2)
		}
		resp, err := http.Get(url)
		if resp != nil && resp.StatusCode >= 500 {
			continue
		}
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			continue
		}
		return body, nil
	}
	return nil, fmt.Errorf("ran out of retries reading from %q. The last error was: %v", url, err)
}

func sync(c *Config, o options) {
	body, err := readHTTP(o.prowURL + "/data.js")
	if err != nil {
		log.Fatalf("error reading jobs from prow: %v", err)
	}

	var jobs []prowJob
	if err := json.Unmarshal(body, &jobs); err != nil {
		log.Fatalf("error unmarshaling prowjobs: %v", err)
	}

	jobMap := make(map[string]map[string][]string)
	for _, job := range jobs {
		if job.Type != "presubmit" {
			continue
		}
		if job.Repo != o.repo {
			continue
		}
		if job.State != "success" && job.State != "failure" {
			continue
		}
		if _, ok := jobMap[job.Job]; !ok {
			jobMap[job.Job] = make(map[string][]string)
		}
		if _, ok := jobMap[job.Job][job.PullSHA]; !ok {
			jobMap[job.Job][job.PullSHA] = make([]string, 0)
		}
		jobMap[job.Job][job.PullSHA] = append(jobMap[job.Job][job.PullSHA], job.State)
	}

	jobCommits := make(map[string]int)
	jobFlakes := make(map[string]int)
	for job, commits := range jobMap {
		jobCommits[job] = len(commits)
		jobFlakes[job] = 0
		for _, results := range commits {
			hasFailure, hasSuccess := false, false
			for _, state := range results {
				if state == "success" {
					hasSuccess = true
				}
				if state == "failure" {
					hasFailure = true
				}
				if hasSuccess && hasFailure {
					break
				}
			}
			if hasSuccess && hasFailure {
				jobFlakes[job]++
			}
		}
	}

	totalSuccessChance := 1.0
	var flaky []jobResults
	for job, flakeCount := range jobFlakes {
		if jobCommits[job] < 10 {
			continue
		}
		failChance := float64(flakeCount) / float64(jobCommits[job])
		totalSuccessChance *= (1.0 - failChance)
		flaky = append(flaky, jobResults{
			flakeCount:  flakeCount,
			commitCount: jobCommits[job],
			chance:      100 * failChance,
			jobName:     job,
		})
	}

	fmt.Println("Certain flakes:")
	sort.Sort(byChance(flaky))
	for _, flake := range flaky {
		fmt.Println(flake)
		if c != nil {
			c.Metrics.Set(flake.jobName, flake.chance)
		}
	}
	flakeChance := float64(100 * (1 - totalSuccessChance))
	if c != nil {
		c.Metrics.Set(total, flakeChance)
	}
	fmt.Printf("Chance that a PR hits a flake: %.2f%%\n", flakeChance)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	o := flagOptions()
	var c *Config

	if len(o.path) > 0 {
		var err error
		c, err = Load(o.path)
		if err != nil {
			log.Fatalf("could not load config: %v", err)
		}
		for _, metric := range c.Metrics {
			prometheus.MustRegister(metric.Gauge)
		}
	}

	go func() {
		for {
			start := time.Now()
			sync(c, o)
			if o.runOnce {
				return
			}
			log.Printf("Sync time: %v", time.Since(start))
			time.Sleep(time.Minute)
		}
	}()

	if o.runOnce {
		return
	}

	http.Handle("/prometheus", promhttp.Handler())
	log.Fatal(http.ListenAndServe(":8080", nil))
}
