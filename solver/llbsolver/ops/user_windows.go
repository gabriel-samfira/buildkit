package ops

import (
	"context"

	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/solver/llbsolver/file"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/windows"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

func getReadUserFn(worker worker.Worker) func(chopt *pb.ChownOpt, mu, mg *file.Mount) (*copy.User, error) {
	return func(chopt *pb.ChownOpt, mu, mg *file.Mount) (*copy.User, error) {
		return readUser(chopt, mu, mg, worker)
	}
}

func readUser(chopt *pb.ChownOpt, mu, mg *file.Mount, worker worker.Worker) (*copy.User, error) {
	if chopt == nil {
		return nil, nil
	}

	if chopt.User != nil {
		switch u := chopt.User.User.(type) {
		case *pb.UserOpt_ByName:
			if mu == nil {
				return nil, errors.Errorf("invalid missing user mount")
			}
			mountable := mu.Mountable()
			if mountable == nil {
				return nil, errors.Errorf("invalid mountable")
			}

			rootMounts, release, err := mountable.Mount()
			if err != nil {
				return nil, err
			}
			defer release()
			ident, err := windows.ResolveUsernameToSID(context.Background(), worker.Executor(), rootMounts, u.ByName.Name)
			if err != nil {
				return nil, err
			}
			return &copy.User{SID: ident.SID}, nil
		default:
			return &copy.User{SID: idtools.ContainerAdministratorSidString}, nil
		}
	}
	return &copy.User{SID: idtools.ContainerAdministratorSidString}, nil
}
