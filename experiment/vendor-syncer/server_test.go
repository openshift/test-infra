package main

import (
	"reflect"
	"testing"

	"k8s.io/test-infra/prow/github"
)

func TestGetRepositoryFromMessage(t *testing.T) {
	testMapping := ForkMapping{
		ForkRepository:    "github.com/forkorg/containers-image",
		ForkDefaultBranch: "release-1.0",
		VendorPath:        "vendor/github.com/containers/image",
	}
	testMappingConfig := VendorForkMappings{
		Repositories: map[string]ForkMapping{
			"containers/image": testMapping,
		},
	}

	table := []struct {
		commit github.Commit
		expect *PickCommit
	}{
		{
			commit: github.Commit{
				ID:      "c4eefeff07841366bbca7c86e71929a3c5bf2d04",
				Message: "UPSTREAM: containers/image: 2384: Some commit message",
			},
			expect: &PickCommit{
				ID:      "c4eefeff07841366bbca7c86e71929a3c5bf2d04",
				Message: "UPSTREAM: containers/image: 2384: Some commit message",
				Mapping: &testMapping,
			},
		},
	}

	for _, tc := range table {
		got := processCommit(tc.commit, testMappingConfig)
		if !reflect.DeepEqual(got, tc.expect) {
			t.Errorf("[%s] expected:\n\t %#+v\n got:\n\t %#+v", tc.commit, tc.expect, got)
		}
		if got == nil {
			continue
		}
	}
}

func TestReadForkMappingsFile(t *testing.T) {
	mappings, err := readForkMappingsConfig("sample_config.yaml")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(mappings.Repositories) == 0 {
		t.Error("expected non-empty repositories")
	}
	if mappings.Repositories["containers/image"].ForkRepository != "github.com/foo/containers-image" {
		t.Errorf("invalid forkRepository for containers/image: %q", mappings.Repositories["containers/image"].ForkRepository)
	}
}

func TestGetForkMapping(t *testing.T) {
	table := []struct {
		shortRepo       string
		config          VendorForkMappings
		expectedMapping *ForkMapping
	}{
		{
			shortRepo: "containers/image",
			config: VendorForkMappings{Repositories: map[string]ForkMapping{
				"containers/image": {},
			}},
			expectedMapping: &ForkMapping{},
		},
		{
			shortRepo: "unknown/repo",
			config: VendorForkMappings{Repositories: map[string]ForkMapping{
				"containers/image": {},
			}},
			expectedMapping: nil,
		},
		{
			shortRepo: "github.com/containers/image",
			config: VendorForkMappings{Repositories: map[string]ForkMapping{
				"containers/image": {},
			}},
			expectedMapping: &ForkMapping{},
		},
		{
			shortRepo: "",
			config: VendorForkMappings{Repositories: map[string]ForkMapping{
				"k8s.io/kubernetes": {},
			}},
			expectedMapping: nil,
		},
	}

	for _, tc := range table {
		got := getForkMapping(tc.shortRepo, tc.config)
		if !reflect.DeepEqual(got, tc.expectedMapping) {
			t.Errorf("[%s] expected %#v mapping, got %#v", tc.shortRepo, tc.expectedMapping, *got)
		}
	}
}
