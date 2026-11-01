package extismrun_test

import (
	"context"
	"testing"

	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/extismrun"
	"github.com/igorrius/ganglion/runner/runnertest"
)

func TestConformance(t *testing.T) {
	probe, err := runnertest.BuildProbe("extism")
	if err != nil {
		t.Fatal(err)
	}
	runnertest.Run(t, func(ctx context.Context, wasm []byte, m runner.Manifest, deps runner.Deps) (runnertest.Instance, error) {
		return extismrun.Load(ctx, wasm, m, deps)
	}, probe)
}
