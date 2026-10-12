package httpapi

import (
	"context"
	"errors"
	"testing"

	"fcard/internal/airwallex"
	"fcard/internal/card"
	"fcard/internal/domain"
	"fcard/internal/storage"
	"fcard/internal/storage/db"
	"fcard/internal/userauth"
	"fcard/migrations"

	"github.com/google/uuid"
)

func issueSlotCard(t *testing.T, fixture cardFixture, login userauth.LoginResult, key, nickname string) card.Operation {
	t.Helper()
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, key, nickname), 202)
	if err := fixture.cards.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	return operation
}

func expectOccupiedSlots(t *testing.T, fixture cardFixture, login userauth.LoginResult, expected int64) {
	t.Helper()
	availability := dataAs[card.Availability](t, send(fixture.router, "GET", "/api/v1/user/cards/config", login.AccessToken, ""), 200)
	if availability.OccupiedSlots != expected {
		t.Fatalf("expected %d occupied slots, got %d", expected, availability.OccupiedSlots)
	}
}

func TestClosedCardReleasesSlotAndPreservesHistory(t *testing.T) {
	fixture := newCardFixture(t)
	login := fixture.readyUser(t, "closed-slot@example.com")
	foreign := fixture.readyUser(t, "foreign-slot@example.com")
	fixture.bind(t)
	ctx := context.Background()
	firstKey := uuid.NewString()
	first := issueSlotCard(t, fixture, login, firstKey, "Old card")
	oldProviderID := fixture.provider.issued.ID
	fixture.provider.issued.Status = "CLOSED"
	expectOccupiedSlots(t, fixture, login, 1)
	expectStatus(t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Before sync"), 409)
	path := "/api/v1/user/cards/" + first.CardID.String() + "/sync"
	expectStatus(t, send(fixture.router, "POST", path, foreign.AccessToken, ""), 404)
	expectOccupiedSlots(t, fixture, login, 1)
	closed := dataAs[card.Card](t, send(fixture.router, "POST", path, login.AccessToken, ""), 200)
	if closed.Status != "CLOSED" {
		t.Fatal("closure not synchronized")
	}
	expectOccupiedSlots(t, fixture, login, 0)
	operation, err := fixture.cards.Operation(ctx, login.User.ID, first.ID)
	if err != nil || operation.Status != "SUCCEEDED" {
		t.Fatal("historical success was rewritten", err)
	}
	replay := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, firstKey, "Old card"), 202)
	if replay.ID != first.ID {
		t.Fatal("old idempotency key created a replacement")
	}
	expectStatus(t, sendCard(fixture.router, login.AccessToken, firstKey, "Changed card"), 409)
	dataAs[card.Card](t, send(fixture.router, "POST", path, login.AccessToken, ""), 200)
	var releases int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM user_security_events WHERE user_id=$1 AND action='card.slot.released'", login.User.ID).Scan(&releases); err != nil || releases != 1 {
		t.Fatal("closure release not audited exactly once", err)
	}
	replacement := issueSlotCard(t, fixture, login, uuid.NewString(), "New enterprise card")
	if replacement.CardID == first.CardID || fixture.provider.issued.ID == oldProviderID {
		t.Fatal("replacement reused old card")
	}
	expectOccupiedSlots(t, fixture, login, 1)
	listed := dataAs[struct {
		Items []card.Card `json:"items"`
		Total int64       `json:"total"`
	}](t, send(fixture.router, "GET", "/api/v1/user/cards", login.AccessToken, ""), 200)
	if listed.Total != 2 || len(listed.Items) != 2 {
		t.Fatal("old card history erased")
	}
	old := dataAs[card.Card](t, send(fixture.router, "GET", "/api/v1/user/cards/"+first.CardID.String(), login.AccessToken, ""), 200)
	if old.Status != "CLOSED" {
		t.Fatal("old card reopened")
	}
	stale := fixture.provider.cards[oldProviderID]
	stale.Status = "ACTIVE"
	fixture.provider.cards[oldProviderID] = stale
	expectStatus(t, send(fixture.router, "POST", path, login.AccessToken, ""), 409)
	expectOccupiedSlots(t, fixture, login, 1)
	saved, err := db.New(fixture.pool).GetUserCard(ctx, db.GetUserCardParams{ID: first.CardID, UserID: login.User.ID})
	if err != nil || saved.Status != "CLOSED" {
		t.Fatal("stale provider snapshot reopened closed card", err)
	}
	expectStatus(t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Third card"), 409)
}

func TestNonClosedProviderStatesKeepCardSlot(t *testing.T) {
	for _, status := range []string{"PENDING", "ACTIVE", "INACTIVE", "BLOCKED", "LOST", "STOLEN", "EXPIRED", "UNKNOWN", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			fixture := newCardFixture(t)
			login := fixture.readyUser(t, "nonclosed-slot@example.com")
			fixture.bind(t)
			operation := issueSlotCard(t, fixture, login, uuid.NewString(), "Reserved")
			fixture.provider.issued.Status = status
			dataAs[card.Card](t, send(fixture.router, "POST", "/api/v1/user/cards/"+operation.CardID.String()+"/sync", login.AccessToken, ""), 200)
			expectOccupiedSlots(t, fixture, login, 1)
			expectStatus(t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Blocked replacement"), 409)
		})
	}
}

func TestCloseSyncVerificationAndReleaseRollback(t *testing.T) {
	fixture := newCardFixture(t)
	login := fixture.readyUser(t, "rollback-slot@example.com")
	fixture.bind(t)
	ctx := context.Background()
	operation := issueSlotCard(t, fixture, login, uuid.NewString(), "Original")
	path := "/api/v1/user/cards/" + operation.CardID.String() + "/sync"
	fixture.provider.issued.Status = "CLOSED"
	fixture.provider.getFailure = &airwallex.Error{Code: "network_error"}
	expectStatus(t, send(fixture.router, "POST", path, login.AccessToken, ""), 502)
	expectOccupiedSlots(t, fixture, login, 1)
	fixture.provider.getFailure = nil
	holderID := fixture.provider.issued.CardholderID
	fixture.provider.issued.CardholderID = uuid.NewString()
	expectStatus(t, send(fixture.router, "POST", path, login.AccessToken, ""), 502)
	expectOccupiedSlots(t, fixture, login, 1)
	fixture.provider.issued.CardholderID = holderID
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events ADD CONSTRAINT reject_slot_release CHECK (action <> 'card.slot.released') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, send(fixture.router, "POST", path, login.AccessToken, ""), 500)
	expectOccupiedSlots(t, fixture, login, 1)
	saved, err := fixture.cards.Get(ctx, login.User.ID, operation.CardID)
	if err != nil || saved.Status != "PENDING" {
		t.Fatal("partial closure committed", err)
	}
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events DROP CONSTRAINT reject_slot_release"); err != nil {
		t.Fatal(err)
	}
	dataAs[card.Card](t, send(fixture.router, "POST", path, login.AccessToken, ""), 200)
	expectOccupiedSlots(t, fixture, login, 0)
}

func TestConcurrentCloseAndReplacementKeepSingleSlot(t *testing.T) {
	fixture := newCardFixture(t)
	login := fixture.readyUser(t, "concurrent-slot@example.com")
	fixture.bind(t)
	ctx := context.Background()
	operation := issueSlotCard(t, fixture, login, uuid.NewString(), "Original")
	fixture.provider.issued.Status = "CLOSED"
	syncResults := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() { _, err := fixture.cards.Sync(ctx, login.User.ID, operation.CardID); syncResults <- err }()
	}
	for index := 0; index < 2; index++ {
		if err := <-syncResults; err != nil {
			t.Fatal(err)
		}
	}
	expectOccupiedSlots(t, fixture, login, 0)
	principal, err := fixture.service.Authenticate(ctx, login.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			_, err := fixture.cards.Create(ctx, principal, uuid.NewString(), uuid.NewString(), card.CreateInput{Nickname: "Concurrent replacement"})
			results <- err
		}()
	}
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, domain.ErrCardLimit)) || (second == nil && errors.Is(first, domain.ErrCardLimit))) {
		t.Fatal("concurrent replacement exceeded quota", first, second)
	}
	expectOccupiedSlots(t, fixture, login, 1)
	var releases int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM user_security_events WHERE user_id=$1 AND action='card.slot.released'", login.User.ID).Scan(&releases); err != nil || releases != 1 {
		t.Fatal("concurrent sync duplicated release", err)
	}
}

func TestClosedIssuanceAndReconciliationReleaseSlot(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		name := "issuance response"
		if reconcile {
			name = "unknown reconciliation"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCardFixture(t)
			login := fixture.readyUser(t, "closed-result@example.com")
			fixture.bind(t)
			fixture.provider.issuedStatus = "CLOSED"
			if reconcile {
				fixture.provider.failure = &airwallex.Error{Code: "network_error"}
			}
			operation := issueSlotCard(t, fixture, login, uuid.NewString(), "Closed result")
			if reconcile {
				expectOccupiedSlots(t, fixture, login, 1)
				result, err := fixture.cards.Reconcile(context.Background(), fixture.admin, login.User.ID, operation.ID, fixture.provider.issued.ID, uuid.NewString())
				if err != nil || result.Status != "SUCCEEDED" {
					t.Fatal("closed result reconciliation failed", err)
				}
			}
			expectOccupiedSlots(t, fixture, login, 0)
			historical := dataAs[card.Operation](t, send(fixture.router, "GET", "/api/v1/user/card-operations/"+operation.ID.String(), login.AccessToken, ""), 200)
			if historical.Status != "SUCCEEDED" {
				t.Fatal("closed result lost success history")
			}
			dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Replacement"), 202)
		})
	}
}

func TestCardSlotMigrationBackfillsOnlyResolvedClosures(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	closedUser := fixture.readyUser(t, "migrate-closed@example.com")
	failedUser := fixture.readyUser(t, "migrate-failed@example.com")
	unknownUser := fixture.readyUser(t, "migrate-unknown@example.com")
	fixture.bind(t)
	closed := issueSlotCard(t, fixture, closedUser, uuid.NewString(), "Closed")
	fixture.provider.issued.Status = "CLOSED"
	dataAs[card.Card](t, send(fixture.router, "POST", "/api/v1/user/cards/"+closed.CardID.String()+"/sync", closedUser.AccessToken, ""), 200)
	fixture.provider.failure = &airwallex.Error{Status: 422, Code: "provider_rejected"}
	issueSlotCard(t, fixture, failedUser, uuid.NewString(), "Failed")
	fixture.provider.failure = &airwallex.Error{Code: "network_error"}
	unknown := issueSlotCard(t, fixture, unknownUser, uuid.NewString(), "Unknown")
	if _, err := fixture.pool.Exec(ctx, "UPDATE cards SET status='CLOSED',provider_card_id=$2 WHERE id=$1", unknown.CardID, fixture.provider.issued.ID); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.Files.ReadFile("000005_card_slots.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, string(down)+" UPDATE schema_migrations SET version=4;"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(fixture.pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	expectOccupiedSlots(t, fixture, closedUser, 0)
	expectOccupiedSlots(t, fixture, failedUser, 0)
	expectOccupiedSlots(t, fixture, unknownUser, 1)
	if _, err := fixture.pool.Exec(ctx, "UPDATE provider_operations SET holds_card_slot=false WHERE id=$1", unknown.ID); err == nil {
		t.Fatal("database allowed unresolved operation to release its slot")
	}
	historical, err := fixture.cards.Operation(ctx, closedUser.User.ID, closed.ID)
	if err != nil || historical.Status != "SUCCEEDED" {
		t.Fatal("migration rewrote successful operation", err)
	}
	dataAs[card.Operation](t, sendCard(fixture.router, closedUser.AccessToken, uuid.NewString(), "After migration"), 202)
	if _, err := fixture.pool.Exec(ctx, "UPDATE provider_operations SET holds_card_slot=true WHERE id=$1", closed.ID); err == nil {
		t.Fatal("database allowed two reservations for the same user")
	}
	if _, err := fixture.pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("unsafe downgrade with replacement history succeeded")
	}
	if err := storage.Ready(ctx, fixture.pool); err != nil {
		t.Fatal("failed downgrade damaged current schema", err)
	}
}
