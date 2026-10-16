package ingest

import "time"

// The renderers print local clocks and the tests pin their bytes in UTC. The
// zone is set here, before any test goroutine exists: assigning time.Local
// while a timer or a database connection cleaner calls time.Now is a data race.
func init() { time.Local = time.UTC }
