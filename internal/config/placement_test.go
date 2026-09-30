package config

import (
	"reflect"
	"strings"
	"testing"
)

// The bodies below seed a config where one actor places its rounds on a
// configured server.
const (
	placementCandidates = `[{"harness":"claude","provider":"p","model":"m"}]`
	placementServers    = `{"zen":{"url":"https://zen:7777","fingerprint":"sha256:abcd"}}`
	placementActor      = `{"builder":{"agent":"plan-executor","candidates":["m"],"placement":["zen","local"]}}`
)

func TestStoreAcceptsAPlacementNamingAServer(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: placementCandidates,
		Servers:    placementServers,
	})

	if _, err := s.Put(Actors, []byte(placementActor)); err != nil {
		t.Fatalf("Put(actors): %v", err)
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := L.Actors["builder"].Placement; !reflect.DeepEqual(got, []string{"zen", "local"}) {
		t.Errorf("builder placement = %v, want [zen local]", got)
	}
}

func TestStoreAcceptsLocalPlacementWithNoServers(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if _, err := s.Put(Candidates, []byte(placementCandidates)); err != nil {
		t.Fatalf("Put(candidates): %v", err)
	}
	actor := `{"builder":{"agent":"plan-executor","candidates":["m"],"placement":["local"]}}`
	if _, err := s.Put(Actors, []byte(actor)); err != nil {
		t.Fatalf("Put(actors): %v", err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestStoreRefusesAPlacementNamingAnUnknownServer(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: placementCandidates,
		Servers:    placementServers,
	})
	beforeVersion, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}

	actor := `{"builder":{"agent":"plan-executor","candidates":["m"],"placement":["nope"]}}`
	_, err = s.Put(Actors, []byte(actor))
	if err == nil {
		t.Fatal("Put(actors) with an unknown placement server: want a refusal")
	}
	for _, want := range []string{"builder.placement[0]", `"nope"`, "is not in the servers section"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	if ok, err := s.Has(Actors); err != nil || ok {
		t.Errorf("Has(actors) = (%v, %v), want the refusal to write nothing", ok, err)
	}
	if v, err := s.Version(); err != nil || v != beforeVersion {
		t.Errorf("Version = (%d, %v), want %d unchanged", v, err, beforeVersion)
	}
}

func TestStoreRefusesDeletingAServerAnActorNames(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	// The servers section is written before the actors section: the cross-check
	// refuses an actor naming a server the document does not hold.
	if _, err := s.Put(Candidates, []byte(placementCandidates)); err != nil {
		t.Fatalf("Put(candidates): %v", err)
	}
	if _, err := s.Put(Servers, []byte(placementServers)); err != nil {
		t.Fatalf("Put(servers): %v", err)
	}
	if _, err := s.Put(Actors, []byte(placementActor)); err != nil {
		t.Fatalf("Put(actors): %v", err)
	}

	err := s.Delete(Servers)
	if err == nil {
		t.Fatal("Delete(servers) while an actor places on zen: want a refusal")
	}
	for _, want := range []string{"builder.placement[0]", `"zen"`, "is not in the servers section"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	if ok, err := s.Has(Servers); err != nil || !ok {
		t.Errorf("Has(servers) = (%v, %v), want the refusal to leave the body", ok, err)
	}

	local := `{"builder":{"agent":"plan-executor","candidates":["m"],"placement":["local"]}}`
	if _, err := s.Put(Actors, []byte(local)); err != nil {
		t.Fatalf("Put(actors) with the server dropped: %v", err)
	}
	if err := s.Delete(Servers); err != nil {
		t.Fatalf("Delete(servers) once no actor names one: %v", err)
	}
}

func TestCheckActorPlacementIgnoresTheLegacyRolesPath(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if _, err := s.Put(Candidates, []byte(placementCandidates)); err != nil {
		t.Fatalf("Put(candidates): %v", err)
	}
	if _, err := s.Put(Servers, []byte(placementServers)); err != nil {
		t.Fatalf("Put(servers): %v", err)
	}
	if _, err := s.Put(Roles, []byte(`{"builder":{"candidates":["claude/p/m"]}}`)); err != nil {
		t.Fatalf("Put(roles): %v", err)
	}
	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(L.Actors) != 0 {
		t.Errorf("Actors = %v, want none on the legacy roles path", L.Actors)
	}
}
