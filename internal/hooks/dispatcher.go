// Package hooks fires configured hook scripts and webhooks off relevo's
// lifecycle events, without blocking the caller.
package hooks

import "context"

// Dispatcher routes an event to registered hooks; Dispatch must return
// immediately and never block.
type Dispatcher interface {
	Dispatch(ctx context.Context, event Event)
}

// LocalDispatcher runs the argv lists Config configures for an event type,
// each in its own detached goroutine.
type LocalDispatcher struct {
	config   Config
	executor Executor
}

func NewLocalDispatcher(config Config, executor Executor) *LocalDispatcher {
	return &LocalDispatcher{config: config, executor: executor}
}

// Dispatch runs every argv list configured for event.Type, in order, each in
// its own goroutine.
func (d *LocalDispatcher) Dispatch(ctx context.Context, event Event) {
	if d == nil || d.executor == nil {
		return
	}
	for _, argv := range d.config.Hooks[string(event.Type)] {
		go func() { _ = d.executor.Execute(context.Background(), argv, event) }()
	}
}

// MultiDispatcher fans one event out to every member; a nil member is
// skipped.
type MultiDispatcher []Dispatcher

var _ Dispatcher = (MultiDispatcher)(nil)

func (m MultiDispatcher) Dispatch(ctx context.Context, event Event) {
	for _, d := range m {
		if d != nil {
			d.Dispatch(ctx, event)
		}
	}
}
