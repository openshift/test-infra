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
	"bytes"
	"flag"
	"io/ioutil"
	"net/http"
	"net/url"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/sirupsen/logrus"

	"k8s.io/test-infra/prow/git"
	"k8s.io/test-infra/prow/github"
)

var (
	port              = flag.Int("port", 8888, "Port to listen on.")
	dryRun            = flag.Bool("dry-run", true, "Dry run for testing. Uses API tokens but does not mutate.")
	githubEndpoint    = flag.String("github-endpoint", "https://api.github.com", "GitHub's API endpoint.")
	githubTokenFile   = flag.String("github-token-file", "/etc/github/oauth", "Path to the file containing the GitHub OAuth secret.")
	webhookSecretFile = flag.String("hmac-secret-file", "/etc/webhook/hmac", "Path to the file containing the GitHub HMAC secret.")
	forksMappingsFile = flag.String("forks-mappings-file", "", "Path to the file containing mappings between vendor and fork repositories.")
)

func main() {
	flag.Parse()
	logrus.SetFormatter(&logrus.JSONFormatter{})
	// Ignore SIGTERM so that we don't drop hooks when the pod is removed.
	// We'll get SIGTERM first and then SIGKILL after our graceful termination
	// deadline.
	signal.Ignore(syscall.SIGTERM)

	webhookSecretRaw, err := ioutil.ReadFile(*webhookSecretFile)
	if err != nil {
		logrus.WithError(err).Fatal("Could not read webhook secret file.")
	}
	webhookSecret := bytes.TrimSpace(webhookSecretRaw)

	oauthSecretRaw, err := ioutil.ReadFile(*githubTokenFile)
	if err != nil {
		logrus.WithError(err).Fatal("Could not read oauth secret file.")
	}
	oauthSecret := string(bytes.TrimSpace(oauthSecretRaw))

	_, err = url.Parse(*githubEndpoint)
	if err != nil {
		logrus.WithError(err).Fatal("Must specify a valid --github-endpoint URL.")
	}

	githubClient := github.NewClient(oauthSecret, *githubEndpoint)
	if *dryRun {
		githubClient = github.NewDryRunClient(oauthSecret, *githubEndpoint)
	}

	gitClient, err := git.NewClient()
	if err != nil {
		logrus.WithError(err).Fatal("Error getting git client.")
	}

	// TODO: Use global option from the prow config.
	logrus.SetLevel(logrus.DebugLevel)

	// The bot name is used to determine to what fork we can push cherry-pick branches.
	botName, err := githubClient.BotName()
	if err != nil {
		logrus.WithError(err).Fatal("Error getting bot name.")
	}

	gitClient.SetCredentials(botName, oauthSecret)

	repos, err := githubClient.GetRepos(botName, true)
	if err != nil {
		logrus.WithError(err).Fatal("Error listing bot repositories.")
	}

	// TODO: Make fork mappings hot-reloadable
	server := NewServer(botName, oauthSecret, webhookSecret, gitClient, githubClient, repos, *forksMappingsFile)

	http.Handle("/", server)
	logrus.Fatal(http.ListenAndServe(":"+strconv.Itoa(*port), nil))
}
