package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/matrix"
)

// taggedTestsBuilt refuses a derived matrix that builds no configuration for test
// files whose build constraint names a tag only test files name: their uses would be
// missing from the run, so each is a setup failure naming the files and the entry of
// analysis.configurations that builds them. A matrix the configuration declares is
// the project's own answer and is not read here.
func taggedTestsBuilt(derived *matrix.Derived) error {
	if derived == nil || len(derived.TaggedTests) == 0 {
		return nil
	}
	host := load.HostConfiguration()
	byTags := make(map[string][]string)
	var order []string
	for _, one := range derived.TaggedTests {
		key := strings.Join(one.Tags, ",")
		if _, held := byTags[key]; !held {
			order = append(order, key)
		}
		byTags[key] = append(byTags[key], one.Path)
	}
	failure := &load.SetupError{}
	for _, key := range order {
		tags := strings.Split(key, ",")
		entry, err := json.Marshal(config.Platform{
			ID: host.ID + "-" + strings.Join(tags, "-"), OS: host.OS, Arch: host.Arch, Tags: tags,
		})
		if err != nil {
			return err
		}
		failure.Failures = append(failure.Failures, load.SetupFailure{Class: load.TestBuildTag, Detail: fmt.Sprintf(
			"%s build under no configuration of the run, because their build constraint names %s; "+
				"list %s in analysis.configurations beside the configurations the run builds",
			strings.Join(byTags[key], ", "), strings.Join(tags, " and "), entry,
		)})
	}
	return failure
}
