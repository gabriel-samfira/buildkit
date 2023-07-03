package resources

import (
	"time"

	"github.com/moby/buildkit/executor/resources/types"
)

func newSysSampler() (*Sampler[*types.SysSample], error) {
	return NewSampler(2*time.Second, 20, func(tm time.Time) (*types.SysSample, error) {
		return &types.SysSample{}, nil
	}), nil
}
