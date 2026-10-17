package db

import (
	"context"
	"encoding/json"
	"fmt"
)

const ctlPendingKey = "sync.ctl_pending.v1"

// CtlItem is a control entry waiting to be appended to this installation's log.
type CtlItem struct {
	Table       string
	PK          string
	Body        json.RawMessage
	AfterOrigin string
	AfterSeq    int
}

// QueueCtl adds a control entry to the pending list in the caller's transaction,
// so the entry exists exactly when the change it announces does.
func (t *Tx) QueueCtl(item CtlItem) error {
	list, err := loadCtl(t.ctx, t.conn)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(append(list, item))
	if err != nil {
		return fmt.Errorf("db: queue ctl: %w", err)
	}
	return t.KVPut(ctlPendingKey, raw)
}

// PendingCtl lists the control entries not yet appended, oldest first.
func (d *DB) PendingCtl() ([]CtlItem, error) {
	return loadCtl(context.Background(), d.sqlDB)
}

// DropPendingCtl removes the first n pending entries once they were appended.
func (d *DB) DropPendingCtl(n int) error {
	return d.Tx(func(t *Tx) error {
		list, err := loadCtl(t.ctx, t.conn)
		if err != nil {
			return err
		}
		if n > len(list) {
			n = len(list)
		}
		raw, err := json.Marshal(list[n:])
		if err != nil {
			return fmt.Errorf("db: drop ctl: %w", err)
		}
		return t.KVPut(ctlPendingKey, raw)
	})
}

func loadCtl(ctx context.Context, q queryer) ([]CtlItem, error) {
	raw, ok, err := kvGet(ctx, q, ctlPendingKey)
	if err != nil || !ok {
		return nil, err
	}
	var list []CtlItem
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("db: pending ctl: %w", err)
	}
	return list, nil
}
