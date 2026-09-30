package main

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestMain turns off git's automatic housekeeping for every git command the
// tests run. After a commit git may start a detached gc or maintenance
// process that keeps writing to .git after the test has returned, and the
// temporary directory cleanup then fails with "directory not empty".
func TestMain(m *testing.M) {
	settings := [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}}
	count, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	for _, setting := range settings {
		os.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", count), setting[0])
		os.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", count), setting[1])
		count++
	}
	os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(count))
	os.Exit(m.Run())
}

func TestGitHousekeepingIsOffInTests(t *testing.T) {
	root := createGitRepository(t, "{}")
	for _, key := range []string{"gc.auto", "maintenance.auto"} {
		value, err := gitOutput(t.Context(), root, "config", "--get", key)
		if err != nil {
			t.Fatalf("git config %s: %v", key, err)
		}
		if got := string(value); got != "0\n" && got != "false\n" {
			t.Fatalf("git config %s = %q", key, got)
		}
	}
}
