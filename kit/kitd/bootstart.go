package kitd

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmartHealthNetwork/shn-kit/supervisor"
)

// BootStartAttempts bounds how many times the Kit's boot builds its stack and
// starts its core children when a child exits during startup.
const BootStartAttempts = 3

// StackStarter is the part of the supervisor the Kit's boot drives.
type StackStarter interface {
	Start(ctx context.Context, spec supervisor.ChildSpec) error
	Stop(name string) error
	Forget(name string) error
}

// ChildStartError is StartStack's error when a child fails to start: Child
// names it and Err is the supervisor's reason.
type ChildStartError struct {
	Child string
	Err   error
}

func (e *ChildStartError) Error() string {
	return fmt.Sprintf("child %s failed to start: %v", e.Child, e.Err)
}

func (e *ChildStartError) Unwrap() error { return e.Err }

// StartStack builds the stack and starts its children in order.
//
// Each child's ports are chosen when the stack is built and bound only when
// the child starts, so another process can take one in between; the child
// then exits during startup (supervisor.StartupExitError). When that
// happens, StartStack stops and forgets every child this attempt started,
// releases what the build itself started, builds the stack again —
// allocating fresh ports for every child whose port the caller did not fix —
// and starts over, up to attempts builds in all, reporting each retry through
// notify. A gateway whose port the caller fixed (Stack.GatewayPortPinned) is
// not retried: a rebuild would reuse the port it could not bind. A child that
// is merely slow is not retried either: a readiness timeout, like every other
// failure, is returned as it is, and so is the last attempt's early exit.
//
// build is called once per attempt; prepare runs after each build and before
// any child starts (publishing the stack's facts, which change with its
// ports). A build or prepare error ends the boot at once, as does a boot the
// caller cancels.
func StartStack(ctx context.Context, sup StackStarter, attempts int, build func() (Stack, error), prepare func(Stack) error, notify func(string)) (Stack, error) {
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return Stack{}, err
		}
		stack, err := build()
		if err != nil {
			return Stack{}, fmt.Errorf("build stack: %w", err)
		}
		if err := prepare(stack); err != nil {
			stack.Close()
			return Stack{}, err
		}
		started := make([]string, 0, len(stack.Children))
		var failed *ChildStartError
		for _, spec := range stack.Children {
			// A failed start registers the child too, so it is stopped and
			// forgotten with the rest before a retry.
			started = append(started, spec.Name)
			if err := sup.Start(ctx, spec); err != nil {
				failed = &ChildStartError{Child: spec.Name, Err: err}
				break
			}
		}
		if failed == nil {
			return stack, nil
		}
		// What the build started in-process is not a child: release it with
		// the attempt (the supervisor stops the children on shutdown).
		stack.Close()
		var early *supervisor.StartupExitError
		switch {
		case ctx.Err() != nil, !errors.As(failed.Err, &early):
			return Stack{}, failed
		case failed.Child == gatewayChildName && stack.GatewayPortPinned:
			notify(fmt.Sprintf("shnkitd: child %s exited during startup and is not retried: its port is fixed by --gateway-port, so a rebuild would reuse the port it could not bind", failed.Child))
			return Stack{}, failed
		case attempt >= attempts:
			return Stack{}, failed
		}
		notify(fmt.Sprintf("shnkitd: child %s exited during startup (attempt %d of %d): %v; retrying on freshly allocated ports", failed.Child, attempt, attempts, failed.Err))
		for i := len(started) - 1; i >= 0; i-- {
			_ = sup.Stop(started[i])
			if err := sup.Forget(started[i]); err != nil {
				// The child's own failure stays findable (errors.As) beside
				// why the retry could not go ahead.
				return Stack{}, errors.Join(failed, fmt.Errorf("retry boot: %w", err))
			}
		}
	}
}
