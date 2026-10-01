//go:build !modernc

package db

// schemaGoldenName is the golden this build pins. Turso stores a CREATE
// statement's original text in sqlite_master, where modernc rewrites it, so the
// two builds pin their own exact output.
const schemaGoldenName = "schema_turso.golden"
