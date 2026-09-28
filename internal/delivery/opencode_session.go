package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// OpencodeSessionFinder finds the active OpenCode session id matching a working directory.
type OpencodeSessionFinder struct {
	Exec    usage.Exec
	DBPath  string
	Timeout time.Duration
}

// Find queries OpenCode's SQLite database for session records and returns the matching session id.
func (f OpencodeSessionFinder) Find(cwd string, now time.Time) (string, error) {
	if f.Exec == nil {
		return "", fmt.Errorf("%w: nil exec", mastermind.ErrNoOpencodeSession)
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sessions, err := f.sessions(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %s", mastermind.ErrNoOpencodeSession, err.Error())
	}

	return mastermind.MatchOpencodeSession(cwd, sessions, now)
}

// Directory returns the working directory OpenCode recorded for one session,
// so a caller that knows only the session id can resolve its repository.
func (f OpencodeSessionFinder) Directory(sessionID string) (string, error) {
	if f.Exec == nil {
		return "", fmt.Errorf("%w: nil exec", mastermind.ErrNoOpencodeSession)
	}
	if sessionID == "" {
		return "", fmt.Errorf("%w: empty session id", mastermind.ErrNoOpencodeSession)
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sessions, err := f.sessions(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %s", mastermind.ErrNoOpencodeSession, err.Error())
	}
	for _, s := range sessions {
		if s.ID == sessionID && s.Directory != "" {
			return s.Directory, nil
		}
	}
	return "", fmt.Errorf("%w: session %s has no recorded directory", mastermind.ErrNoOpencodeSession, sessionID)
}

// sessions reads OpenCode's session records: OpenCode 2.0.14 keeps its
// own in session_v2, and the legacy session table only holds pre-2.0 rows. The
// union keeps a legacy row only when session_v2 has no row with the same id, so
// an id never appears twice. A database with only one of the two tables, or one
// without session_message, still works.
func (f OpencodeSessionFinder) sessions(ctx context.Context) ([]mastermind.OpencodeSession, error) {
	tables, err := f.tableSet(ctx)
	if err != nil {
		return nil, err
	}

	var sessions []mastermind.OpencodeSession
	if tables["session_v2"] {
		rows, err := f.readSessions(ctx, opencodeV2SessionQuery(tables["session_message"]))
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, rows...)
	}
	if tables["session"] {
		rows, err := f.readSessions(ctx, opencodeLegacySessionQuery(tables["session_v2"]))
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, rows...)
	}
	return sessions, nil
}

// tableSet reports which of OpenCode's tables the database has, so a database
// that predates session_v2 (or has not yet created session_message) does not
// turn into a failed query.
func (f OpencodeSessionFinder) tableSet(ctx context.Context) (map[string]bool, error) {
	out, err := f.Exec.Run(ctx, "sqlite3", "-readonly", "-json", f.DBPath,
		"select name from sqlite_master where type = 'table'")
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(out))
	tables := map[string]bool{}
	if len(trimmed) == 0 {
		return tables, nil
	}

	var rows []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		tables[r.Name] = true
	}
	return tables, nil
}

// readSessions runs one session query and decodes its json rows.
func (f OpencodeSessionFinder) readSessions(ctx context.Context, query string) ([]mastermind.OpencodeSession, error) {
	out, err := f.Exec.Run(ctx, "sqlite3", "-readonly", "-json", f.DBPath, query)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(out))
	if len(trimmed) == 0 {
		return nil, nil
	}

	var rows []struct {
		ID           string  `json:"id"`
		Directory    string  `json:"directory"`
		ParentID     *string `json:"parent_id"`
		Title        string  `json:"title"`
		Updated      int64   `json:"updated"`
		TimeArchived any     `json:"time_archived"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, err
	}

	sessions := make([]mastermind.OpencodeSession, 0, len(rows))
	for _, r := range rows {
		var parentID string
		if r.ParentID != nil {
			parentID = *r.ParentID
		}
		sessions = append(sessions, mastermind.OpencodeSession{
			ID:        r.ID,
			Directory: r.Directory,
			ParentID:  parentID,
			Title:     r.Title,
			Updated:   time.UnixMilli(r.Updated),
			Archived:  r.TimeArchived != nil,
		})
	}
	return sessions, nil
}

// opencodeV2SessionQuery reads OpenCode 2.0's session_v2 rows. A v2 session's
// time_updated is not bumped as the session works, so its activity time is the
// greatest of time_updated, time_idle, time_viewed and the latest
// session_message row's time_created.
func opencodeV2SessionQuery(hasMessages bool) string {
	updated := "max(coalesce(time_updated, 0), coalesce(time_idle, 0), coalesce(time_viewed, 0)"
	if hasMessages {
		updated += ", (select coalesce(max(time_created), 0) from session_message" +
			" where session_message.session_id = session_v2.id)"
	}
	updated += ")"

	return "select id, directory, parent_id, title, time_archived," +
		" " + updated + " as updated from session_v2"
}

// opencodeLegacySessionQuery reads the pre-2.0 session table. When session_v2
// exists, the ids it holds are dropped here, so the union has no duplicates.
// Its activity time stays time_updated.
func opencodeLegacySessionQuery(hasV2 bool) string {
	query := "select id, directory, parent_id, title, time_updated as updated, time_archived from session"
	if hasV2 {
		query += " where id not in (select id from session_v2)"
	}
	return query
}
