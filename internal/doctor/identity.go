package doctor

// GitIdentityInput is everything the git identity row needs that
// GitIdentityCheck cannot read itself. cmd/relevo gathers it; the rule lives
// here.
type GitIdentityInput struct {
	HasServers bool   // at least one remote server is configured
	InRepo     bool   // the working directory is inside a git work tree
	Name       string // resolved identity; "" when the key is unset
	Email      string
}

// GitIdentityCheck is the `relevo bind --server` preflight: a remote builder
// commits as the client, so a repo whose effective user.name or user.email
// is unset makes every remote add refuse. ok is false (no row) unless there
// is a server to add to and the working directory is inside a repo.
func GitIdentityCheck(in GitIdentityInput) (Check, bool) {
	if !in.HasServers || !in.InRepo {
		return Check{}, false
	}

	if in.Name != "" && in.Email != "" {
		return Check{
			Name:     "git identity",
			Severity: SevOK,
			Detail:   in.Name + " <" + in.Email + ">",
		}, true
	}

	detail := "user.name not set"
	switch {
	case in.Name == "" && in.Email == "":
		detail = "user.name/user.email not set: relevo bind --server will refuse"
	case in.Email == "":
		detail = "user.email not set"
	}
	return Check{
		Name:     "git identity",
		Severity: SevWarn,
		Detail:   detail,
		Fix:      "git config --global user.name '<your name>' && git config --global user.email '<you@example.com>'",
	}, true
}
