// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package runner

import (
	"os"
	"syscall"
)

// forwarded are the signals passed on to the job.
var forwarded = []os.Signal{syscall.SIGTERM}
