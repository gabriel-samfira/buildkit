package containerdexecutor

import (
	"context"
	"os"

	"github.com/containerd/containerd"
	containerdoci "github.com/containerd/containerd/oci"
	"github.com/containerd/continuity/fs"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/windows"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func (w *containerdExecutor) getTaskOpts(ctx context.Context, rootMount snapshot.Mountable, mounts []executor.Mount) (containerd.NewTaskOpts, func(), error) {
	var releasers []func() error
	releaseAll := func() {
		for _, release := range releasers {
			release()
		}
	}
	rootMounts, release, err := rootMount.Mount()
	if err != nil {
		return nil, nil, err
	}
	releasers = append(releasers, release)
	return containerd.WithRootFS(rootMounts), releaseAll, nil
}

func (w *containerdExecutor) getOCISpecOpts(ctx context.Context, meta executor.Meta, rootMount snapshot.Mountable) ([]containerdoci.SpecOpts, error) {
	return []containerdoci.SpecOpts{
		containerdoci.WithUser(meta.User),
	}, nil
}

func (w *containerdExecutor) getOCISpec(ctx context.Context, id string, meta executor.Meta, mounts []executor.Mount, namespace network.Namespace, opts ...containerdoci.SpecOpts) (*specs.Spec, func(), error) {
	var releasers []func()
	releaseAll := func() {
		for _, release := range releasers {
			release()
		}
	}

	processMode := oci.ProcessSandbox // FIXME(AkihiroSuda)
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, "", "", namespace, "", processMode, nil, "", false, w.traceSocket, opts...)
	if err != nil {
		return nil, releaseAll, err
	}
	releasers = append(releasers, cleanup)
	return spec, releaseAll, nil
}

func (w *containerdExecutor) ensureCWD(ctx context.Context, rootMount snapshot.Mountable, meta executor.Meta) error {
	cwdOwner, err := windows.ResolveUsernameToSID(ctx, w, rootMount, meta.User)
	if err != nil {
		return errors.Wrap(err, "getting user SID")
	}
	rootMounts, release, err := rootMount.Mount()
	if err != nil {
		return err
	}
	defer release()

	lm := snapshot.LocalMounterWithMounts(rootMounts)
	rootfsPath, err := lm.Mount()
	if err != nil {
		return err
	}
	defer lm.Unmount()

	newp, err := fs.RootPath(rootfsPath, meta.Cwd)
	if err != nil {
		return errors.Wrapf(err, "working dir %s points to invalid target", newp)
	}

	if _, err := os.Stat(newp); err != nil {
		if err := idtools.MkdirAllAndChown(newp, 0755, cwdOwner); err != nil {
			return errors.Wrapf(err, "failed to create working directory %s", newp)
		}
	}
	return nil
}
