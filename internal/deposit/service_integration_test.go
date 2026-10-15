package deposit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func managementTestService(test testing.TB, pool *pgxpool.Pool) (*Service, auth.Principal) {
	test.Helper()
	store := depositTestStore(test, pool)
	client, err := epusdt.New(epusdt.Config{Environment: "nile", PID: "1000", Secret: "synthetic-only", BaseURL: "https://gateway.example"})
	if err != nil {
		test.Fatal(err)
	}
	worker, err := NewCreateWorker(store, client, []string{testTronRecipient})
	if err != nil {
		test.Fatal(err)
	}
	service, err := NewService(store, worker)
	if err != nil {
		test.Fatal(err)
	}
	principal := auth.Principal{Admin: auth.Admin{ID: uuid.New()}, SessionID: uuid.New()}
	jobTestExec(test, pool, "INSERT INTO admins(id,email,password_hash) VALUES($1,$2,'synthetic-test-hash')", principal.Admin.ID, principal.Admin.ID.String()+"@example.com")
	jobTestExec(test, pool, "INSERT INTO admin_sessions(id,admin_id,token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')", principal.SessionID, principal.Admin.ID, []byte(principal.SessionID.String()))
	return service, principal
}

func TestDepositManagementIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	service, admin := managementTestService(test, pool)
	user := depositTestPrincipal(test, pool)
	ctx := context.Background()
	order, err := service.store.Prepare(ctx, user, uuid.NewString(), "20.010001")
	if err != nil {
		test.Fatal(err)
	}
	order, err = service.store.StartCreation(ctx, order.ID)
	if err != nil {
		test.Fatal(err)
	}
	order, err = service.store.CreateFailed(ctx, order.ID, uuid.UUID(order.CreateToken.Bytes), true)
	if err != nil {
		test.Fatal(err)
	}
	input := RecoveryInput{ClaimToken: uuid.UUID(order.CreateToken.Bytes), Reason: "reviewed original upstream quote", EvidenceReference: "controlled-export/test-order", Quote: depositTestQuote(order, "20.010002")}
	if _, err := service.RecoverQuote(ctx, auth.Principal{}, order.ID, input, uuid.NewString()); !errors.Is(err, domain.ErrUnauthorized) {
		test.Fatal("untrusted actor recovered", err)
	}
	wrong := input
	wrong.ClaimToken = uuid.New()
	if _, err := service.RecoverQuote(ctx, admin, order.ID, wrong, uuid.NewString()); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal(err)
	}
	wrong = input
	wrong.Quote.ReceiveAddress = "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"
	if _, err := service.RecoverQuote(ctx, admin, order.ID, wrong, uuid.NewString()); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_recovery CHECK(action <> 'CREATE_QUOTE_RECOVERED')")
	if _, err := service.RecoverQuote(ctx, admin, order.ID, input, uuid.NewString()); err == nil {
		test.Fatal("recovery audit failure accepted")
	}
	jobTestExec(test, pool, "ALTER TABLE deposit_events DROP CONSTRAINT reject_recovery")
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_orders WHERE status='CREATE_UNKNOWN'"); count != 1 {
		test.Fatal("partial recovery", count)
	}
	bound, err := service.RecoverQuote(ctx, admin, order.ID, input, uuid.NewString())
	if err != nil || bound.Status != "WAITING" {
		test.Fatal(bound.Status, err)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
		test.Fatal("quote recovery credited")
	}
	page, err := service.List(ctx, user, 1, 20)
	if err != nil || page.Total != 1 || page.Items[0].ReceiveAddress != testTronRecipient {
		test.Fatal(page, err)
	}
	other := depositTestPrincipal(test, pool)
	if _, err := service.Get(ctx, other, order.ID); !errors.Is(err, domain.ErrNotFound) {
		test.Fatal(err)
	}
	management, err := service.Management(ctx, admin, 1, 20)
	if err != nil || management.Orders.Total != 1 || management.Counts.UnresolvedCreates != 0 {
		test.Fatal(err)
	}
	detail, err := service.ManagedOrder(ctx, admin, order.ID)
	if err != nil || detail.ClaimToken == nil || len(detail.Events) < 4 {
		test.Fatal(err)
	}
	jobID := chainTestQueue(test, pool, order, testTronHash())
	jobTestExec(test, pool, "UPDATE deposit_jobs SET status='MANUAL_REVIEW',attempts=10,last_error='PROOF_PENDING' WHERE id=$1", jobID)
	jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_requeue CHECK(action <> 'VERIFICATION_REQUEUED')")
	if err := service.Requeue(ctx, admin, jobID, "reviewed proof backfill", uuid.NewString()); err == nil {
		test.Fatal("requeue audit rollback missing")
	}
	jobTestExec(test, pool, "ALTER TABLE deposit_events DROP CONSTRAINT reject_requeue")
	if err := service.Requeue(ctx, admin, jobID, "reviewed proof backfill", uuid.NewString()); err != nil {
		test.Fatal(err)
	}
	current, err := db.New(pool).GetDepositJob(ctx, jobID)
	if err != nil || current.Status != "PENDING" || current.Attempts != 0 {
		test.Fatal(err)
	}
	var createID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM deposit_jobs WHERE kind='CREATE' AND order_id=$1", order.ID).Scan(&createID); err != nil {
		test.Fatal(err)
	}
	if err := service.Requeue(ctx, admin, createID, "never repeat create POST", uuid.NewString()); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal(err)
	}
	chainTestProof(test, pool, order, "confirm", testTronHash(), 100)
	leases := jobTestLeases(test, pool)
	worker, err := NewChainWorker(leases)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := worker.ScanOnce(ctx); err != nil {
		test.Fatal(err)
	}
	confirmed, err := service.Get(ctx, user, order.ID)
	if err != nil || confirmed.Status != "CONFIRMED" {
		test.Fatal(confirmed.Status, err)
	}
	jobTestExec(test, pool, "UPDATE admin_sessions SET revoked=true WHERE id=$1", admin.SessionID)
	if _, err := service.Management(ctx, admin, 1, 20); !errors.Is(err, domain.ErrUnauthorized) {
		test.Fatal("revoked admin read", err)
	}
}

func TestDepositExpiryIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	service, _ := managementTestService(test, pool)
	user := depositTestPrincipal(test, pool)
	order := depositTestWaiting(test, service.store, user)
	jobTestExec(test, pool, "ALTER TABLE deposit_orders DISABLE TRIGGER deposit_snapshot_immutable")
	jobTestExec(test, pool, "UPDATE deposit_orders SET create_started_at=now()-interval '20 minutes',expires_at=now()-interval '1 minute',reserved_until=now()+interval '1 day'-interval '1 minute' WHERE id=$1", order.ID)
	jobTestExec(test, pool, "ALTER TABLE deposit_orders ENABLE TRIGGER deposit_snapshot_immutable")
	jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_expiry CHECK(action <> 'ORDER_EXPIRED')")
	if _, err := service.Expire(context.Background()); err == nil {
		test.Fatal("expiry audit failure accepted")
	}
	jobTestExec(test, pool, "ALTER TABLE deposit_events DROP CONSTRAINT reject_expiry")
	if count, err := service.Expire(context.Background()); err != nil || count != 1 {
		test.Fatal(count, err)
	}
	if count, err := service.Expire(context.Background()); err != nil || count != 0 {
		test.Fatal(count, err)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_orders WHERE status='EXPIRED' AND reserved_until>now()"); count != 1 {
		test.Fatal("expiry discarded reservation", count)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
		test.Fatal(count)
	}
}

func TestDepositRuntimeAdmission(test *testing.T) {
	runtime := &Runtime{}
	if runtime.AdmissionReady() {
		test.Fatal("unstarted runtime admitted")
	}
	runtime.running.Store(true)
	runtime.lastCaughtUp.Store(time.Now().UnixNano())
	if !runtime.AdmissionReady() {
		test.Fatal("fresh caught-up scanner rejected")
	}
	runtime.lastCaughtUp.Store(time.Now().Add(-time.Minute).UnixNano())
	if runtime.AdmissionReady() {
		test.Fatal("stale scanner admitted")
	}
	if _, err := NewService(nil, nil); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	if managementReason("short", "reference", uuid.NewString()) || managementReason(strings.Repeat("x", 513), "reference", uuid.NewString()) {
		test.Fatal("invalid management reason")
	}
}

func TestDepositAdmissionCapacityIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	service, _ := managementTestService(test, pool)
	user := depositTestPrincipal(test, pool)
	ctx := context.Background()
	key := uuid.NewString()
	first, err := service.Prepare(ctx, user, key, "20")
	if err != nil {
		test.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if _, err := service.Prepare(ctx, user, uuid.NewString(), "20"); err != nil {
			test.Fatal(err)
		}
	}
	if _, err := service.Prepare(ctx, user, uuid.NewString(), "20"); !errors.Is(err, ErrDepositCapacity) {
		test.Fatal("unbounded public queue", err)
	}
	if replay, err := service.Prepare(ctx, user, key, "20"); err != nil || replay.ID != first.ID {
		test.Fatal("capacity blocked original replay", err)
	}
	service.admission = func() bool { return false }
	if service.Enabled() {
		test.Fatal("stale runtime availability")
	}
	if _, err := service.Prepare(ctx, user, key, "20"); !errors.Is(err, ErrDepositsDisabled) {
		test.Fatal("stale runtime admitted prepare", err)
	}
	service.creator.admission = func() bool { return false }
	if _, err := service.creator.Process(ctx, first.ID); !errors.Is(err, ErrDepositsDisabled) {
		test.Fatal("stale runtime claimed creation", err)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_orders WHERE create_token IS NOT NULL"); count != 0 {
		test.Fatal("admission generated claim", count)
	}
}
