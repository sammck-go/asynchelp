package asynchelp

// AsyncShutdowner is an interface implemented by objects that provide
// asynchronous shutdown capability. Shutdown() is similar to Close() except
// that:
//    a) It is safe to call multiple times or concurrently; the first call is effective
//    b) It allows the caller to provide an error condition as the reason for
//       shutdown, which can be used as a return code for subsequent calls,
//       logging, etc.
//    c) It operates asynchronously and provides a chan that is closed after shutdown is
//       complete, so that a caller can wait for clean shutdown
//
// If an implementation also provides Close(), then the object should be closed at completion
// of Shutdown.
//
// See shutdown_helper.go for tools that make it easy to implement this interface.
//
// Methods:
//
// StartShutdown schedules asynchronous shutdown of the object. If the object
// has already been scheduled for shutdown, it has no effect.
// completionErr is an advisory error (or nil) to use as the completion status
// from WaitShutdown(). The implementation may use this value or decide to return
// something else.
//
// ShutdownDoneChan returns a chan that is closed after shutdown is complete.
// After this channel is closed, it is guaranteed that IsDoneShutdown() will
// return true, and WaitForShutdown will not block.
//
// IsDoneShutdown returns false if the object is not yet completely
// shut down. Otherwise it returns true with the guarantee that
// ShutDownDoneChan() will be immediately closed and WaitForShutdown
// will immediately return the final status.
//
// WaitShutdown blocks until the object is completely shut down, and
// returns the final completion status
type AsyncShutdowner interface {
	StartShutdown(completionErr error)
	ShutdownDoneChan() <-chan struct{}
	IsDoneShutdown() bool
	WaitShutdown() error
}
