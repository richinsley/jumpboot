//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris)

package jumpboot

import "errors"

// lockEnvDir has no lock to take without flock; the goomba functions refuse
// these systems before they would use it.
func lockEnvDir(dir string) (func(), error) {
	return nil, errors.ErrUnsupported
}
