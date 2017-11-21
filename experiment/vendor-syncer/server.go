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
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"

	"k8s.io/test-infra/prow/git"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/hook"
)

const pluginName = "vendor-sync"

// VendorForkMappings maps the vendor repositories to their forks.
type VendorForkMappings struct {
	// The key in this mapping is the short name of the repository
	// used in the commit message, eg. "UPSTREAM: containers/image: 00000: description"
	Repositories map[string]ForkMapping `yaml:"repositories"`
}

// ForkMapping represents a full fork repository name and the source repository
// vendor path where the patches were applied.
// The VendorPath is required so we know how to apply the patch file (how many
// slashes are going to be stripped).
type ForkMapping struct {
	// ForkRepository represents a real location of the fork repository for this vendor
	ForkRepository string `yaml:"forkRepository"`
	// ForkDefaultBranch is the default branch we open a PRs against
	// TODO: We should try to figure this out from the glide.yaml
	ForkDefaultBranch string `yaml:"forkDefaultBranch"`
	// VendorPath is the location where this fork is vendored inside source
	// repository.
	VendorPath string `yaml:"vendorPath"`
}

type githubClient interface {
	GetPullRequest(org, repo string, number int) (*github.PullRequest, error)
	CreateComment(org, repo string, number int, comment string) error
	IsMember(org, user string) (bool, error)
	CreatePullRequest(org, repo, title, body, head, base string, canModify bool) (int, error)
	ListIssueComments(org, repo string, number int) ([]github.IssueComment, error)
	CreateFork(org, repo string) error
}

// Server implements http.Handler. It validates incoming GitHub webhooks and
// then dispatches them to the appropriate plugins.
type Server struct {
	hmacSecret []byte
	botName    string

	gc  *git.Client
	ghc githubClient
	log *logrus.Entry

	bare     *http.Client
	patchURL string

	repoLock sync.Mutex
	repos    []github.Repo

	forksMappingsConfig VendorForkMappings
}

func NewServer(name, creds string, hmac []byte, gc *git.Client, ghc *github.Client, repos []github.Repo, forkMappingsFile string) *Server {
	if len(forkMappingsFile) == 0 {
		logrus.Error("Fork mappings config file must be specified")
	}
	forkMappings, err := readForkMappingsConfig(forkMappingsFile)
	if err != nil {
		logrus.WithError(err).Errorf("Error parsing fork mappings config file %q", forkMappingsFile)
		return nil
	}
	return &Server{
		hmacSecret: hmac,
		botName:    name,

		gc:  gc,
		ghc: ghc,
		log: logrus.StandardLogger().WithField("client", "vendor-sync"),

		bare:     &http.Client{},
		patchURL: "https://patch-diff.githubusercontent.com",

		repos:               repos,
		forksMappingsConfig: *forkMappings,
	}
}

func readForkMappingsConfig(name string) (*VendorForkMappings, error) {
	yamlFile, err := ioutil.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var result VendorForkMappings
	if err := yaml.Unmarshal(yamlFile, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ServeHTTP validates an incoming webhook and puts it into the event channel.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	eventType, eventGUID, payload, ok := hook.ValidateWebhook(w, r, s.hmacSecret)
	if !ok {
		return
	}
	fmt.Fprint(w, "Event received. Have a nice day.")

	if err := s.handleEvent(eventType, eventGUID, payload); err != nil {
		logrus.WithError(err).Error("Error parsing event.")
	}
}

func (s *Server) handleEvent(eventType, eventGUID string, payload []byte) error {
	switch eventType {
	case "push":
		var push github.PushEvent
		if err := json.Unmarshal(payload, &push); err != nil {
			return err
		}
		go func() {
			if err := s.handlePush(push, s.forksMappingsConfig); err != nil {
				s.log.WithError(err).Info("Vendor sync failed.")
			}
		}()
	default:
		return fmt.Errorf("received an event of type %q but didn't ask for it", eventType)
	}
	return nil
}

// PickCommit is variant of commit where we changing one or more files but
// not bumping the dependency level. Usually we pick a commit that has
// upstream alternative. These commits start with "UPSTREAM: " prefix.
type PickCommit struct {
	// ID represents a SHA256 of the commit that is going to be picked
	ID string

	// SourceRepository represents a repository from where the commit will be
	// extracted.
	SourceRepository string

	// Mapping contains information about target (fork) repository and the
	// mapping to the filesystem path in vendor/ directory.
	Mapping *ForkMapping

	// Author represents the author of the commit.
	Author string

	// For PR body
	Message string
}

// PullRequestDetails contains additional information about pull requests.
type PullRequestDetails struct {
	Author string
}

// ForkRepository returns the repository this pick was made for.
func (c *PickCommit) ForkRepository() string {
	if c.Mapping == nil {
		return ""
	}
	return c.Mapping.ForkRepository
}

// WithBranch returns fork repository and the default branch for the given fork
// repository.
// TODO: We should be able to determine the branch from the commit?
func (c *PickCommit) WithBranch() string {
	return c.ForkRepository() + "#" + c.Mapping.ForkDefaultBranch
}

func processCommit(c github.Commit, forks VendorForkMappings) *PickCommit {
	var result *PickCommit
	if strings.HasPrefix(strings.TrimSpace(c.Message), "UPSTREAM: ") {
		message := strings.TrimSpace(c.Message)
		if pos := strings.Index(message[10:], ": "); pos != -1 {
			mappings := getForkMapping(message[10:10+pos], forks)
			result = &PickCommit{
				ID:      c.ID,
				Mapping: mappings,
				Message: strings.TrimSpace(c.Message),
			}
		}
	}
	// TODO: Do we need to handle <drop> commits?
	return result
}

func getForkMapping(shortRepository string, forks VendorForkMappings) *ForkMapping {
	// try to find an exact match first
	mapping, ok := forks.Repositories[shortRepository]
	if ok {
		return &mapping
	}
	// no match, guess the last two segments is the shortName
	parts := strings.Split(shortRepository, "/")
	if len(parts) < 2 {
		return nil
	}
	mapping, ok = forks.Repositories[strings.Join(parts[len(parts)-2:], "/")]
	if ok {
		return &mapping
	}
	return nil
}

func (s *Server) handlePush(p github.PushEvent, forks VendorForkMappings) error {
	// Fill the information about commits and group them based on the targed
	// repository.
	commits := map[string][]*PickCommit{}
	for _, commit := range p.Commits {
		// check if the commit is changing vendor/ (iow. has UPSTREAM: prefix)
		// extract information about this commit and get fork repo mappings.
		c := processCommit(commit, forks)
		if c == nil {
			// commit is not UPSTREAM commit
			continue
		}
		// TODO: Need to handle the case when there is no fork-mapping defined for
		// the commit. Probably need to inform the commit author about doing the
		// pick manually after the fork repo is created.
		if len(c.ForkRepository()) == 0 {
			s.log.Infof("No fork repository mapping defined for: %s", c.Message)
			continue
		}
		// fill more details from the hook payload
		c.SourceRepository = p.Repo.HTMLURL
		c.Author = p.Sender.Login
		commits[c.WithBranch()] = append(commits[c.WithBranch()], c)
	}

	// Open PR's with grouped commits
	for forkRepo, groupedCommits := range commits {
		// ID is needed for uniqe working branch name
		shortID := string(groupedCommits[0].ID[0:7])
		go s.handlePicksForRepo(shortID, forkRepo, groupedCommits)
	}
	return nil
}

func (s *Server) handlePicksForRepo(ref string, forkRepoBranch string, picks []*PickCommit) {
	parts := strings.Split(forkRepoBranch, "#")
	forkRepo := parts[0]
	forkBranch := parts[1]

	s.log.Debugf("Cloning %s", forkRepo)
	targetRepo, err := s.gc.Clone(forkRepo)
	if err != nil {
		s.log.WithError(err).Error("Error cloning fork repository.")
		return
	}
	defer targetRepo.Clean()

	if err := targetRepo.Config("user.name", "vendorpicker"); err != nil {
		s.log.WithError(err).Errorf("Error setting user.name")
		return
	}
	if err := targetRepo.Config("user.email", "vendorpicker@localhost"); err != nil {
		s.log.WithError(err).Errorf("Error setting user.email")
		return
	}

	s.log.Debugf("Checkouting target branch %s", forkBranch)
	if err := targetRepo.Checkout(forkBranch); err != nil {
		s.log.WithError(err).Errorf("Error checking out target branch %s.", forkBranch)
		return
	}
	workingBranch := "auto-pick-" + forkBranch + "-" + ref
	s.log.Debugf("Creating working branch %s", workingBranch)
	if err := targetRepo.CheckoutNewBranch(workingBranch); err != nil {
		s.log.WithError(err).Errorf("Error checking out branch %s.", workingBranch)
		return
	}

	messages := []string{}
	authors := []string{}
	sourceRepo := ""
	for _, commit := range picks {
		s.log.Debugf("Fetching commit %#v", commit)
		patchFile, err := s.getPatchFile(commit.SourceRepository, commit.ID)
		if err != nil {
			s.log.WithError(err).Errorf("Error getting patch for commit %q.", commit.ID)
			return
		}

		// apply the patch in the destination repository
		// git am -3 -p<slashes> /tmp/patch
		s.log.Debugf("Applying %s in %q", commit.Message, forkRepo)
		if err := s.applyPatch(commit, targetRepo.Dir, patchFile); err != nil {
			// TODO: Probably need to notify the authors that their commits have not
			// applied cleanely.
			s.log.WithError(err).Errorf("Error applying patch for %q.", commit.ID)
			return
		}
		messages = append(messages, commit.Message)
		authors = append(authors, commit.Author)
		sourceRepo = commit.SourceRepository
	}

	// TODO: Need to figure out good PR title here. If there is more than one
	//       commit, this might get pretty long, so shorted in that case.
	title := "Automatic pick for " + ref
	if len(messages) == 1 {
		title = "Automatic pick of " + messages[0]
	}
	body := fmt.Sprintf("This is automatic pick from %s.\n", sourceRepo)
	for _, author := range authors {
		body += "/cc @" + author
	}
	head := fmt.Sprintf("%s:%s", s.botName, workingBranch)
	repoParts := strings.Split(forkRepo, "/")

	s.log.Debugf("Pushing branch %s to %s", workingBranch, forkRepoBranch)
	if err := targetRepo.Push(repoParts[1], workingBranch); err != nil {
		s.log.WithError(err).Errorf("Error pushing working branch")
		return
	}

	// TODO: We should probably just push to fork instead of creating a PR there?
	_, err = s.ghc.CreatePullRequest(repoParts[0], repoParts[1], title, body, head, forkBranch, true)
	if err != nil {
		s.log.WithError(err).Errorf("Error opening pull request")
		return
	}
}

func (s *Server) getPatchFile(sourceRepo, commitID string) (string, error) {
	url := fmt.Sprintf("%s/commit/%s.patch", sourceRepo, commitID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// TODO: Add retries
	resp, err := s.bare.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("cannot get Github patch for commit %s: %s", commitID, resp.Status)
	}
	outputDir, err := ioutil.TempDir("", "vendor-sync")
	if err != nil {
		return "", err
	}
	outputFile := path.Join(outputDir, fmt.Sprintf("%s.patch", commitID))
	out, err := os.Create(outputFile)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}
	return outputFile, nil
}

func (s *Server) applyPatch(c *PickCommit, baseRepoDir string, patchFile string) error {
	stripSlashesCount := fmt.Sprintf("-p%d", strings.Count(c.Mapping.VendorPath, "/")+2)
	cmd := exec.Command("git", "am", "-3", "--ignore-whitespace", stripSlashesCount, patchFile)
	cmd.Dir = baseRepoDir
	output, err := cmd.CombinedOutput()
	s.log.Infof("git am: %s", output)
	return err
}
