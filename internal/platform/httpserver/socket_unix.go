//go:build !windows

package httpserver

import "syscall"

const addressInUse = syscall.EADDRINUSE
