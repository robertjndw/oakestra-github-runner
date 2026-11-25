package controller

import (
	"context"
	"errors"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// ScaleSetClient is the part of *scaleset.Client the controller uses, plus OpenSession so the
// message session can be faked as well.
type ScaleSetClient interface {
	GetRunnerGroupByName(ctx context.Context, name string) (*scaleset.RunnerGroup, error)
	// GetRunnerScaleSet returns nil, nil when no scale set has that name in the group.
	GetRunnerScaleSet(ctx context.Context, runnerGroupID int, name string) (*scaleset.RunnerScaleSet, error)
	CreateRunnerScaleSet(ctx context.Context, s *scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error)
	DeleteRunnerScaleSet(ctx context.Context, scaleSetID int) error
	GenerateJitRunnerConfig(ctx context.Context, setting *scaleset.RunnerScaleSetJitRunnerSetting, scaleSetID int) (*scaleset.RunnerScaleSetJitRunnerConfig, error)
	// GetRunnerByName returns nil, nil when no runner has that name.
	GetRunnerByName(ctx context.Context, name string) (*scaleset.RunnerReference, error)
	// RemoveRunner fails with scaleset.JobStillRunningError while the runner runs a job.
	RemoveRunner(ctx context.Context, runnerID int64) error
	OpenSession(ctx context.Context, scaleSetID int, owner string) (Session, error)
}

// Session is a scale set message session. Close deletes it on GitHub so a restarted controller
// can open a new one right away instead of waiting for the old one to expire.
type Session interface {
	listener.Client
	Close(ctx context.Context) error
}

// ClientFactory builds a scale set client for a ready connection.
type ClientFactory interface {
	NewClient(ctx context.Context, conn core.Connection) (ScaleSetClient, error)
}

// ClientFactoryFunc adapts a function to ClientFactory.
type ClientFactoryFunc func(ctx context.Context, conn core.Connection) (ScaleSetClient, error)

func (f ClientFactoryFunc) NewClient(ctx context.Context, conn core.Connection) (ScaleSetClient, error) {
	return f(ctx, conn)
}

// NewScaleSetClientFactory wraps a builder of real scaleset clients, typically
// (*github.ScaleSetClientFactory).NewClient.
func NewScaleSetClientFactory(build func(core.Connection) (*scaleset.Client, error)) ClientFactory {
	return ClientFactoryFunc(func(_ context.Context, conn core.Connection) (ScaleSetClient, error) {
		if build == nil {
			return nil, errors.New("no scale set client builder configured")
		}
		c, err := build(conn)
		if err != nil {
			return nil, err
		}
		return WrapScaleSetClient(c), nil
	})
}

// WrapScaleSetClient adapts a *scaleset.Client to ScaleSetClient.
func WrapScaleSetClient(c *scaleset.Client) ScaleSetClient {
	return realClient{c}
}

type realClient struct {
	*scaleset.Client
}

func (c realClient) OpenSession(ctx context.Context, scaleSetID int, owner string) (Session, error) {
	return c.MessageSessionClient(ctx, scaleSetID, owner)
}
