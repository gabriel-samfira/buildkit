package file

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

func (fb *Backend) readUser(chopt *pb.ChownOpt, mu, mg fileoptypes.Mount) (*copy.User, error) {
	if chopt == nil {
		return nil, nil
	}

	if chopt.User != nil {
		switch u := chopt.User.User.(type) {
		case *pb.UserOpt_ByName:
			if strings.EqualFold(u.ByName.Name, "ContainerAdministrator") || u.ByName.Name == "" {
				return &copy.User{SID: idtools.ContainerAdministratorSidString}, nil
			} else if strings.EqualFold(u.ByName.Name, "ContainerUser") {
				return &copy.User{SID: idtools.ContainerUserSidString}, nil
			}

			// Not one of the built-in users
			// TODO: Lookup well known users/groups
			if mu == nil {
				return nil, errors.Errorf("invalid missing user mount")
			}
			mmu, ok := mu.(*Mount)
			if !ok {
				return nil, errors.Errorf("invalid mount type %T", mu)
			}

			ident, err := getUserIdentFromContainer(context.Background(), fb.Executor, mmu.m, u.ByName.Name)
			if err != nil {
				return nil, err
			}
			return &copy.User{SID: ident.SID}, nil
		default:
			return &copy.User{SID: idtools.ContainerUserSidString}, nil
		}
	}
	return &copy.User{SID: idtools.ContainerAdministratorSidString}, nil
}

// TEMPORARY for testing

func getUserIdentFromContainer(ctx context.Context, exec executor.Executor, rootMount snapshot.Mountable, userName string) (idtools.Identity, error) {
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
			Args: []string{"get-user-info", userName},
			User: "ContainerAdministrator",
			Cwd:  "/",
		},
		Stdin:  nil,
		Stdout: stdout,
		Stderr: stderr,
	}

	if err := exec.Run(ctx, "", newStubMountable(rootMount), nil, procInfo, nil); err != nil {
		return ident, errors.Wrap(err, "executing command")
	}

	data := stdout.bw.Bytes()
	if err := json.Unmarshal(data, &ident); err != nil {
		return ident, errors.Wrap(err, "reading user info")
	}

	return ident, nil
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
