package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/airwallex"
	"fcard/internal/auth"
	"fcard/internal/card"
	"fcard/internal/domain"
	"fcard/internal/storage"
	"fcard/internal/storage/db"
	"fcard/internal/userauth"
	"fcard/migrations"

	"github.com/google/uuid"
)

type sandboxProvider struct {
	mutex        sync.Mutex
	requests     [][]byte
	issued       airwallex.Card
	failure      error
	mismatch     bool
	holderStatus string
	cards        map[string]airwallex.Card
	issuedStatus string
	getFailure   error
}

func (provider *sandboxProvider) GetCardholder(_ context.Context, id string) (airwallex.Cardholder, error) {
	holder := airwallex.Cardholder{ID: id, Status: "READY", Type: "INDIVIDUAL"}
	if provider.holderStatus != "" {
		holder.Status = provider.holderStatus
	}
	holder.Individual.Name.First, holder.Individual.Name.Last = "John", "Smith"
	return holder, nil
}
func (provider *sandboxProvider) CreateCard(_ context.Context, payload json.RawMessage) (airwallex.Card, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	provider.requests = append(provider.requests, append([]byte(nil), payload...))
	var request airwallex.CreateRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return airwallex.Card{}, err
	}
	if provider.cards == nil {
		provider.cards = make(map[string]airwallex.Card)
	}
	if provider.issued.ID != "" {
		provider.cards[provider.issued.ID] = provider.issued
	}
	if provider.issued.ID == "" || provider.issued.RequestID != request.RequestID {
		provider.issued = airwallex.Card{ID: uuid.NewString(), CardholderID: request.CardholderID, RequestID: request.RequestID, Status: "PENDING", Number: "4111111111111111", Metadata: request.Metadata}
		if provider.issuedStatus != "" {
			provider.issued.Status = provider.issuedStatus
		}
	}
	provider.cards[provider.issued.ID] = provider.issued
	if provider.failure != nil {
		return airwallex.Card{}, provider.failure
	}
	result := provider.issued
	if provider.mismatch {
		result.CardholderID = uuid.NewString()
	}
	return result, nil
}
func (provider *sandboxProvider) GetCard(_ context.Context, id string) (airwallex.Card, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	if provider.getFailure != nil {
		return airwallex.Card{}, provider.getFailure
	}
	if provider.issued.ID == id {
		return provider.issued, nil
	}
	if saved, ok := provider.cards[id]; ok {
		return saved, nil
	}
	return airwallex.Card{}, errors.New("not found")
}

type cardFixture struct {
	userFixture
	cards    *card.Service
	provider *sandboxProvider
}

func TestCompanyIncompleteHolderUsesProviderIssuingResult(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		failure   error
		status    string
		errorCode string
	}{
		{name: "provider accepts", status: "SUCCEEDED"},
		{name: "provider rejects", failure: &airwallex.Error{Status: 422, Code: "provider_rejected"}, status: "FAILED", errorCode: "provider_http_422"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newCardFixture(t)
			fixture.provider.holderStatus = "INCOMPLETE"
			fixture.provider.failure = scenario.failure
			customer := fixture.readyUser(t, "incomplete-holder@example.com")
			binding := dataAs[card.Binding](t, send(fixture.router, "POST", "/api/v1/admin/company/cardholder", fixture.adminToken, `{"provider_cardholder_id":"11111111-1111-4111-8111-111111111111"}`), 200)
			if !binding.Bound {
				t.Fatal("incomplete holder blocked company configuration")
			}
			requested := dataAs[card.Operation](t, sendCard(fixture.router, customer.AccessToken, uuid.NewString(), "Incomplete holder card"), 202)
			ctx := context.Background()
			if err := fixture.cards.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			completed, err := fixture.cards.Operation(ctx, customer.User.ID, requested.ID)
			if err != nil || completed.Status != scenario.status || completed.ErrorCode != scenario.errorCode || len(fixture.provider.requests) != 1 {
				t.Fatal("provider issuing result not preserved", completed, err)
			}
		})
	}
}

func TestCompanyIssuingCardsKeepSeparateOwners(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	first := fixture.readyUser(t, "company-first@example.com")
	second := fixture.readyUser(t, "company-second@example.com")
	fixture.bind(t)
	availability := dataAs[card.Availability](t, send(fixture.router, "GET", "/api/v1/user/cards/config", second.AccessToken, ""), 200)
	if !availability.CompanyConfigured {
		t.Fatal("company identity not available to new customer")
	}
	firstOperation := dataAs[card.Operation](t, sendCard(fixture.router, first.AccessToken, uuid.NewString(), "First card"), 202)
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	firstCard := dataAs[card.Card](t, send(fixture.router, "GET", "/api/v1/user/cards/"+firstOperation.CardID.String(), first.AccessToken, ""), 200)
	fixture.provider.issued = airwallex.Card{}
	secondOperation := dataAs[card.Operation](t, sendCard(fixture.router, second.AccessToken, uuid.NewString(), "Second card"), 202)
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	secondCard := dataAs[card.Card](t, send(fixture.router, "GET", "/api/v1/user/cards/"+secondOperation.CardID.String(), second.AccessToken, ""), 200)
	if firstCard.ID == secondCard.ID || firstCard.Status != "PENDING" || secondCard.Status != "PENDING" {
		t.Fatal("shared identity conflated cards")
	}
	for _, pair := range []struct {
		token        string
		own, foreign card.Operation
	}{
		{first.AccessToken, firstOperation, secondOperation}, {second.AccessToken, secondOperation, firstOperation},
	} {
		listed := dataAs[struct {
			Items []card.Card `json:"items"`
			Total int64       `json:"total"`
		}](t, send(fixture.router, "GET", "/api/v1/user/cards", pair.token, ""), 200)
		if listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].ID != pair.own.CardID {
			t.Fatal("cards crossed customer boundary")
		}
		expectStatus(t, send(fixture.router, "GET", "/api/v1/user/cards/"+pair.foreign.CardID.String(), pair.token, ""), 404)
		expectStatus(t, send(fixture.router, "POST", "/api/v1/user/cards/"+pair.foreign.CardID.String()+"/sync", pair.token, ""), 404)
		expectStatus(t, send(fixture.router, "GET", "/api/v1/user/card-operations/"+pair.foreign.ID.String(), pair.token, ""), 404)
	}
	if len(fixture.provider.requests) != 2 {
		t.Fatal("wrong issuance count")
	}
	var firstRequest, secondRequest airwallex.CreateRequest
	if json.Unmarshal(fixture.provider.requests[0], &firstRequest) != nil || json.Unmarshal(fixture.provider.requests[1], &secondRequest) != nil {
		t.Fatal("invalid request")
	}
	if firstRequest.CardholderID != secondRequest.CardholderID || firstRequest.CreatedBy != "John Smith" || secondRequest.CreatedBy != firstRequest.CreatedBy {
		t.Fatal("not using company identity")
	}
	if firstRequest.Metadata["fcard_card_id"] == secondRequest.Metadata["fcard_card_id"] || firstRequest.RequestID == secondRequest.RequestID {
		t.Fatal("requests not independently identified")
	}
	for _, payload := range fixture.provider.requests {
		if bytes.Contains(payload, []byte("fcard_user_id")) || bytes.Contains(payload, []byte(first.User.ID.String())) || bytes.Contains(payload, []byte(second.User.ID.String())) || bytes.Contains(payload, []byte("@example.com")) {
			t.Fatal("customer identity sent to provider")
		}
	}
	var mappings int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM provider_cardholders").Scan(&mappings); err != nil || mappings != 0 {
		t.Fatal("per-customer external mapping created")
	}
}

func TestCompanyIssuingConfigurationPermissionsAndConcurrency(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	customer := fixture.readyUser(t, "config-permissions@example.com")
	path := "/api/v1/admin/company/cardholder"
	for _, token := range []string{"", customer.AccessToken} {
		expectStatus(t, send(fixture.router, "GET", path, token, ""), 401)
		expectStatus(t, send(fixture.router, "POST", path, token, `{"provider_cardholder_id":"11111111-1111-4111-8111-111111111111"}`), 401)
	}
	initial := dataAs[card.Binding](t, send(fixture.router, "GET", path, fixture.adminToken, ""), 200)
	if initial.Bound {
		t.Fatal("unconfigured profile reported configured")
	}
	expectStatus(t, send(fixture.router, "POST", path, fixture.adminToken, `{"provider_cardholder_id":"invalid"}`), 400)
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			_, err := fixture.cards.Bind(ctx, fixture.admin, uuid.NewString(), uuid.NewString())
			results <- err
		}()
	}
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, domain.ErrInvalidState)) || (second == nil && errors.Is(first, domain.ErrInvalidState))) {
		t.Fatal("concurrent configuration overwrote identity", first, second)
	}
	configured := dataAs[card.Binding](t, send(fixture.router, "GET", path, fixture.adminToken, ""), 200)
	if !configured.Bound {
		t.Fatal("company configuration missing")
	}
	replay := dataAs[card.Binding](t, send(fixture.router, "POST", path, fixture.adminToken, `{"provider_cardholder_id":"`+configured.CardholderID+`"}`), 200)
	if replay != configured {
		t.Fatal("company configuration not idempotent")
	}
	response := send(fixture.router, "GET", "/api/v1/user/cards/config", customer.AccessToken, "")
	dataAs[card.Availability](t, response, 200)
	if strings.Contains(response.Body.String(), configured.CardholderID) || strings.Contains(response.Body.String(), configured.LegalName) {
		t.Fatal("company identity exposed to customer")
	}
}

func TestCompanyIssuingMigrationPreservesHistoricalCards(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	customer := fixture.readyUser(t, "legacy-company@example.com")
	chainDown := chainEvidenceDown(t)
	slotDown, err := migrations.Files.ReadFile("000005_card_slots.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrations.Files.ReadFile("000004_company_issuing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, string(chainDown)+string(slotDown)+string(down)+" UPDATE schema_migrations SET version=3;"); err != nil {
		t.Fatal(err)
	}
	holderID, cardID := uuid.NewString(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO provider_cardholders(user_id,provider_cardholder_id,legal_name) VALUES($1,$2,'Legacy Holder')", customer.User.ID, holderID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, "INSERT INTO cards(id,user_id,provider_cardholder_id,nick_name,limit_cents) VALUES($1,$2,$3,'Legacy card',1000)", cardID, customer.User.ID, holderID); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(fixture.pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	saved, err := db.New(fixture.pool).GetUserCard(ctx, db.GetUserCardParams{ID: cardID, UserID: customer.User.ID})
	if err != nil || saved.ProviderCardholderID != holderID || saved.UserID != customer.User.ID {
		t.Fatal("historical ownership changed", err)
	}
	binding, err := fixture.cards.Binding(ctx)
	if err != nil || binding.Bound {
		t.Fatal("legacy customer was silently made company identity")
	}
	fixture.bind(t)
	other := fixture.readyUser(t, "new-company@example.com")
	dataAs[card.Operation](t, sendCard(fixture.router, other.AccessToken, uuid.NewString(), "Company card"), 202)
	var historicalMappings int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM provider_cardholders WHERE user_id=$1", customer.User.ID).Scan(&historicalMappings); err != nil || historicalMappings != 1 {
		t.Fatal("legacy mapping erased")
	}
	if _, err := fixture.pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("unsafe downgrade accepted company cards without legacy mappings")
	}
	if err := storage.Ready(ctx, fixture.pool); err != nil {
		t.Fatal("failed downgrade damaged current schema", err)
	}
}

func newCardFixture(t *testing.T) cardFixture {
	base := newUserFixture(t)
	provider := &sandboxProvider{}
	service := card.New(base.pool, provider, card.Options{Enabled: true, LimitCents: 1000, ProgramPurpose: "COMMERCIAL"})
	adminAuth, err := auth.New(base.pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	base.router = New(adminAuth, base.users, func(ctx context.Context) error { return storage.Ready(ctx, base.pool) }, slog.New(slog.NewTextHandler(io.Discard, nil)), base.service, service)
	return cardFixture{userFixture: base, cards: service, provider: provider}
}
func (fixture cardFixture) readyUser(t *testing.T, email string) userauth.LoginResult {
	t.Helper()
	profile := fixture.create(t, email)
	fixture.reset(t, profile.ID, temporaryPassword)
	first := fixture.login(t, email, temporaryPassword)
	principal, err := fixture.service.Authenticate(context.Background(), first.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.ChangePassword(context.Background(), principal, temporaryPassword, permanentPassword, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return fixture.login(t, email, permanentPassword)
}
func (fixture cardFixture) bind(t *testing.T) {
	t.Helper()
	if _, err := fixture.cards.Bind(context.Background(), fixture.admin, "11111111-1111-4111-8111-111111111111", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
}
func sendCard(handler http.Handler, token, key, nickname string) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(map[string]string{"nick_name": nickname})
	request := httptest.NewRequest("POST", "/api/v1/user/cards", bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSandboxCardsOwnershipAndIdempotency(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	first := fixture.readyUser(t, "card-a@example.com")
	second := fixture.readyUser(t, "card-b@example.com")
	key := uuid.NewString()
	expectStatus(t, sendCard(fixture.router, first.AccessToken, key, "My card"), 409)
	fixture.bind(t)
	operation := dataAs[card.Operation](t, sendCard(fixture.router, first.AccessToken, key, "My card"), 202)
	replay := dataAs[card.Operation](t, sendCard(fixture.router, first.AccessToken, key, "My card"), 202)
	if replay.ID != operation.ID {
		t.Fatal("idempotent replay created a new operation")
	}
	expectStatus(t, sendCard(fixture.router, first.AccessToken, key, "Changed"), 409)
	expectStatus(t, sendCard(fixture.router, first.AccessToken, uuid.NewString(), "Another"), 409)
	expectStatus(t, sendCard(fixture.router, fixture.adminToken, uuid.NewString(), "Admin"), 401)
	expectStatus(t, sendCard(fixture.router, first.AccessToken, "", "No key"), 400)
	foreignPaths := []string{"/api/v1/user/cards/" + operation.CardID.String(), "/api/v1/user/card-operations/" + operation.ID.String()}
	for _, path := range foreignPaths {
		expectStatus(t, send(fixture.router, "GET", path, second.AccessToken, ""), 404)
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/cards/"+operation.CardID.String()+"/sync", second.AccessToken, ""), 404)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/card-operations/"+operation.ID.String()+"/retry", second.AccessToken, ""), 404)
	list := dataAs[struct {
		Total int64 `json:"total"`
	}](t, send(fixture.router, "GET", "/api/v1/user/cards", second.AccessToken, ""), 200)
	if list.Total != 0 {
		t.Fatal("another user's cards listed")
	}
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.cards.Operation(ctx, first.User.ID, operation.ID)
	if err != nil || completed.Status != "SUCCEEDED" {
		t.Fatal("creation not completed")
	}
	response := send(fixture.router, "GET", "/api/v1/user/cards/"+operation.CardID.String(), first.AccessToken, "")
	issued := dataAs[card.Card](t, response, 200)
	if issued.LastFour != "1111" || issued.Status != "PENDING" || strings.Contains(response.Body.String(), "4111111111111111") || strings.Contains(response.Body.String(), "cardholder_id") {
		t.Fatal("sensitive card details leaked or pending reported active")
	}
	var request airwallex.CreateRequest
	if json.Unmarshal(fixture.provider.requests[0], &request) != nil || request.Metadata["fcard_card_id"] != operation.CardID.String() || request.Metadata["fcard_user_id"] != "" || request.CreatedBy != "John Smith" || request.Controls.Limits.Limits[0].Amount.String() != "10.00" || request.Personalized || request.Program.Purpose != "COMMERCIAL" {
		t.Fatal("incorrect provider mapping or limit")
	}
	fixture.provider.issued.Status = "ACTIVE"
	synced := dataAs[card.Card](t, send(fixture.router, "POST", "/api/v1/user/cards/"+operation.CardID.String()+"/sync", first.AccessToken, ""), 200)
	if synced.Status != "ACTIVE" {
		t.Fatal("provider status not synchronized")
	}
	if err := fixture.cards.ProcessOne(ctx); err != nil || len(fixture.provider.requests) != 1 {
		t.Fatal("completed operation repeated")
	}
}

func TestSandboxUnknownRecoveryUsesOriginalLegacyPayload(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "unknown@example.com")
	fixture.bind(t)
	fixture.provider.failure = &airwallex.Error{Code: "network_error"}
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Original"), 202)
	if _, err := fixture.pool.Exec(ctx, "UPDATE provider_operations SET request_payload=jsonb_set(request_payload,'{is_personalized}','true'::jsonb) WHERE id=$1", operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if unknown.Status != "UNKNOWN" {
		t.Fatal("ambiguous outcome incorrectly failed")
	}
	expectStatus(t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Duplicate"), 409)
	principal, err := fixture.service.Authenticate(ctx, login.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.cards.Retry(ctx, principal, operation.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	fixture.provider.failure = nil
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fixture.provider.requests) != 2 || !bytes.Equal(fixture.provider.requests[0], fixture.provider.requests[1]) {
		t.Fatal("retry changed request ID or payload")
	}
	var savedRequest airwallex.CreateRequest
	if json.Unmarshal(fixture.provider.requests[0], &savedRequest) != nil || !savedRequest.Personalized {
		t.Fatal("legacy personalized request was rewritten")
	}
	completed, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if completed.Status != "SUCCEEDED" {
		t.Fatal("retry failed to recover")
	}
}

func TestSandboxMismatchAndAdministratorReconciliation(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "reconcile@example.com")
	fixture.bind(t)
	fixture.provider.mismatch = true
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Reconcile"), 202)
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if unknown.Status != "UNKNOWN" {
		t.Fatal("foreign provider response accepted")
	}
	if _, err := fixture.cards.Reconcile(ctx, fixture.admin, login.User.ID, operation.ID, uuid.NewString(), uuid.NewString()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatal("unverified external card accepted")
	}
	completed, err := fixture.cards.Reconcile(ctx, fixture.admin, login.User.ID, operation.ID, fixture.provider.issued.ID, uuid.NewString())
	if err != nil || completed.Status != "SUCCEEDED" {
		t.Fatal("verified provider result not recovered")
	}
}

func TestSandboxConcurrentCreationAndCompanyIdentity(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "concurrent-card@example.com")
	other := fixture.readyUser(t, "other-mapping@example.com")
	holderID := uuid.NewString()
	if _, err := fixture.cards.Bind(ctx, fixture.admin, holderID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.cards.Bind(ctx, fixture.admin, holderID, uuid.NewString()); err != nil {
		t.Fatal("same company configuration was not idempotent")
	}
	if _, err := fixture.cards.Bind(ctx, fixture.admin, uuid.NewString(), uuid.NewString()); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatal("company identity reassigned")
	}
	otherPrincipal, err := fixture.service.Authenticate(ctx, other.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.cards.Create(ctx, otherPrincipal, uuid.NewString(), uuid.NewString(), card.CreateInput{Nickname: "Other customer"}); err != nil {
		t.Fatal("company identity not shared", err)
	}
	principal, _ := fixture.service.Authenticate(ctx, login.AccessToken)
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			_, err := fixture.cards.Create(ctx, principal, uuid.NewString(), uuid.NewString(), card.CreateInput{Nickname: "Concurrent"})
			results <- err
		}()
	}
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, domain.ErrCardLimit)) || (second == nil && errors.Is(first, domain.ErrCardLimit))) {
		t.Fatal("concurrent requests exceeded sandbox quota", first, second)
	}
}

func TestSandboxRestrictionsAndInterruptedWorker(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	profile := fixture.create(t, "restricted-card@example.com")
	fixture.reset(t, profile.ID, temporaryPassword)
	restricted := fixture.login(t, profile.Email, temporaryPassword)
	expectStatus(t, sendCard(fixture.router, restricted.AccessToken, uuid.NewString(), "Restricted"), 403)
	login := fixture.readyUser(t, "worker-card@example.com")
	fixture.bind(t)
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Interrupted"), 202)
	if _, err := fixture.pool.Exec(ctx, "UPDATE provider_operations SET status='PROCESSING', attempts=1, started_at=now()-interval '2 minutes' WHERE id=$1", operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if unknown.Status != "UNKNOWN" || len(fixture.provider.requests) != 0 {
		t.Fatal("interrupted request automatically replayed")
	}
	fixture.reset(t, login.User.ID, temporaryPassword)
	principal := userauth.Principal{User: login.User, AuthVersion: 1}
	if _, err := fixture.cards.Create(ctx, principal, uuid.NewString(), uuid.NewString(), card.CreateInput{Nickname: "Stale"}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("revoked principal used")
	}
}

func TestSandboxQueuedRevocationAndEventRollback(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "queued-card@example.com")
	fixture.bind(t)
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Queued"), 202)
	fixture.reset(t, login.User.ID, temporaryPassword)
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	failed, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if failed.Status != "FAILED" || len(fixture.provider.requests) != 0 {
		t.Fatal("revoked queued task called provider")
	}
	ready := fixture.readyUser(t, "rollback-card@example.com")
	fixture.bind(t)
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events ADD CONSTRAINT reject_card_events CHECK (action <> 'card.create.request') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, sendCard(fixture.router, ready.AccessToken, uuid.NewString(), "Rollback"), 500)
	count, err := db.New(fixture.pool).CountUserCards(ctx, ready.User.ID)
	if err != nil || count != 0 {
		t.Fatal("card creation did not roll back with security event")
	}
}

func TestSandboxResultPersistenceRollback(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "persist-card@example.com")
	fixture.bind(t)
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Persist"), 202)
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events ADD CONSTRAINT reject_card_success CHECK (action <> 'card.create.SUCCEEDED') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.cards.ProcessOne(ctx); err == nil {
		t.Fatal("event failure ignored")
	}
	item, err := fixture.cards.Get(ctx, login.User.ID, operation.CardID)
	if err != nil || item.Status != "REQUESTED" || item.LastFour != "" {
		t.Fatal("partial provider result committed")
	}
	if _, err := fixture.pool.Exec(ctx, "UPDATE provider_operations SET started_at=now()-interval '2 minutes' WHERE id=$1", operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if unknown.Status != "UNKNOWN" || len(fixture.provider.requests) != 1 {
		t.Fatal("failed save caused a new external request")
	}
}

func TestSandboxRetryRejectionRemainsUnknown(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "retry-rejected@example.com")
	fixture.bind(t)
	fixture.provider.failure = &airwallex.Error{Code: "network_error"}
	operation := dataAs[card.Operation](t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "Unknown"), 202)
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	principal, _ := fixture.service.Authenticate(ctx, login.AccessToken)
	if _, err := fixture.cards.Retry(ctx, principal, operation.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	fixture.provider.failure = &airwallex.Error{Status: 400, Code: "provider_rejected"}
	if err := fixture.cards.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, _ := fixture.cards.Operation(ctx, login.User.ID, operation.ID)
	if unknown.Status != "UNKNOWN" {
		t.Fatal("retry rejection incorrectly proved previous request failed")
	}
	expectStatus(t, sendCard(fixture.router, login.AccessToken, uuid.NewString(), "New"), 409)
}

func TestSandboxMigrationPreservesExistingSessions(t *testing.T) {
	fixture := newCardFixture(t)
	ctx := context.Background()
	login := fixture.readyUser(t, "migration-card@example.com")
	chainDown := chainEvidenceDown(t)
	slotDown, err := migrations.Files.ReadFile("000005_card_slots.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	companyDown, err := migrations.Files.ReadFile("000004_company_issuing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrations.Files.ReadFile("000003_sandbox_cards.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, string(chainDown)+string(slotDown)+string(companyDown)+string(down)+" UPDATE schema_migrations SET version=2;"); err != nil {
		t.Fatal(err)
	}
	if storage.Ready(ctx, fixture.pool) == nil {
		t.Fatal("old schema incorrectly ready")
	}
	if err := storage.Migrate(fixture.pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Authenticate(ctx, login.AccessToken); err != nil {
		t.Fatal("migration invalidated user session")
	}
	adminAuth, err := auth.New(fixture.pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminAuth.Authenticate(ctx, fixture.adminToken); err != nil {
		t.Fatal("migration invalidated administrator session")
	}
	availability, err := fixture.cards.Availability(ctx, userauth.Principal{User: login.User})
	if err != nil || availability.CompanyConfigured {
		t.Fatal("migration fabricated a holder mapping")
	}
}
