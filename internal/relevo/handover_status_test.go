package relevo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/view"
)

func TestScopedStatusHandedOverAndTakenOverRows(t *testing.T) {
	rt := newTestRuntime(t, nil)
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}

	now := time.Now()
	var recHW, recHW2 string
	if err := d.Tx(func(tx *db.Tx) error {
		if err := tx.InstallationTouch("worker-b-id", "worker-b", now); err != nil {
			return err
		}
		if err := tx.InstallationTouch("worker-c1-id", "shared-worker", now); err != nil {
			return err
		}
		if err := tx.InstallationTouch("worker-c2-id", "shared-worker", now); err != nil {
			return err
		}

		id1, err := tx.RecordPut(db.Record{Name: "hw", State: "open", Round: 1, CWD: "/repo1", JSON: "{}"})
		if err != nil {
			return err
		}
		recHW = id1
		if err := tx.SetRootWriter("binding_record", recHW, "worker-b-id"); err != nil {
			return err
		}
		if err := tx.RecordBindingLeft(db.BindingLeft{
			RootID: recHW, Owner: "", Name: "hw", ToWriter: "worker-b-id", Kind: db.LeftHandover, At: now,
		}); err != nil {
			return err
		}

		id2, err := tx.RecordPut(db.Record{Name: "hw2", State: "open", Round: 1, CWD: "/repo2", JSON: "{}"})
		if err != nil {
			return err
		}
		recHW2 = id2
		if err := tx.SetRootWriter("binding_record", recHW2, "worker-c1-id"); err != nil {
			return err
		}
		if err := tx.RecordBindingLeft(db.BindingLeft{
			RootID: recHW2, Owner: "", Name: "hw2", ToWriter: "worker-c1-id", Kind: db.LeftTakeover, At: now.Add(-time.Second),
		}); err != nil {
			return err
		}
		return tx.DiscardOutboxSince("binding_record", `["`+recHW2+`"]`, 0)
	}); err != nil {
		t.Fatalf("setup tx: %v", err)
	}

	rep, err := ScopedStatus(context.Background(), rt, Scope{})
	if err != nil {
		t.Fatalf("ScopedStatus: %v", err)
	}

	if len(rep.Bindings) != 0 {
		t.Fatalf("len(rep.Bindings) = %d, want 0", len(rep.Bindings))
	}
	if len(rep.HandedOver) != 2 {
		t.Fatalf("len(rep.HandedOver) = %d, want 2: %+v", len(rep.HandedOver), rep.HandedOver)
	}

	checkRow(t, rep.HandedOver[0], "hw", "worker-b", db.LeftHandover, true)
	checkRow(t, rep.HandedOver[1], "hw2", "worker-c1-id", db.LeftTakeover, false)

	human := view.RenderStatus(rep)
	if want := "hw · handed over to worker-b (not yet exported: relevo db sync)"; !strings.Contains(human, want) {
		t.Errorf("RenderStatus missing %q, got:\n%s", want, human)
	}
	if want := "hw2 · taken over by worker-c1-id"; !strings.Contains(human, want) {
		t.Errorf("RenderStatus missing %q, got:\n%s", want, human)
	}

	doc, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	docStr := string(doc)
	if !strings.Contains(docStr, `"bindings":[]`) && !strings.Contains(docStr, `"bindings": []`) {
		t.Errorf("json missing empty bindings: %s", docStr)
	}
	if !strings.Contains(docStr, `"name":"hw"`) || !strings.Contains(docStr, `"to":"worker-b"`) || !strings.Contains(docStr, `"not_exported":true`) {
		t.Errorf("json missing hw fields: %s", docStr)
	}
	if !strings.Contains(docStr, `"name":"hw2"`) || !strings.Contains(docStr, `"to":"worker-c1-id"`) {
		t.Errorf("json missing hw2 fields: %s", docStr)
	}

	scoped := ScopeReport(rep, Scope{})
	if len(scoped.HandedOver) != 2 {
		t.Fatalf("ScopeReport dropped HandedOver: %+v", scoped.HandedOver)
	}
}

func checkRow(t *testing.T, row view.HandedOverRow, name, to, kind string, notExported bool) {
	t.Helper()
	if row.Name != name || row.To != to || row.Kind != kind || row.NotExported != notExported {
		t.Errorf("row = %+v, want Name=%s To=%s Kind=%s NotExported=%v", row, name, to, kind, notExported)
	}
}
