//go:build !linux && !darwin

package aiprovision

import "fmt"

func (p *Provisioner) lockInitialPublication() (func(), error) {
	return nil, fmt.Errorf("%w: provisioning lock unavailable on this platform", ErrInvalidConfig)
}
