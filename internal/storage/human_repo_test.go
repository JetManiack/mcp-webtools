package storage

import (
	"sync"
	"testing"
)

func TestGetOrCreateHumanActorCreatesOnce(t *testing.T) {
	db := openTestDB(t)

	first, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "admin")
	if err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}
	if first.Kind != ActorKindHuman {
		t.Errorf("Kind = %q, want %q", first.Kind, ActorKindHuman)
	}

	second, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "admin")
	if err != nil {
		t.Fatalf("second GetOrCreateHumanActor: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second call created a new actor (%q vs %q)", second.ID, first.ID)
	}

	var count int64
	if err := db.Model(&Actor{}).Where("kind = ?", ActorKindHuman).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d human actors exist, want 1", count)
	}
}

// A role change in the identity provider (someone added to or removed from the
// admin group) has to take effect on their next request, not on a new login.
func TestGetOrCreateHumanActorUpdatesRole(t *testing.T) {
	db := openTestDB(t)

	if _, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "viewer"); err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}
	if _, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "admin"); err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}

	var identity UserIdentity
	if err := db.First(&identity, "keycloak_subject = ?", "sub-1").Error; err != nil {
		t.Fatalf("load identity: %v", err)
	}
	if identity.Role != "admin" {
		t.Errorf("Role = %q, want admin", identity.Role)
	}

	// And back down again — a demotion must propagate just as readily as a
	// promotion.
	if _, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "viewer"); err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}
	if err := db.First(&identity, "keycloak_subject = ?", "sub-1").Error; err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if identity.Role != "viewer" {
		t.Errorf("Role = %q, want viewer", identity.Role)
	}
}

func TestGetOrCreateHumanActorSeparatesSubjects(t *testing.T) {
	db := openTestDB(t)

	ada, err := GetOrCreateHumanActor(db, "sub-1", "Ada", "admin")
	if err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}
	grace, err := GetOrCreateHumanActor(db, "sub-2", "Grace", "viewer")
	if err != nil {
		t.Fatalf("GetOrCreateHumanActor: %v", err)
	}
	if ada.ID == grace.ID {
		t.Error("two different subjects share one actor")
	}
}

// Two concurrent first-requests for the same subject must produce exactly one
// Actor. Both racers pass the same display name, so a naive implementation
// collides on Actor.DisplayName's unique index and one request fails with a
// 500 on an otherwise ordinary login.
func TestGetOrCreateHumanActorConcurrentFirstRequests(t *testing.T) {
	db := openTestDB(t)

	const racers = 8
	var wg sync.WaitGroup
	ids := make([]string, racers)
	errs := make([]error, racers)

	wg.Add(racers)
	for i := range racers {
		go func() {
			defer wg.Done()
			actor, err := GetOrCreateHumanActor(db, "sub-race", "Ada", "admin")
			errs[i] = err
			if actor != nil {
				ids[i] = actor.ID
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: %v", i, err)
		}
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Errorf("racer %d got actor %q, racer 0 got %q — more than one actor was created", i, id, ids[0])
		}
	}

	var count int64
	if err := db.Model(&Actor{}).Where("kind = ?", ActorKindHuman).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d human actors exist, want exactly 1", count)
	}
}
