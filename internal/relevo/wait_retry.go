package relevo

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	waitRetryInitial = 250 * time.Millisecond
	waitRetryMax     = 2 * time.Second
	waitRetryLimit   = 60 * time.Second
)

// waitRetryer retries transient database failures during the wait poll loop.
type waitRetryer struct {
	reader   waitReader
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	stderr   io.Writer
	streakAt time.Time
	inStreak bool
}

func newWaitRetryer(rt Runtime, opts WaitOptions) *waitRetryer {
	rdr := opts.reader
	if rdr == nil {
		rdr = rt.Store
	}
	nowFn := opts.now
	if nowFn == nil {
		nowFn = rt.Now
		if nowFn == nil {
			nowFn = time.Now
		}
	}
	sleepFn := opts.sleep
	if sleepFn == nil {
		sleepFn = defaultWaitSleep
	}
	errOut := opts.stderr
	if errOut == nil {
		errOut = os.Stderr
	}
	return &waitRetryer{
		reader: rdr,
		now:    nowFn,
		sleep:  sleepFn,
		stderr: errOut,
	}
}

func defaultWaitSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (r *waitRetryer) load(ctx context.Context, name string) (store.Binding, error) {
	var b store.Binding
	err := r.retry(ctx, func() error {
		var lerr error
		b, lerr = r.reader.Load(name)
		return lerr
	})
	return b, err
}

func (r *waitRetryer) readLog(ctx context.Context, name string) ([]store.LogEntry, error) {
	var entries []store.LogEntry
	err := r.retry(ctx, func() error {
		var rerr error
		entries, rerr = r.reader.ReadLog(name)
		return rerr
	})
	return entries, err
}

func (r *waitRetryer) retry(ctx context.Context, op func() error) error {
	delay := waitRetryInitial
	for {
		err := op()
		if err == nil {
			r.inStreak = false
			return nil
		}
		if !db.IsTransient(err) {
			r.inStreak = false
			return err
		}

		now := r.now()
		if !r.inStreak {
			r.inStreak = true
			r.streakAt = now
			fmt.Fprintf(r.stderr, "relevo: db read failed (%v); retrying\n", err)
		}
		if now.Sub(r.streakAt) >= waitRetryLimit {
			return err
		}

		if serr := r.sleep(ctx, delay); serr != nil {
			return serr
		}
		delay *= 2
		if delay > waitRetryMax {
			delay = waitRetryMax
		}
	}
}
