//go:build unix

package owner

import "strings"

// transactionOpen reports whether this connection is inside a transaction,
// which is what Drain waits for before it drops the clients.
func (c *conn) transactionOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inTx
}

// recordTransaction moves this connection's transaction membership from the
// statement that just succeeded. The caller orders it against that statement's
// ack: a drain reads this flag to decide whether to wait for the connection, so
// an opener is recorded before its ack is sent and a closer only after, keeping
// the flag true for every moment an answer is on the wire. A failed COMMIT
// leaves the flag set: SQLite keeps that transaction open, so the connection
// still counts as in one.
func (c *conn) recordTransaction(query string) {
	open, closeTx := txEffect(query)
	if !open && !closeTx {
		return
	}
	c.mu.Lock()
	c.inTx = open
	c.mu.Unlock()
}

// txEffect reads a statement's leading keyword for the transaction state it
// leaves behind. BEGIN (and BEGIN IMMEDIATE) opens; COMMIT, END and ROLLBACK
// close.
func txEffect(query string) (open, closeTx bool) {
	switch firstKeyword(query) {
	case "BEGIN":
		return true, false
	case "COMMIT", "END", "ROLLBACK":
		return false, true
	}
	return false, false
}

// firstKeyword returns the statement's first word, upper-cased, so BEGIN
// IMMEDIATE is seen as BEGIN and the keyword match is case-insensitive.
func firstKeyword(query string) string {
	query = strings.TrimLeft(query, " \t\r\n")
	if i := strings.IndexAny(query, " \t\r\n("); i >= 0 {
		return strings.ToUpper(query[:i])
	}
	return strings.ToUpper(query)
}
