package xerror

import "errors"

var (
	ErrProductPackageLocation              = errors.New("product package location is invalid")
	ErrProductPackageNotFound              = errors.New("product package was not found")
	ErrProductPackageInvalid               = errors.New("product package is invalid")
	ErrProductPackageUnavailable           = errors.New("product package could not be read safely")
	ErrProductPackageSelection             = errors.New("product package selection is invalid")
	ErrProductPackageIncompatible          = errors.New("product package requirements are unsupported")
	ErrProductPackageVerifierConfiguration = errors.New("product package verifier configuration is invalid")
)
