package xerror

import "errors"

var ErrOutputValidationDependencies = errors.New("output validation requires installed node definitions")
