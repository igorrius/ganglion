package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &out); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "ganglion "+version {
		t.Fatalf("run --version printed %q, want %q", got, "ganglion "+version)
	}
}

func TestRunRejectsUnknownRole(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"--roles", "core,brain"}, &out)
	if err == nil || !strings.Contains(err.Error(), "brain") {
		t.Fatalf("run --roles core,brain = %v, want error naming the bad role", err)
	}
}

func TestRunStartsAllRolesByDefault(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the node stops as soon as it has started

	if err := run(ctx, nil, &out); err != nil {
		t.Fatalf("run with defaults: %v", err)
	}
	if !strings.Contains(out.String(), "roles=core,edge,worker") {
		t.Fatalf("default run did not start all roles:\n%s", out.String())
	}
}
