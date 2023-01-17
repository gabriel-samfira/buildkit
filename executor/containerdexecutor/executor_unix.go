//go:build !windows
// +build !windows

package containerdexecutor

import (
	"context"
	"os"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/mount"
	containerdoci "github.com/containerd/containerd/oci"
	"github.com/containerd/continuity/fs"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/network"
	rootlessspecconv "github.com/moby/buildkit/util/rootless/specconv"
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
	lm := snapshot.LocalMounterWithMounts(rootMounts)
	rootfsPath, err := lm.Mount()
	if err != nil {
		return nil, releaseAll, err
	}

	cleanStubs := func() error {
		executor.MountStubsCleaner(rootfsPath, mounts)
		return nil
	}
	releasers = append(releasers, lm.Unmount)
	releasers = append(releasers, cleanStubs)
	return containerd.WithRootFS([]mount.Mount{{
		Source:  rootfsPath,
		Type:    "bind",
		Options: []string{"rbind"},
	}}), releaseAll, nil
}

func (w *containerdExecutor) getOCISpecOpts(ctx context.Context, meta executor.Meta, rootMount snapshot.Mountable) ([]containerdoci.SpecOpts, error) {
	rootMounts, release, err := rootMount.Mount()
	if err != nil {
		return nil, err
	}
	if release != nil {
		defer release()
	}

	lm := snapshot.LocalMounterWithMounts(rootMounts)
	rootfsPath, err := lm.Mount()
	if err != nil {
		return nil, err
	}
	defer lm.Unmount()
	uid, gid, sgids, err := oci.GetUser(rootfsPath, meta.User)
	if err != nil {
		return nil, err
	}
	opts := []containerdoci.SpecOpts{oci.WithUIDGID(uid, gid, sgids)}
	if meta.ReadonlyRootFS {
		opts = append(opts, containerdoci.WithRootFSReadonly())
	}
	return opts, nil
}

func (w *containerdExecutor) getOCISpec(ctx context.Context, id string, meta executor.Meta, mounts []executor.Mount, namespace network.Namespace, opts ...containerdoci.SpecOpts) (*specs.Spec, func(), error) {
	var releasers []func()
	releaseAll := func() {
		for _, release := range releasers {
			release()
		}
	}
	resolvConf, err := oci.GetResolvConf(ctx, w.root, nil, w.dnsConfig)
	if err != nil {
		return nil, releaseAll, errors.Wrap(err, "getting resolvconf")
	}

	hostsFile, clean, err := oci.GetHostsFile(ctx, w.root, meta.ExtraHosts, nil, meta.Hostname)
	if err != nil {
		return nil, releaseAll, err
	}

	releasers = append(releasers, clean)

	processMode := oci.ProcessSandbox // FIXME(AkihiroSuda)
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, resolvConf, hostsFile, namespace, w.cgroupParent, processMode, nil, w.apparmorProfile, w.selinux, w.traceSocket, opts...)
	if err != nil {
		return nil, releaseAll, err
	}
	releasers = append(releasers, cleanup)
	if w.rootless {
		if err := rootlessspecconv.ToRootless(spec); err != nil {
			return nil, releaseAll, err
		}
	}
	return spec, releaseAll, nil
}

func (w *containerdExecutor) ensureCWD(ctx context.Context, rootMount snapshot.Mountable, meta executor.Meta) error {
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

	uid, gid, _, err := oci.GetUser(rootfsPath, meta.User)
	if err != nil {
		return err
	}

	cwdOwner := idtools.Identity{
		UID: int(uid),
		GID: int(gid),
	}

	if _, err := os.Stat(newp); err != nil {
		if err := idtools.MkdirAllAndChown(newp, 0755, cwdOwner); err != nil {
			return errors.Wrapf(err, "failed to create working directory %s", newp)
		}
	}
	return nil
}
