package doctor

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// SyncMeterFunc reads the local sync quota and idle-export figures. It lives
// in cmd/relevo beside `relevo status` so the two surfaces keep reading the
// same file.
type SyncMeterFunc func(rt relevo.Runtime) (relevosync.QuotaReading, bool, relevosync.ExportHour, bool)

// Deps carries the cmd-owned values the doctor report reads: the build facts
// and helpers that stay in cmd/relevo because other commands use them. Every
// field is a value or a plain function; the report never names cmd/relevo.
type Deps struct {
	Env              Env
	Version          string
	ConfigRoot       string
	RepoOf           func(ctx context.Context, rt relevo.Runtime, cwd string) db.Repo
	RepoKnown        func(db.Repo) bool
	ChatResolver     chatlabel.Resolver
	RestartCheck     func(relevo.Runtime) Check
	InsertRestartRow func(checks []Check, c Check) []Check
	SyncMeter        SyncMeterFunc
}
