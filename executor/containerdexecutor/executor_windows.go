package containerdexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/containerd/containerd"
	containerdoci "github.com/containerd/containerd/oci"
	"github.com/containerd/continuity/fs"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/network"
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

func (w *containerdExecutor) getUserIdentFromContainer(ctx context.Context, rootMount snapshot.Mountable, meta executor.Meta) (idtools.Identity, error) {
	var ident idtools.Identity
	rootMounts, release, err := rootMount.Mount()
	if err != nil {
		return ident, errors.Wrap(err, "getting root mounts")
	}
	defer release()

	if len(rootMounts) > 1 {
		return ident, fmt.Errorf("unexpected number of root mounts: %d", len(rootMounts))
	}

	stdout := &bytesReadWriteCloser{
		bw: &bytes.Buffer{},
	}
	stderr := &bytesReadWriteCloser{
		bw: &bytes.Buffer{},
	}

	procInfo := executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"get-user-info", meta.User},
			User: "ContainerAdministrator",
			Cwd:  "/",
		},
		Stdin:  nil,
		Stdout: stdout,
		Stderr: stderr,
	}

	if err := w.Run(ctx, "", newStubMountable(rootMount), nil, procInfo, nil); err != nil {
		return ident, errors.Wrap(err, "executing command")
	}

	data := stdout.bw.Bytes()
	if err := json.Unmarshal(data, &ident); err != nil {
		return ident, errors.Wrap(err, "reading user info")
	}

	return ident, nil
}

func (w *containerdExecutor) getUserIdent(ctx context.Context, rootMount snapshot.Mountable, meta executor.Meta) (idtools.Identity, error) {
	var ident idtools.Identity
	if strings.EqualFold(meta.User, "ContainerAdministrator") || meta.User == "" {
		ident.SID = idtools.ContainerAdministratorSidString
	} else if strings.EqualFold(meta.User, "ContainerUser") {
		ident.SID = idtools.ContainerUserSidString
	} else {
		return w.getUserIdentFromContainer(ctx, rootMount, meta)
	}
	return ident, nil
}

func (w *containerdExecutor) ensureCWD(ctx context.Context, rootMount snapshot.Mountable, meta executor.Meta) error {
	cwdOwner, err := w.getUserIdent(ctx, rootMount, meta)
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

type bytesReadWriteCloser struct {
	bw *bytes.Buffer
}

func (b *bytesReadWriteCloser) Write(p []byte) (int, error) {
	if b.bw == nil {
		return 0, fmt.Errorf("invalid bytes buffer")
	}
	return b.bw.Write(p)
}

func (b *bytesReadWriteCloser) Close() error {
	if b.bw == nil {
		return nil
	}
	b.bw.Reset()
	return nil
}

type mountable struct {
	m snapshot.Mountable
}

func (m *mountable) Mount(ctx context.Context, readonly bool) (snapshot.Mountable, error) {
	return m.m, nil
}

func newStubMountable(m snapshot.Mountable) executor.Mount {
	return executor.Mount{Src: &mountable{m: m}}
}
