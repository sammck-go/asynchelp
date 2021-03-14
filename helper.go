package asyncobj

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// HandleOnceActivator is an interface that may be implemented by the object managed by AsyncObjHelper if
// the object provides its own HandleOnceActivate method. If the object does not provide this method, a handler
// function can be provided directly to DoOnceActivate.
type HandleOnceActivator interface {
	// HandleOnceActivate is called exactly once from DoOnceActivate, in StateActivating, with shutdown deferred,
	// to activate the object that supports shutdown.
	// If it returns nil, the object will be activated. If it returns an error, the object will not be activated,
	// and shutdown will be immediately started.
	// If shutdown has already started before DoOnceActivate is called, this function will not be invoked.
	HandleOnceActivate() error
}

// OnceActivateCallback is a function that is called exactly once, in StateActivating, with shutdown deferred,
// to activate the object that supports shutdown.
// If it returns nil, the object will be activated. If it returns an error, the object will not be activated,
// and shutdown will be immediately started.
// If shutdown has already started before DoOnceActivate is called, this function will not be invoked.
type OnceActivateCallback func() error

// OnceShutdownHandler is a function that will be called exactly once, in StateShuttingDown, in its own goroutine.
// It should take completionError as an advisory completion value, actually shut down, then return the real completion value.
// This function will never be called while shutdown is deferred (and hence, will never be called during activation).
type OnceShutdownHandler func(completionError error) error

// HandleOnceShutdowner is an interface that may be implemented by the object managed by AsyncObjHelper if
// the object provides its own HandleOnceShutdown method. If the object does not provide this method, a handler
// function can be provided directly to InitHelperWithShutdownHandler.
type HandleOnceShutdowner interface {
	// HandleOnceShutdown will be called exactly once, in StateShuttingDown, in its own goroutine. It should take completionError
	// as an advisory completion value, actually shut down, then return the real completion value.
	// This method will never be called while shutdown is deferred.
	HandleOnceShutdown(completionError error) error
}

// WrapHandleOnceShutdowner generates a OnceShutdownHandler function for an object that
// implements HandleOnceShutdowner
func WrapHandleOnceShutdowner(o HandleOnceShutdowner) OnceShutdownHandler {
	return func(completionError error) error {
		return o.HandleOnceShutdown(completionError)
	}
}

// State represents a discreet state in the Helper state machine. During transitions, the state can only move
// to a higher state number.
type State int

// Various State values for the Helper state machine. During transitions, the state can only move
// to a higher state number.
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

// Helper is a a state machine that manages clean asynchronous object activation and shutdown.
// Typically it is included as an anonymous base member of the object being managed, but it
// can also work as an independent managing object.
type Helper struct {
	// Logger is the Logger that will be used for log output from this helper
	Logger

	// o is the object being managed
	obj interface{}

	// Lock is a general-purpose fine-grained mutex for this helper; it may be used
	// as a general-purpose lock by derived objects as well
	Lock sync.Mutex

	// state indicates our procress in the lifecycle of the state machine. Its value
	// states at StateUnactivated and can only increase during state transitions.
	state State

	// The shutdown handler for this managed object, which is called exactly once
	// to perform synchronous local shutdown. Will never be called during activation or
	// when shutdown is deferred.
	shutdownHandler OnceShutdownHandler

	// shutdownDeferCount is the number of times UndeferShutdown() must be called before
	// shutdown can commence. It cannot be incremented once shutdown has started. If
	// isScheduledShutdown is true, then shutdown will commence when this counter becomes
	// 0.
	shutdownDeferCount int

	// isActivated is set to true when SetIsActivated is called. It indicates that the object has been
	// successfully activated. It does not play any other semantic role in the lifecycle
	// of the object, but implementors can use it to decide how to handle API requests, clean
	// shutdown, etc.
	isActivated bool

	// isScheduledShutdown is set to true when StartShutdown is called. This may become set
	// at any time. If shutdown is not deferred, then shutdown commences when it
	// becomes set.  If shutdown is deferred, shutdown will commence as soon as shutdownDeferCount
	// becomes 0.
	isScheduledShutdown bool

	// shutdownErr contains the final completion status after state >= StateLocalShutdown
	shutdownErr error

	// activatingDoneChan is a chan that is closed when the state advances beyond StateActivating. Anyone
	// who wants to can wait on this chan to be notified of the end of the activating phase. This
	// signal does not mean that activation succeeded.
	activatingDoneChan chan struct{}

	// shutdownStartedChan is a chan that is closed when shutdown is started. Anyone
	// who wants to can wait on this chan to be notified of the start of shutdown.
	// This chan will never be closed while shutdown is deferred.
	shutdownStartedChan chan struct{}

	// localShutdownDoneChan is a chan that is closed upon advancein to StateLocalShutdown,
	// after shutdownHandler returns, before we begin waiting on the dependent child
	// waitgroup. It is used to wake up goroutiness that actively shutdown dependents. Anyone can wait
	// on this chan to be notified when local shutdown is complete (the managed
	// object has been shut down but dependent children have not)
	localShutdownDoneChan chan struct{}

	// shutdownDoneChan is a chan that is closed when shutdown is completely done, including
	// dependents.
	// After it is closed, anyone can wait on this chan to be notified
	shutdownDoneChan chan struct{}

	// wg is a sync.WaitGroup that this helper will wait on before it considers final shutdown
	// to be complete. it is incremented for each dependent that we are waiting on. It cannot
	// be incremented after StateShutdown is entered.
	wg sync.WaitGroup
}

// InitHelperWithShutdownHandler initializes a new Helper in place with an independent
// shutdown handler function. Useful for embedding in an object.
func (h *Helper) InitHelperWithShutdownHandler(
	obj interface{},
	logger Logger,
	shutdownHandler OnceShutdownHandler,
) {
	h.Logger = logger
	h.obj = obj
	h.state = StateUnactivated
	h.shutdownHandler = shutdownHandler
	h.activatingDoneChan = make(chan struct{})
	h.shutdownStartedChan = make(chan struct{})
	h.localShutdownDoneChan = make(chan struct{})
	h.shutdownDoneChan = make(chan struct{})
}

// InitHelper initializes a new Helper in place. Useful for embedding in an object.
func (h *Helper) InitHelper(
	logger Logger,
	obj HandleOnceShutdowner,
) {
	h.InitHelperWithShutdownHandler(obj, logger, WrapHandleOnceShutdowner(obj))
}

// NewHelperWithShutdownHandler creates a new Helper as its own object with an independent
// shutdown handler function.
func NewHelperWithShutdownHandler(
	obj interface{},
	logger Logger,
	shutdownHandler OnceShutdownHandler,
) *Helper {
	h := &Helper{
		Logger:                logger,
		obj:                   obj,
		state:                 StateUnactivated,
		shutdownHandler:       shutdownHandler,
		activatingDoneChan:    make(chan struct{}),
		shutdownStartedChan:   make(chan struct{}),
		localShutdownDoneChan: make(chan struct{}),
		shutdownDoneChan:      make(chan struct{}),
	}
	return h
}

// NewHelper creates a new Helper as an independent object
func NewHelper(
	logger Logger,
	obj HandleOnceShutdowner,
) *Helper {
	h := NewHelperWithShutdownHandler(obj, logger, WrapHandleOnceShutdowner(obj))
	return h
}

// GetAsyncObjState returns the current state in the lifecycle of the object.
func (h *Helper) GetAsyncObjState() State {
	h.Lock.Lock()
	defer h.Lock.Unlock()
	return h.state
}

// DeferShutdown increments the shutdown defer count, preventing shutdown from starting. Returns an error
// if shutdown has already started. Note that pausing does not prevent shutdown from being scheduled
// with StartShutDown(), it just prevents actual async shutdown from beginning. Each successful call
// to DeferShutdown must pair with a matching call to UndeferShutdown.
func (h *Helper) DeferShutdown() error {
	h.Lock.Lock()
	defer h.Lock.Unlock()
	if h.state >= StateShuttingDown {
		return h.Errorf("Shutdown already started; cannot defer")
	}
	h.shutdownDeferCount++
	return nil
}

// IsActivated returns true if this helper has ever been successfully activated. Once it becomes
// true, it is never reset, even after shutting down.
func (h *Helper) IsActivated() bool {
	return h.isActivated
}

// SetIsActivated Sets the "activated" flag for this helper if shutdown has not yet started. Does nothing
// if already activated. Fails if shutdown has already been started.
// This method is normally not called directly by applications that perform asynchronous activation--
// they should call DoOnceActivate instead, which indirectly calls this method if activation is successful.
// This method is public mainly for use by simple objects with trivial
// construct-time activation that will complete before the new object is ever exposed. In these special cases,
// the application can simply call SetIsActivated() after construction and before returning the new object--the object
// will never be seen in an inactive or activating state. If this approach is taken, the application *must*
// call SetIsActivated() at construct time, or it is responsible for calling StartShutdown to clean up and drive the state
// to StateShutdown before the object is garbage collected.
func (h *Helper) SetIsActivated() error {
	h.Lock.Lock()
	defer h.Lock.Unlock()

	if !h.isActivated {
		if h.state >= StateShuttingDown {
			return h.Errorf("Cannot activate; shutdown already initiated")
		}
		h.isActivated = true
		h.state = StateActivated
		close(h.activatingDoneChan)
	}

	return nil
}

// DoOnceActivate is called by the application at any point where activation of the object
// is required. Upon successful return, the object has been fully and successfully activated,
// though it may already be shutting down. Upon error return, the object is already scheduled
// for shutdown, and if waitOnFail is true, has been completely shutdown.
//
// This method ensures that activation occurs only once and takes steps to activate the object:
//
//     if already activated, returns nil
//     else if not activated and already started shutting down:
//        if waitOnFail is true, waits for shutdown to complete
//        returns an error
//     else if not activated and not shutting down:
//        defers shutdown
//        invokes the OnceActivateCallback
//        if handler returns nil:
//           activates the object
//           if activation fails:
//             schedules shutting down with error
//             undefers shutdown
//           if activation succeeds, returns nil
//        if handler or activation returns an error:
//           schedules shutting down with that error
//           undefers shutdown
//           if waitOnFail is true, waits for shutdown to complete
//           returns an error
//        undefers shutdown
//        returns nil
//
// If activation fails, the object will go directly into StateShuttingDown without passing through
// StateActivated.
//
// It is safe to call this method multiple times (normally with the same parameters). Only the
// first caller will perform activation, but all callers will complete when the first caller
// completes, and will complete with the same return code.
//
// Note that while onceActivateCallback is running, shutdown is deferred.  This prevents the
// object from being actively shut down while activation is in progress (though a shutdown
// can be scheduled). Because of this, onceActivateCallback *must not* wait for shutdown
// or call Close(), since a deadlock will result.
//
// if onceActivateCallback is nil, interface HandleOnceActivator on the object must be implemented and is used instead.
//
// The caller must not call this method with waitOnFail==true if shutdowns are deferred, unless
// these deferrals can be released before DoOnceActivate returns; otherwise a deadlock will occur.
func (h *Helper) DoOnceActivate(onceActivateCallback OnceActivateCallback, waitOnFail bool) error {
	var err error
	h.Lock.Lock()
	if h.isActivated {
		// Early out for already successfully activated
		h.Lock.Unlock()
		return nil
	}
	if h.state == StateActivating {
		// activating already started by someone else... Wait for it to finish before figuring
		// out what to do next
		h.Lock.Unlock()
		<-h.activatingDoneChan
		h.Lock.Lock()
	}

	if h.isActivated {
		// Already successfully activated
		h.Lock.Unlock()
		return nil
	}

	if h.state >= StateShuttingDown {
		// Shutdown has already started. Optionally wait for complete shutdown, and return an error
		h.Lock.Unlock()
		if waitOnFail {
			err = h.WaitShutdown()
		}
		if err == nil {
			err = h.Errorf("Shutdown of object already started; cannot SetIsActivated")
		}
		return err
	}

	// Defer shutdowns while activating
	h.shutdownDeferCount++

	h.state = StateActivating
	h.Lock.Unlock()

	if onceActivateCallback == nil {
		err = onceActivateCallback()
	} else {
		err = h.obj.(HandleOnceActivator).HandleOnceActivate()
	}

	if err == nil {
		err = h.SetIsActivated()
	}

	if err != nil {
		h.StartShutdown(err)
	}

	// Activation either succeeded or it failed and we have already scheduled shutdown... Allow shutdown
	// to proceed whenever it is the right time...
	h.UndeferShutdown()

	// On error, optionally wait for complete shutdown
	if err != nil && waitOnFail {
		h.WaitShutdown()
	}

	return err
}

// lockedEnterShuttingDownState is the common code used by StartShutdown and UndeferShutdown to
// actually transition to StateShuttingDown.  The lock must be held when this method is called.
func (h *Helper) lockedEnterShuttingDownState() {
	oldState := h.state
	h.state = StateShuttingDown
	if oldState < StateActivated {
		close(h.activatingDoneChan)
	}
	h.DLogf("->shutdownStartedChan")
	close(h.shutdownStartedChan)
}

// UndeferShutdown decrements the shutdown defer count, and if it becomes zero, allows shutdown to start
func (h *Helper) UndeferShutdown() {
	h.Lock.Lock()
	if h.shutdownDeferCount < 1 {
		h.Panic("UndeferShutdown before DeferShutdown")
		return
	}
	h.shutdownDeferCount--
	doShutdownNow := h.shutdownDeferCount == 0 && h.isScheduledShutdown && h.state < StateShuttingDown
	if doShutdownNow {
		h.lockedEnterShuttingDownState()
	}
	h.Lock.Unlock()

	if doShutdownNow {
		h.asyncDoStartedShutdown()
	}
}

// UndeferAndStartShutdown decrements the shutdown defer count and then
// immediately starts shutting down.
// This method is suitable for use in a defer statement after DeferShutdown
func (h *Helper) UndeferAndStartShutdown(completionErr error) {
	h.StartShutdown(completionErr)
	h.UndeferShutdown()
}

// UndeferAndLocalShutdown decrements the shutdown defer count and immediately shuts down.
// Does not wait for dependents to shut down. Returns the final completion code.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
// This method is suitable for use in a golang defer statement after DeferShutdown
func (h *Helper) UndeferAndLocalShutdown(completionErr error) error {
	h.UndeferAndStartShutdown(completionErr)
	return h.WaitLocalShutdown()
}

// UndeferAndShutdown decrements the shutdown defer count and immediately shuts down.
// Returns the final completion code.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
// This method is suitable for use in a golang defer statement after DeferShutdown
func (h *Helper) UndeferAndShutdown(completionErr error) error {
	h.UndeferAndStartShutdown(completionErr)
	return h.WaitShutdown()
}

// UndeferAndLocalShutdownIfNotActivated decrements the shutdown defer count and then
// immediately starts shutting down if the helper has not yet been activated. If
// waitOnFail is true and the helper is not activated, waits for local shutdown, but
// does not wait for dependents to shut down.
// The return code is nil if the helper is activated. Otherise, it is the final
// completion code if waitOnFail is true, or completionErr if not.
// The caller must not call this method with waitOnFail==true if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
// This method is suitable for use in a defer statement after DeferShutdown.
func (h *Helper) UndeferAndLocalShutdownIfNotActivated(completionErr error, waitOnFail bool) error {
	succeeded := h.IsActivated()
	if !succeeded {
		h.StartShutdown(completionErr)
	}
	h.UndeferShutdown()
	var err error = nil
	if !succeeded {
		if waitOnFail {
			err = h.WaitLocalShutdown()
		} else {
			err = completionErr
		}
	}
	return err
}

// UndeferAndShutdownIfNotActivated decrements the shutdown defer count and then
// immediately starts shutting down if the helper has not yet been activated. If
// waitOnFail is true and the helper is not activated, waits for final shutdown.
// The return code is nil if the helper is activated. Otherise, it is the final
// completion code if waitOnFail is true, or completionErr if not.
// The caller must not call this method with waitOnFail==true if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
// This method is suitable for use in a defer statement after DeferShutdown.
func (h *Helper) UndeferAndShutdownIfNotActivated(completionErr error, waitOnFail bool) error {
	succeeded := h.IsActivated()
	if !succeeded {
		h.StartShutdown(completionErr)
	}
	h.UndeferShutdown()
	var err error = nil
	if !succeeded {
		if waitOnFail {
			err = h.WaitShutdown()
		} else {
			err = completionErr
		}
	}
	return err
}

// UndeferAndWaitLocalShutdown decrements the shutdown defer count and waits for local shutdown,
// but does not wait for dependents to shut down.
// Returns the final completion code. Does not actually initiate shutdown, so intended
// for cases when you wish to wait for the natural life of the object.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
// This method is suitable for use in a golang defer statement after DeferShutdown.
func (h *Helper) UndeferAndWaitLocalShutdown(completionErr error) error {
	h.UndeferShutdown()
	return h.WaitLocalShutdown()
}

// UndeferAndWaitShutdown decrements the shutdown defer count and waits for shutdown.
// Returns the final completion code. Does not actually initiate shutdown, so intended
// for cases when you wish to wait for the natural life of the object.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
// This method is suitable for use in a golang defer statement after DeferShutdown.
func (h *Helper) UndeferAndWaitShutdown(completionErr error) error {
	h.UndeferShutdown()
	return h.WaitShutdown()
}

// ShutdownOnContext begins background monitoring of a context.Context, and
// will begin asynchronously shutting down this helper with the context's error
// if the context is completed. This method does not block, it just
// constrains the lifetime of this object to a context.
func (h *Helper) ShutdownOnContext(ctx context.Context) {
	go func() {
		select {
		case <-h.shutdownStartedChan:
		case <-ctx.Done():
			h.StartShutdown(ctx.Err())
		}
	}()
}

// IsScheduledShutdown returns true if StartShutdown() has been called. It continues to return true after shutdown
// is started and completes
func (h *Helper) IsScheduledShutdown() bool {
	h.Lock.Lock()
	defer h.Lock.Unlock()
	return h.isScheduledShutdown
}

// IsStartedShutdown returns true if shutdown has begun. It continues to return true after shutdown
// is complete
func (h *Helper) IsStartedShutdown() bool {
	h.Lock.Lock()
	defer h.Lock.Unlock()
	return h.state >= StateShuttingDown
}

// IsDoneLocalShutdown returns true if local shutdown is complete, not including shutdown of dependents. If
// true, final completion status is available. Continues to return true after final shutdown.
func (h *Helper) IsDoneLocalShutdown() bool {
	h.Lock.Lock()
	defer h.Lock.Unlock()
	return h.state >= StateLocalShutdown
}

// IsDoneShutdown returns true if shutdown is complete, including shutdown of dependents. Final completion
// status is available.
func (h *Helper) IsDoneShutdown() bool {
	h.Lock.Lock()
	defer h.Lock.Unlock()
	return h.state >= StateShutDown
}

// GetShutdownWG returns a sync.WaitGroup that you can call Add() on to
// defer final completion of shutdown until the specified number of calls to
// ShutdownWG().Done() are made. Note that this waitgroup does not prevent shutdown from happening;
// it just holds off code that is waiting for shutdown to complete. This helps with clean and complete
// shutdown before process exit.
// The caller is responsible for not adding to the WaitGroup after StateShutdown has been entered.
// This method rarely needs to be called directly by applications; most of the time AddShutdownChildChan
// is a better choice, as it enforces the StateShutdown exclusion.
func (h *Helper) GetShutdownWG() *sync.WaitGroup {
	return &h.wg
}

// ShutdownStartedChan returns a channel that will be closed as soon as shutdown is initiated. Anyone
// can use this channel to be notified when the object has begun shutting down.
func (h *Helper) ShutdownStartedChan() <-chan struct{} {
	return h.shutdownStartedChan
}

// LocalShutdownDoneChan returns a channel that will be closed when StateLocalShutdown
// is reached, after shutdownHandler, but before children are shut down and waited for. At this time,
// the final completion status is available. Anyone can use this channel to be notified when
// local shutdown is done and the final completion status is available.
func (h *Helper) LocalShutdownDoneChan() <-chan struct{} {
	return h.localShutdownDoneChan
}

// ShutdownDoneChan returns a channel that will be closed when StateShutdown is entered. Shutdown
// is complete, all dependent children have shut down, resources have been freed and final status
// is available. Anyone can use this channel to be notified when final shutdown is complete.
func (h *Helper) ShutdownDoneChan() <-chan struct{} {
	return h.shutdownDoneChan
}

// WaitLocalShutdown waits for the local shutdown to complete, without waiting for dependents
// to finish shutting down, and returns the final completion status.
// It does not initiate shutdown, so it can be used to wait on an object that
// will shutdown at an unspecified point in the future.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
func (h *Helper) WaitLocalShutdown() error {
	<-h.localShutdownDoneChan
	return h.shutdownErr
}

// WaitShutdown waits for the shutdown to complete, including shutdown of all dependents, then
// returns the shutdown status. It does not initiate shutdown, so it can be used to wait on
// an object that will shutdown at an unspecified point in the future.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
func (h *Helper) WaitShutdown() error {
	<-h.shutdownDoneChan
	return h.shutdownErr
}

// LocalShutdown performs a synchronous local shutdown, but does not wait for dependents to
// fully shut down. It initiates shutdown if it has not already started, waits for local
// shutdown to comlete, then returns the final shutdown status.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
func (h *Helper) LocalShutdown(completionError error) error {
	h.StartShutdown(completionError)
	return h.WaitLocalShutdown()
}

// Shutdown performs a synchronous shutdown. It initiates shutdown if it has
// not already started, waits for the shutdown to comlete, then returns
// the final shutdown status.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
func (h *Helper) Shutdown(completionError error) error {
	h.StartShutdown(completionError)
	return h.WaitShutdown()
}

// asyncDoStartedShutdown starts background processing of shutdown *after*
// StateShuttingDown has already been entered and h.shutdownErr has been set
// to the (not final) advisory completion error. It handles the remainder of
// state transitions up to StateShutdown.
func (h *Helper) asyncDoStartedShutdown() {
	go func() {
		shutdownErr := h.shutdownHandler(h.shutdownErr)
		h.DLogf("->shutdownHandlerDone")
		h.Lock.Lock()
		h.shutdownErr = shutdownErr
		h.state = StateLocalShutdown
		close(h.localShutdownDoneChan)
		h.Lock.Unlock()
		h.wg.Wait()
		h.Lock.Lock()
		h.state = StateShutDown
		h.DLogf("->shutdownDone")
		close(h.shutdownDoneChan)
		h.Lock.Unlock()
	}()
}

// StartShutdown shedules asynchronous shutdown of the object. If the object
// has already been scheduled for shutdown, it has no effect. If shutting down has
// been deferred, actual starting of the shutdown process is deferred.
// "completionError" is an advisory error (or nil) to use as the completion status
// from WaitShutdown(). The implementation may use this value or decide to return
// something else.
//
// Asynchronously, this will help kick off the following, only the first time it is called:
//
//  -   Signal that shutdown has been scheduled
//  -   Wait for shutdown defer count to reach 0
//  -   Signal that shutdown has started
//  -   Invoke HandleOnceShutdown with the provided avdvisory completion status. The
//       return value will be used as the final completion status for shutdown
//  -   Signal that HandleOnceShutdown has completed
//  -   For each registered child, call StartShutdown, using the return value from
//       HandleOnceShutdown as an advirory completion status.
//  -   For each registered child, wait for the
//       child to finish shuting down
//  -   For each manually added child done chan, wait for the
//       child done chan to be closed
//  -   Wait for the wait group count to reach 0
//  -   Signals shutdown complete, using the return value from HandleOnceShutdown
//  -    as the final completion code
func (h *Helper) StartShutdown(completionErr error) {
	doShutdownNow := false
	h.Lock.Lock()
	if !h.isScheduledShutdown {
		if h.state >= StateShuttingDown {
			h.Panic("shutdown started before scheduled")
		}
		h.shutdownErr = completionErr
		h.isScheduledShutdown = true
		doShutdownNow = (h.shutdownDeferCount == 0)
		if doShutdownNow {
			h.lockedEnterShuttingDownState()
		}
	}
	h.Lock.Unlock()

	if doShutdownNow {
		h.asyncDoStartedShutdown()
	}
}

// Close is a default implementation of Close(), which simply shuts down
// with an advisory completion status of nil, and returns the final completion
// status. It is OK to call Close multiple times; the same completion code
// will be returned to all callers.
// The caller must not call this method if shutdowns are deferred, unless
// these deferrals can be released before this method returns; otherwise a deadlock will occur.
func (h *Helper) Close() error {
	h.DLogf("Close()")
	return h.Shutdown(nil)
}

// AddShutdownChildChan adds a chan that will be waited on after StateLocalShutdown,
// before this object's shutdown is considered complete. The caller should close the
// chan when conditions have been met to allow shutdown to complete. The Helper will not take
// any action to cause the chan to be closed; it is the caller's responsibility to do that.
// An error is returned if StateShutdown has already been reached.
func (h *Helper) AddShutdownChildChan(childDoneChan <-chan struct{}) error {
	h.DLogf("AddShutdownChildChan()")
	h.Lock.Lock()
	if h.state >= StateShutDown {
		h.Lock.Unlock()
		return fmt.Errorf("Cannot add shutdown child chan; StateShutdown already entered")
	}
	h.wg.Add(1)
	h.Lock.Unlock()
	go func() {
		<-childDoneChan
		h.wg.Done()
	}()
	return nil
}

// AddAsyncShutdownChild adds a dependent asynchronous child object to the set of objects that will be
// actively shut down by this helper after StateLocalShutdown, before this
// object's shutdown is considered complete. The child will be shut down with an advisory
// completion status equal to the status returned from HandleOnceShutdown. The childs final completion
// code is ignored.
// An error is returned if StateShutdown has already been reached.
func (h *Helper) AddAsyncShutdownChild(child AsyncShutdowner) error {
	h.DLogf("AddAsyncShutdownChild(\"%s\")", child)
	h.Lock.Lock()
	if h.state >= StateShutDown {
		h.Lock.Unlock()
		return fmt.Errorf("Cannot add async shutdown child; StateShutdown already entered: \"%s\"", child)
	}
	h.wg.Add(1)
	h.Lock.Unlock()
	go func() {
		select {
		case <-child.ShutdownDoneChan():
			// The child was shut down by someone else before we got to StateLocalShutdown. No reason to keep waiting.
			h.DLogf("Shutdown of child done before local shutdown complete, signalling wg: \"%s\"", child)
		case <-h.localShutdownDoneChan:
			h.DLogf("Local shutdown done, shutting down async child \"%s\"", child)
			child.StartShutdown(h.shutdownErr)
			err := child.WaitShutdown()
			if err == nil {
				h.DLogf("Shutdown of child done, signalling wg: \"%s\"", child)
			} else {
				h.DLogf("Shutdown of child done with error, signalling wg: \"%s\": %s", child, err)
			}
		}
		h.wg.Done()
	}()
	return nil
}

// AddSyncCloseChild adds a dependent child object to the set of objects that will be
// actively closed by this helper after StateLocalShutdown, before this
// object's shutdown is considered complete. The child will be Close()'d in its own
// goroutine, in parallel with shutdown and closure of other dependent children.
// An error is returned if StateShutdown has already been reached.
func (h *Helper) AddSyncCloseChild(child io.Closer) error {
	h.DLogf("AddSyncCloseChild(\"%s\")", child)
	h.Lock.Lock()
	if h.state >= StateShutDown {
		h.Lock.Unlock()
		return fmt.Errorf("Cannot add shutdown child chan; StateShutdown already entered: \"%s\"", child)
	}
	h.wg.Add(1)
	h.Lock.Unlock()
	go func() {
		<-h.localShutdownDoneChan
		h.DLogf("Local shutdown done, shutting down sync Closer child \"%s\"", child)
		err := child.Close()
		if err == nil {
			h.DLogf("Close of child done, signalling wg: \"%s\"", child)
		} else {
			h.DLogf("Close of child done with error, signalling wg: \"%s\": %s", child, err)
		}
		h.wg.Done()
	}()
	return nil
}
