package lock

import "runtime"

const runtimeGOOS = runtime.GOOS

func asAlready(err error, target **AlreadyRunningError) bool {
	if e, ok := err.(*AlreadyRunningError); ok {
		*target = e
		return true
	}
	return false
}
