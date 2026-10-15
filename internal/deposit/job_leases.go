package deposit

import (
	"context"
	"errors"
	"regexp"
	"time"

	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoDepositJob = errors.New("no ready deposit verification job")
var ErrDepositLeaseLost = errors.New("deposit job lease expired or ownership changed")
var depositJobErrorPattern = regexp.MustCompile(`^[A-Z0-9_]{1,64}$`)

type JobLeaseConfig struct {
	Lease       time.Duration
	MaxAttempts int32
	RetryBase   time.Duration
	RetryMax    time.Duration
}

type JobLeaseStore struct {
	pool   *pgxpool.Pool
	config JobLeaseConfig
}

type LeaseRef struct {
	JobID uuid.UUID
	Token uuid.UUID
}

func NewJobLeaseStore(pool *pgxpool.Pool, config JobLeaseConfig) (*JobLeaseStore, error) {
	if pool == nil || config.Lease < 10*time.Second || config.Lease > 5*time.Minute || config.Lease%time.Second != 0 || config.MaxAttempts < 1 || config.MaxAttempts > 20 || config.RetryBase < time.Second || config.RetryBase > time.Minute || config.RetryBase%time.Second != 0 || config.RetryMax < config.RetryBase || config.RetryMax > time.Hour || config.RetryMax%time.Second != 0 {
		return nil, domain.ErrInvalid
	}
	return &JobLeaseStore{pool: pool, config: config}, nil
}

func verificationJobKind(kind string) bool {
	return kind == "VERIFY_CALLBACK" || kind == "VERIFY_CHAIN"
}

func (store *JobLeaseStore) retryDelay(attempts int32) time.Duration {
	delay := store.config.RetryBase
	for attempt := int32(1); attempt < attempts; attempt++ {
		if delay >= store.config.RetryMax/2 {
			return store.config.RetryMax
		}
		delay *= 2
	}
	if delay > store.config.RetryMax {
		return store.config.RetryMax
	}
	return delay
}

func (store *JobLeaseStore) Claim(ctx context.Context, kind string) (db.DepositJob, error) {
	if !verificationJobKind(kind) {
		return db.DepositJob{}, domain.ErrInvalid
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositJob{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	if _, err := queries.ExhaustDepositVerificationJobs(ctx, db.ExhaustDepositVerificationJobsParams{Kind: kind, MaxAttempts: store.config.MaxAttempts}); err != nil {
		return db.DepositJob{}, err
	}
	job, err := queries.ClaimDepositVerificationJob(ctx, db.ClaimDepositVerificationJobParams{Kind: kind, LeaseToken: optionalDepositUUID(uuid.New()), LeaseSeconds: int32(store.config.Lease / time.Second), MaxAttempts: store.config.MaxAttempts})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := transaction.Commit(ctx); err != nil {
			return db.DepositJob{}, err
		}
		return db.DepositJob{}, ErrNoDepositJob
	}
	if err != nil {
		return db.DepositJob{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return db.DepositJob{}, err
	}
	return job, nil
}

func lockJobLease(ctx context.Context, queries *db.Queries, ref LeaseRef) (db.DepositJob, error) {
	if ref.JobID == uuid.Nil || ref.Token == uuid.Nil {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	job, err := queries.LockDepositJob(ctx, ref.JobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	if err != nil {
		return db.DepositJob{}, err
	}
	if !verificationJobKind(job.Kind) || job.Status != "IN_FLIGHT" || !job.LeaseToken.Valid || uuid.UUID(job.LeaseToken.Bytes) != ref.Token {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	return job, nil
}

func setLeasedJob(ctx context.Context, queries *db.Queries, ref LeaseRef, status string, orderID pgtype.UUID, reason string, delay time.Duration) (db.DepositJob, error) {
	job, err := queries.SetLeasedDepositJobState(ctx, db.SetLeasedDepositJobStateParams{ID: ref.JobID, LeaseToken: optionalDepositUUID(ref.Token), Status: status, OrderID: orderID, LastError: reason, RetrySeconds: int32(delay / time.Second)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	return job, err
}

func (store *JobLeaseStore) Renew(ctx context.Context, ref LeaseRef) (db.DepositJob, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositJob{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	if _, err := lockJobLease(ctx, queries, ref); err != nil {
		return db.DepositJob{}, err
	}
	job, err := queries.RenewDepositJobLease(ctx, db.RenewDepositJobLeaseParams{ID: ref.JobID, LeaseToken: optionalDepositUUID(ref.Token), LeaseSeconds: int32(store.config.Lease / time.Second)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	if err != nil {
		return db.DepositJob{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return db.DepositJob{}, err
	}
	return job, nil
}

func (store *JobLeaseStore) transition(ctx context.Context, ref LeaseRef, reason string, retry bool) (db.DepositJob, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositJob{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	current, err := lockJobLease(ctx, queries, ref)
	if err != nil {
		return db.DepositJob{}, err
	}
	state, delay := "DONE", time.Duration(0)
	if retry {
		state, delay = "PENDING", store.retryDelay(current.Attempts)
		if current.Attempts >= store.config.MaxAttempts {
			state, delay = "MANUAL_REVIEW", 0
		}
	}
	job, err := setLeasedJob(ctx, queries, ref, state, current.OrderID, reason, delay)
	if err != nil {
		return db.DepositJob{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return db.DepositJob{}, err
	}
	return job, nil
}

func (store *JobLeaseStore) Finish(ctx context.Context, ref LeaseRef) (db.DepositJob, error) {
	return store.transition(ctx, ref, "", false)
}

func (store *JobLeaseStore) Retry(ctx context.Context, ref LeaseRef, reason string) (db.DepositJob, error) {
	if !depositJobErrorPattern.MatchString(reason) {
		return db.DepositJob{}, domain.ErrInvalid
	}
	return store.transition(ctx, ref, reason, true)
}

func (store *JobLeaseStore) Recover(ctx context.Context, kind string) (int64, error) {
	if !verificationJobKind(kind) {
		return 0, domain.ErrInvalid
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	recovered, err := queries.RecoverDepositVerificationJobs(ctx, db.RecoverDepositVerificationJobsParams{Kind: kind, MaxAttempts: store.config.MaxAttempts, RetrySeconds: int32(store.config.RetryBase / time.Second)})
	if err != nil {
		return 0, err
	}
	exhausted, err := queries.ExhaustDepositVerificationJobs(ctx, db.ExhaustDepositVerificationJobsParams{Kind: kind, MaxAttempts: store.config.MaxAttempts})
	if err != nil {
		return 0, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, err
	}
	return recovered + exhausted, nil
}
