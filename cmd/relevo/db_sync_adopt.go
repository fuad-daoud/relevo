package main

import "fmt"

// dbSyncAdoptIntake checks the flags of an adoption before anything is dialed.
// An adoption is the one enable that cannot be walked back -- it spends this
// machine's identity -- so it asks for --yes, and it takes the remote from
// --url or the stored section rather than from an invite code, which names a
// fleet to join and not an installation to become.
func dbSyncAdoptIntake(as string, yes, joining bool) error {
	if as == "" {
		return nil
	}
	if joining {
		return fail(codeUsage, "relevo db sync enable --as: an invite code joins as a new installation: drop --join-stdin or --as")
	}
	if !yes {
		return fail(codeUsage,
			"relevo db sync enable --as %s makes this machine that installation and cannot be undone: pass --yes to continue", as)
	}
	return nil
}

// dbSyncAdoptedLine is what a finished adoption tells the owner to do next: the
// daemon reads its origin when it opens, so it must restart before it can write
// as the adopted installation.
func dbSyncAdoptedLine(as string) string {
	return fmt.Sprintf("this machine is now installation %s; sync stays off until the daemon restarts: restart the relevo daemon, then run `relevo db sync enable` to finish", as)
}
