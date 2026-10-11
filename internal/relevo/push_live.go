package relevo

import "github.com/fuad-daoud/relevo/internal/store"

// PushLive reports whether the mastermind's push claim is live and its record
// is not gone. Every failure reads as not live, as in the report's route.
func PushLive(rt Runtime, mastermindID string) bool {
	now := rt.Now()
	claims, _ := loadClaimMap(rt, now)
	route, live := mastermindRoute(rt, store.Binding{MasterMindID: mastermindID}, &rowLoads{claims: claims})
	return route == "push" && live
}
