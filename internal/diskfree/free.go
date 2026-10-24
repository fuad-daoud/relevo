// Package diskfree reads the free space under a path. One small shared
// implementation serves every caller that asks "will it fit": the db's cdc
// drop and the round's disk preflight.
package diskfree
