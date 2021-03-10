# asyncobj
A golang package to simplify management of interdependent objects with asynchronous lifecycles. Makes clean shutdown
and cleanup of failed initialization easier.

[![GoDoc](https://godoc.org/github.com/sammck-go/asyncobj?status.svg)](https://godoc.org/github.com/sammck-go/asyncobj)


### Features

- Easy to use
- Drop into struct as an anonymous base to enable async activation and shutdown
- Threadsafe
- Includes logging support
- Ability to attach dependencies that must be shut down when an object is shut down

**Source**

```sh
$ go get -v github.com/sammck-go/asyncobj
```

### Package Usage

<!-- render these help texts by hand,
  or use https://github.com/jpillora/md-tmpl
    with $ md-tmpl -w README.md -->

<!--tmpl:echo && godocdown -template ./.godocdown.template -->
```go
import "github.com/sammck-go/asyncobj"
```

Package asyncobj provides tools that make it easier to build and manage objects
that have asynchronous lifecycles. In particular, it provides a pattern for
clean asynchronous activation and shutdown of objects with blocking resources
that must be cleanly released.

## Usage

#### type AsyncShutdowner

```go
type AsyncShutdowner interface {
	StartShutdown(completionErr error)
	ShutdownDoneChan() <-chan struct{}
	IsDoneShutdown() bool
	WaitShutdown() error
}
```

AsyncShutdowner is an interface implemented by objects that provide asynchronous
shutdown capability. Shutdown() is similar to Close() except that:

    a) It is safe to call multiple times or concurrently; the first call is effective
    b) It allows the caller to provide an error condition as the reason for
       shutdown, which can be used as a return code for subsequent calls,
       logging, etc.
    c) It operates asynchronously and provides a chan that is closed after shutdown is
       complete, so that a caller can wait for clean shutdown

If an implementation also provides Close(), then the object should be closed at
completion of Shutdown.

See shutdown_helper.go for tools that make it easy to implement this interface.

Methods:

StartShutdown schedules asynchronous shutdown of the object. If the object has
already been scheduled for shutdown, it has no effect. completionErr is an
advisory error (or nil) to use as the completion status from WaitShutdown(). The
implementation may use this value or decide to return something else.

ShutdownDoneChan returns a chan that is closed after shutdown is complete,
including shutdown of dependents. After this channel is closed, it is guaranteed
that IsDoneShutdown() will return true, and WaitForShutdown will not block.

IsDoneShutdown returns false if the object and all dependents have not yet
completely shut down. Otherwise it returns true with the guarantee that
ShutDownDoneChan() will be immediately closed and WaitForShutdown will
immediately return the final status.

WaitShutdown blocks until the object is completely shut down, and returns the
final completion status

#### type HandleOnceShutdowner

```go
type HandleOnceShutdowner interface {
	// Shutdown will be called exactly once, in StateShuttingDown, in its own goroutine. It should take completionError
	// as an advisory completion value, actually shut down, then return the real completion value.
	// This method will never be called while shutdown is deferred.
	HandleOnceShutdown(completionError error) error
}
```

HandleOnceShutdowner is an interface that may be implemented by the object
managed by AsyncObjHelper if the object provides its own HandleOnceShutdown
method. If the object does not provide this method, a handler function can be
provided directly to InitHelperWithShutdownHandler.

#### type Helper

```go
type Helper struct {
	// Logger is the Logger that will be used for log output from this helper
	Logger

	// Lock is a general-purpose fine-grained mutex for this helper; it may be used
	// as a general-purpose lock by derived objects as well
	Lock sync.Mutex
}
```

Helper is a a state machine that manages clean asynchronous object activation
and shutdown. Typically it is included as an anonymous base member of the object
being managed, but it can also work as an independent managing object.

#### func  NewHelper

```go
func NewHelper(
	logger Logger,
	o HandleOnceShutdowner,
) *Helper
```
NewHelper creates a new Helper as an independent object

#### func  NewHelperWithShutdownHandler

```go
func NewHelperWithShutdownHandler(
	logger Logger,
	shutdownHandler OnceShutdownHandler,
) *Helper
```
NewHelperWithShutdownHandler creates a new Helper as its own object with an
independent shutdown handler function.

#### func (*Helper) AddAsyncShutdownChild

```go
func (h *Helper) AddAsyncShutdownChild(child AsyncShutdowner) error
```
AddAsyncShutdownChild adds a dependent asynchronous child object to the set of
objects that will be actively shut down by this helper after StateLocalShutdown,
before this object's shutdown is considered complete. The child will be shut
down with an advisory completion status equal to the status returned from
HandleOnceShutdown. The childs final completion code is ignored. An error is
returned if StateShutdown has already been reached.

#### func (*Helper) AddShutdownChildChan

```go
func (h *Helper) AddShutdownChildChan(childDoneChan <-chan struct{}) error
```
AddShutdownChildChan adds a chan that will be waited on after
StateLocalShutdown, before this object's shutdown is considered complete. The
caller should close the chan when conditions have been met to allow shutdown to
complete. The Helper will not take any action to cause the chan to be closed; it
is the caller's responsibility to do that. An error is returned if StateShutdown
has already been reached.

#### func (*Helper) AddSyncCloseChild

```go
func (h *Helper) AddSyncCloseChild(child io.Closer) error
```
AddSyncCloseChild adds a dependent child object to the set of objects that will
be actively closed by this helper after StateLocalShutdown, before this object's
shutdown is considered complete. The child will be Close()'d in its own
goroutine, in parallel with shutdown and closure of other dependent children. An
error is returned if StateShutdown has already been reached.

#### func (*Helper) Close

```go
func (h *Helper) Close() error
```
Close is a default implementation of Close(), which simply shuts down with an
advisory completion status of nil, and returns the final completion status. It
is OK to call Close multiple times; the same completion code will be returned to
all callers. The caller must not call this method if shutdowns are deferred,
unless these deferrals can be released before this method returns; otherwise a
deadlock will occur.

#### func (*Helper) DeferShutdown

```go
func (h *Helper) DeferShutdown() error
```
DeferShutdown increments the shutdown defer count, preventing shutdown from
starting. Returns an error if shutdown has already started. Note that pausing
does not prevent shutdown from being scheduled with StartShutDown(), it just
prevents actual async shutdown from beginning. Each successful call to
DeferShutdown must pair with a matching call to UndeferShutdown.

#### func (*Helper) DoOnceActivate

```go
func (h *Helper) DoOnceActivate(onceActivateCallback OnceActivateCallback, waitOnFail bool) error
```
DoOnceActivate is called by the application at any point where activation of the
object is required. Upon successful return, the object has been fully and
successfully activated, though it may already be shutting down. Upon error
return, the object is already scheduled for shutdown, and if waitOnFail is true,
has been completely shutdown.

This method ensures that activation occurs only once and takes steps to activate
the object:

    if already activated, returns nil
    else if not activated and already started shutting down:
       if waitOnFail is true, waits for shutdown to complete
       returns an error
    else if not activated and not shutting down:
       defers shutdown
       invokes the OnceActivateCallback
       if handler returns nil:
          activates the object
          if activation fails:
            schedules shutting down with error
            undefers shutdown
          if activation succeeds, returns nil
       if handler or activation returns an error:
          schedules shutting down with that error
          undefers shutdown
          if waitOnFail is true, waits for shutdown to complete
          returns an error
       undefers shutdown
       returns nil

If activation fails, the object will go directly into StateShuttingDown without
passing through StateActivated.

It is safe to call this method multiple times (normally with the same
parameters). Only the first caller will perform activation, but all callers will
complete when the first caller completes, and will complete with the same return
code.

Note that while onceAcivateCallback is running, shutdown is deferred. This
prevents the object from being actively shut down while activation is in
progress (though a shutdown can be scheduled). Because of this,
onceActivateCallback *must not* wait for shutdown or call Close(), since a
deadlock will result.

The caller must not call this method with waitOnFail==true if shutdowns are
deferred, unless these deferrals can be released before DoOnceActivate returns;
otherwise a deadlock will occur.

#### func (*Helper) GetAsyncObjState

```go
func (h *Helper) GetAsyncObjState() State
```
GetAsyncObjState returns the current state in the lifecycle of the object.

#### func (*Helper) GetShutdownWG

```go
func (h *Helper) GetShutdownWG() *sync.WaitGroup
```
GetShutdownWG returns a sync.WaitGroup that you can call Add() on to defer final
completion of shutdown until the specified number of calls to
ShutdownWG().Done() are made. Note that this waitgroup does not prevent shutdown
from happening; it just holds off code that is waiting for shutdown to complete.
This helps with clean and complete shutdown before process exit. The caller is
responsible for not adding to the WaitGroup after StateShutdown has been
entered. This method rarely needs to be called directly by applications; most of
the time AddShutdownChildChan is a better choice, as it enforces the
StateShutdown exclusion.

#### func (*Helper) InitHelper

```go
func (h *Helper) InitHelper(
	logger Logger,
	o HandleOnceShutdowner,
)
```
InitHelper initializes a new Helper in place. Useful for embedding in an object.

#### func (*Helper) InitHelperWithShutdownHandler

```go
func (h *Helper) InitHelperWithShutdownHandler(
	logger Logger,
	shutdownHandler OnceShutdownHandler,
)
```
InitHelperWithShutdownHandler initializes a new Helper in place with an
independent shutdown handler function. Useful for embedding in an object.

#### func (*Helper) IsActivated

```go
func (h *Helper) IsActivated() bool
```
IsActivated returns true if this helper has ever been successfully activated.
Once it becomes true, it is never reset, even after shutting down.

#### func (*Helper) IsDoneLocalShutdown

```go
func (h *Helper) IsDoneLocalShutdown() bool
```
IsDoneLocalShutdown returns true if local shutdown is complete, not including
shutdown of dependents. If true, final completion status is available. Continues
to return true after final shutdown.

#### func (*Helper) IsDoneShutdown

```go
func (h *Helper) IsDoneShutdown() bool
```
IsDoneShutdown returns true if shutdown is complete, including shutdown of
dependents. Final completion status is available.

#### func (*Helper) IsScheduledShutdown

```go
func (h *Helper) IsScheduledShutdown() bool
```
IsScheduledShutdown returns true if StartShutdown() has been called. It
continues to return true after shutdown is started and completes

#### func (*Helper) IsStartedShutdown

```go
func (h *Helper) IsStartedShutdown() bool
```
IsStartedShutdown returns true if shutdown has begun. It continues to return
true after shutdown is complete

#### func (*Helper) LocalShutdown

```go
func (h *Helper) LocalShutdown(completionError error) error
```
LocalShutdown performs a synchronous local shutdown, but does not wait for
dependents to fully shut down. It initiates shutdown if it has not already
started, waits for local shutdown to comlete, then returns the final shutdown
status. The caller must not call this method if shutdowns are deferred, unless
these deferrals can be released before this method returns; otherwise a deadlock
will occur.

#### func (*Helper) LocalShutdownDoneChan

```go
func (h *Helper) LocalShutdownDoneChan() <-chan struct{}
```
LocalShutdownDoneChan returns a channel that will be closed when
StateLocalShutdown is reached, after shutdownHandler, but before children are
shut down and waited for. At this time, the final completion status is
available. Anyone can use this channel to be notified when local shutdown is
done and the final completion status is available.

#### func (*Helper) SetIsActivated

```go
func (h *Helper) SetIsActivated() error
```
SetIsActivated Sets the "activated" flag for this helper if shutdown has not yet
started. Does nothing if already activated. Fails if shutdown has already been
started. This method is normally not called directly by applications that
perform asynchronous activation-- they should call DoOnceActivate instead, which
indirectly calls this method if activation is successful. This method is public
mainly for use by simple objects with trivial construct-time activation that
will complete before the new object is ever exposed. In these special cases, the
application can simply call SetIsActivated() after construction and before
returning the new object--the object will never be seen in an inactive or
activating state. If this approach is taken, the application *must* call
SetIsActivated() at construct time, or it is responsible for calling
StartShutdown to clean up and drive the state to StateShutdown before the object
is garbage collected.

#### func (*Helper) Shutdown

```go
func (h *Helper) Shutdown(completionError error) error
```
Shutdown performs a synchronous shutdown. It initiates shutdown if it has not
already started, waits for the shutdown to comlete, then returns the final
shutdown status. The caller must not call this method if shutdowns are deferred,
unless these deferrals can be released before this method returns; otherwise a
deadlock will occur.

#### func (*Helper) ShutdownDoneChan

```go
func (h *Helper) ShutdownDoneChan() <-chan struct{}
```
ShutdownDoneChan returns a channel that will be closed when StateShutdown is
entered. Shutdown is complete, all dependent children have shut down, resources
have been freed and final status is available. Anyone can use this channel to be
notified when final shutdown is complete.

#### func (*Helper) ShutdownOnContext

```go
func (h *Helper) ShutdownOnContext(ctx context.Context)
```
ShutdownOnContext begins background monitoring of a context.Context, and will
begin asynchronously shutting down this helper with the context's error if the
context is completed. This method does not block, it just constrains the
lifetime of this object to a context.

#### func (*Helper) ShutdownStartedChan

```go
func (h *Helper) ShutdownStartedChan() <-chan struct{}
```
ShutdownStartedChan returns a channel that will be closed as soon as shutdown is
initiated. Anyone can use this channel to be notified when the object has begun
shutting down.

#### func (*Helper) StartShutdown

```go
func (h *Helper) StartShutdown(completionErr error)
```
StartShutdown shedules asynchronous shutdown of the object. If the object has
already been scheduled for shutdown, it has no effect. If shutting down has been
deferred, actual starting of the shutdown process is deferred. "completionError"
is an advisory error (or nil) to use as the completion status from
WaitShutdown(). The implementation may use this value or decide to return
something else.

Asynchronously, this will help kick off the following, only the first time it is
called:

    -   Signal that shutdown has been scheduled
    -   Wait for shutdown defer count to reach 0
    -   Signal that shutdown has started
    -   Invoke HandleOnceShutdown with the provided avdvisory completion status. The
         return value will be used as the final completion status for shutdown
    -   Signal that HandleOnceShutdown has completed
    -   For each registered child, call StartShutdown, using the return value from
         HandleOnceShutdown as an advirory completion status.
    -   For each registered child, wait for the
         child to finish shuting down
    -   For each manually added child done chan, wait for the
         child done chan to be closed
    -   Wait for the wait group count to reach 0
    -   Signals shutdown complete, using the return value from HandleOnceShutdown
    -    as the final completion code

#### func (*Helper) UndeferAndLocalShutdown

```go
func (h *Helper) UndeferAndLocalShutdown(completionErr error) error
```
UndeferAndLocalShutdown decrements the shutdown defer count and immediately
shuts down. Does not wait for dependents to shut down. Returns the final
completion code. The caller must not call this method if shutdowns are deferred,
unless these deferrals can be released before this method returns; otherwise a
deadlock will occur. This method is suitable for use in a golang defer statement
after DeferShutdown

#### func (*Helper) UndeferAndLocalShutdownIfNotActivated

```go
func (h *Helper) UndeferAndLocalShutdownIfNotActivated(completionErr error, waitOnFail bool) error
```
UndeferAndLocalShutdownIfNotActivated decrements the shutdown defer count and
then immediately starts shutting down if the helper has not yet been activated.
If waitOnFail is true and the helper is not activated, waits for local shutdown,
but does not wait for dependents to shut down. The return code is nil if the
helper is activated. Otherise, it is the final completion code if waitOnFail is
true, or completionErr if not. The caller must not call this method with
waitOnFail==true if shutdowns are deferred, unless these deferrals can be
released before this method returns; otherwise a deadlock will occur. This
method is suitable for use in a defer statement after DeferShutdown.

#### func (*Helper) UndeferAndShutdown

```go
func (h *Helper) UndeferAndShutdown(completionErr error) error
```
UndeferAndShutdown decrements the shutdown defer count and immediately shuts
down. Returns the final completion code. The caller must not call this method if
shutdowns are deferred, unless these deferrals can be released before this
method returns; otherwise a deadlock will occur. This method is suitable for use
in a golang defer statement after DeferShutdown

#### func (*Helper) UndeferAndShutdownIfNotActivated

```go
func (h *Helper) UndeferAndShutdownIfNotActivated(completionErr error, waitOnFail bool) error
```
UndeferAndShutdownIfNotActivated decrements the shutdown defer count and then
immediately starts shutting down if the helper has not yet been activated. If
waitOnFail is true and the helper is not activated, waits for final shutdown.
The return code is nil if the helper is activated. Otherise, it is the final
completion code if waitOnFail is true, or completionErr if not. The caller must
not call this method with waitOnFail==true if shutdowns are deferred, unless
these deferrals can be released before this method returns; otherwise a deadlock
will occur. This method is suitable for use in a defer statement after
DeferShutdown.

#### func (*Helper) UndeferAndStartShutdown

```go
func (h *Helper) UndeferAndStartShutdown(completionErr error)
```
UndeferAndStartShutdown decrements the shutdown defer count and then immediately
starts shutting down. This method is suitable for use in a defer statement after
DeferShutdown

#### func (*Helper) UndeferAndWaitLocalShutdown

```go
func (h *Helper) UndeferAndWaitLocalShutdown(completionErr error) error
```
UndeferAndWaitLocalShutdown decrements the shutdown defer count and waits for
local shutdown, but does not wait for dependents to shut down. Returns the final
completion code. Does not actually initiate shutdown, so intended for cases when
you wish to wait for the natural life of the object. The caller must not call
this method if shutdowns are deferred, unless these deferrals can be released
before this method returns; otherwise a deadlock will occur. This method is
suitable for use in a golang defer statement after DeferShutdown.

#### func (*Helper) UndeferAndWaitShutdown

```go
func (h *Helper) UndeferAndWaitShutdown(completionErr error) error
```
UndeferAndWaitShutdown decrements the shutdown defer count and waits for
shutdown. Returns the final completion code. Does not actually initiate
shutdown, so intended for cases when you wish to wait for the natural life of
the object. The caller must not call this method if shutdowns are deferred,
unless these deferrals can be released before this method returns; otherwise a
deadlock will occur. This method is suitable for use in a golang defer statement
after DeferShutdown.

#### func (*Helper) UndeferShutdown

```go
func (h *Helper) UndeferShutdown()
```
UndeferShutdown decrements the shutdown defer count, and if it becomes zero,
allows shutdown to start

#### func (*Helper) WaitLocalShutdown

```go
func (h *Helper) WaitLocalShutdown() error
```
WaitLocalShutdown waits for the local shutdown to complete, without waiting for
dependents to finish shutting down, and returns the final completion status. It
does not initiate shutdown, so it can be used to wait on an object that will
shutdown at an unspecified point in the future. The caller must not call this
method if shutdowns are deferred, unless these deferrals can be released before
this method returns; otherwise a deadlock will occur.

#### func (*Helper) WaitShutdown

```go
func (h *Helper) WaitShutdown() error
```
WaitShutdown waits for the shutdown to complete, including shutdown of all
dependents, then returns the shutdown status. It does not initiate shutdown, so
it can be used to wait on an object that will shutdown at an unspecified point
in the future. The caller must not call this method if shutdowns are deferred,
unless these deferrals can be released before this method returns; otherwise a
deadlock will occur.

#### type Logger

```go
type Logger logger.Logger
```

Logger is a convenient type alias for logger.Logger

#### type OnceActivateCallback

```go
type OnceActivateCallback func() error
```

OnceActivateCallback is a function that is called exactly once, in
StateActivating, with shutdown deferred, to activate the object that supports
shutdown. If it returns nil, the object will be activated. If it returns an
error, the object will not be activated, and shutdown will be immediately
started. If shutdown has already started before DoOnceActivate is called, this
function will not be invoked.

#### type OnceShutdownHandler

```go
type OnceShutdownHandler func(completionError error) error
```

OnceShutdownHandler is a function that will be called exactly once, in
StateShuttingDown, in its own goroutine. It should take completionError as an
advisory completion value, actually shut down, then return the real completion
value. This function will never be called while shutdown is deferred (and hence,
will never be called during activation).

#### func  WrapHandleOnceShutdowner

```go
func WrapHandleOnceShutdowner(o HandleOnceShutdowner) OnceShutdownHandler
```
WrapHandleOnceShutdowner generates a OnceShutdownHandler function for an object
that implements HandleOnceShutdowner

#### type State

```go
type State int
```

State represents a discreet state in the Helper state machine. During
transitions, the state can only move to a higher state number.

```go
const (
	// StateUnactivated indicates that activation has not yet started
	StateUnactivated State = iota

	// StateActivating indicates that activation has begun, but has not yet completed.
	// shutdown is deferred during this state. If activation fails, there will be
	// a transition directly to StateShuttingDown.
	StateActivating State = iota

	// StateActivated indicates that the object is fully and successfully activated and has not yet begun
	// shutting down.  Note that a shutdown may have been scheduled, if shutdown is deferred.
	StateActivated State = iota

	// StateShuttingDown indicates that shutdown has been initiated. shutdown can no longer
	// be deferred. APIs should complete quickly and may return errors. Note that this state
	// may be entered without ever entering StateActivating or StateActivated, if shutdown
	// is initiated before activation, or if activation fails.
	StateShuttingDown State = iota

	// StateLocalShutdown indicates that the object is effectively shut down, and a final
	// completion code is available; however, we are still waiting for registered dependent objects
	// to be shut down before we declare shutdown complete. This makes it easier to achieve clean and complete
	// shutdown before the host program exits or resources need to be reacquired.
	StateLocalShutdown State = iota

	// StateShutdown
	StateShutDown State = iota
)
```
Various State values for the Helper state machine. During transitions, the state
can only move to a higher state number.
<!--/tmpl-->

### Contributing

- http://golang.org/doc/code.html
- http://golang.org/doc/effective_go.html

### Changelog

- `1.0` - Initial release.

### Todo

- Better tests

#### MIT License

Copyright © 2021 Sam McKelvie &lt;dev@mckelvie.org&gt;

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
'Software'), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED 'AS IS', WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
