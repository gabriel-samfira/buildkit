package file

import (
	"github.com/docker/docker/pkg/idtools"
	copy "github.com/tonistiigi/fsutil/copy"
)

func mapUserToChowner(user *copy.User, idmap *idtools.IdentityMapping) (copy.Chowner, error) {
	if user == nil {
		return func(old *copy.User) (*copy.User, error) {
			if old == nil {
				old = &copy.User{
					SID: idtools.ContainerAdministratorSidString,
				}
			}
			return old, nil
		}, nil
	}
	return func(*copy.User) (*copy.User, error) {
		return user, nil
	}, nil
}
