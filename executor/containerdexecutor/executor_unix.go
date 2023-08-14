//go:build !windows
// +build !windows

package containerdexecutor

import (
	"context"
	"os"
	"runtime"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/mount"
	containerdoci "github.com/containerd/containerd/oci"
	"github.com/containerd/continuity/fs"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/snapshot"
	rootlessspecconv "github.com/moby/buildkit/util/rootless/specconv"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func getUserSpec(user, rootfsPath string) (specs.User, error) {
	var err error
	var uid, gid uint32
	var sgids []uint32
	if rootfsPath != "" {
		uid, gid, sgids, err = oci.GetUser(rootfsPath, user)
	} else {
		uid, gid, err = oci.ParseUIDGID(user)
	}
	if err != nil {
		return specs.User{}, errors.WithStack(err)
	}
	return specs.User{
		UID:            uid,
		GID:            gid,
		AdditionalGids: sgids,
	}, nil
}

func (w *containerdExecutor) prepareMounts(ctx context.Context, rootMount executor.Mount, mounts []executor.Mount, meta executor.Meta, details *jobDetails) (func(), error) {
	var releasers []func() error
	releaseAll := func() {
		for _, release := range releasers {
			release()
		}
	}

	mountable, err := rootMount.Src.Mount(ctx, false)
	if err != nil {
		return releaseAll, err
	}

	rootMounts, release, err := mountable.Mount()
	if err != nil {
		return releaseAll, err
	}
	details.rootMounts = rootMounts

	releasers = append(releasers, release)
	lm := snapshot.LocalMounterWithMounts(rootMounts)
	rootfsPath, err := lm.Mount()
	if err != nil {
		return releaseAll, err
	}
	details.rootfsPath = rootfsPath

	cleanStubs := func() error {
		executor.MountStubsCleaner(ctx, rootfsPath, mounts, meta.RemoveMountStubsRecursive)()
		return nil
	}
	releasers = append(releasers, lm.Unmount)
	releasers = append(releasers, cleanStubs)

	return releaseAll, nil
}

func (w *containerdExecutor) getTaskOpts(ctx context.Context, details *jobDetails) (containerd.NewTaskOpts, error) {
	rootfs := containerd.WithRootFS([]mount.Mount{{
		Source:  details.rootfsPath,
		Type:    "bind",
		Options: []string{"rbind"},
	}})
	if runtime.GOOS == "freebsd" {
		rootfs = containerd.WithRootFS([]mount.Mount{{
			Source:  details.rootfsPath,
			Type:    "nullfs",
			Options: []string{},
		}})
	}
	return rootfs, nil
}

func (w *containerdExecutor) ensureCWD(ctx context.Context, details *jobDetails, meta executor.Meta) error {
	newp, err := fs.RootPath(details.rootfsPath, meta.Cwd)
	if err != nil {
		return errors.Wrapf(err, "working dir %s points to invalid target", newp)
	}

	uid, gid, _, err := oci.GetUser(details.rootfsPath, meta.User)
	if err != nil {
		return err
	}

	identity := idtools.Identity{
		UID: int(uid),
		GID: int(gid),
	}

	if _, err := os.Stat(newp); err != nil {
		if err := idtools.MkdirAllAndChown(newp, 0755, identity); err != nil {
			return errors.Wrapf(err, "failed to create working directory %s", newp)
		}
	}
	return nil
}

func (w *containerdExecutor) getOCISpec(ctx context.Context, id string, mounts []executor.Mount, meta executor.Meta, details *jobDetails) (*specs.Spec, func(), error) {
	var releasers []func()
	releaseAll := func() {
		for _, release := range releasers {
			release()
		}
	}
	resolvConf, err := oci.GetResolvConf(ctx, w.root, nil, w.dnsConfig)
	if err != nil {
		return nil, releaseAll, err
	}

	hostsFile, clean, err := oci.GetHostsFile(ctx, w.root, meta.ExtraHosts, nil, meta.Hostname)
	if err != nil {
		return nil, releaseAll, err
	}
	if clean != nil {
		defer clean()
	}

	opts := []containerdoci.SpecOpts{containerdoci.WithUser(meta.User), containerdoci.WithAdditionalGIDs(meta.User)}
	if meta.ReadonlyRootFS {
		opts = append(opts, containerdoci.WithRootFSReadonly())
	}

	processMode := oci.ProcessSandbox // FIXME(AkihiroSuda)
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, resolvConf, hostsFile, details.namespace, w.cgroupParent, processMode, nil, w.apparmorProfile, w.selinux, w.traceSocket, opts...)
	if err != nil {
		return nil, releaseAll, err
	}
	releasers = append(releasers, cleanup)
	spec.Process.Terminal = meta.Tty
	if w.rootless {
		if err := rootlessspecconv.ToRootless(spec); err != nil {
			return nil, releaseAll, err
		}
	}
	return spec, releaseAll, nil
}
