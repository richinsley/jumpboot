//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package jumpboot

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockEnvDir takes an exclusive, non-blocking flock on the environment
// directory dir, the lock goomba holds on a prefix while it installs into
// it or removes it, so that goomba.CheckComplete reports the environment as
// busy (goomba.ErrPrefixLocked) and goomba.RemoveIncomplete leaves it
// alone. It does not follow a symlink at dir. It returns the release, or an
// error when the lock is held elsewhere or dir cannot be opened.
func lockEnvDir(dir string) (func(), error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "flock", Path: dir, Err: err}
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}
