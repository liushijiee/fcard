package deposit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"
	"fcard/internal/userauth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDepositsDisabled = errors.New("new deposit orders are disabled")
var ErrDepositCapacity = errors.New("too many unresolved deposit orders")
var depositAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$`)
var depositTradePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type OrderStoreConfig struct {
	MaxOrderUnits    int64
	Enabled          bool
	Chain            TronVerifierConfig
	PID              string
	NotifyURL        string
	RedirectURL      string
	GatewayOrigin    string
	MaxQuoteLifetime time.Duration
	Quarantine       time.Duration
}

type OrderStore struct {
	pool   *pgxpool.Pool
	config OrderStoreConfig
}

func depositURL(raw string, optional bool) bool {
	if raw == "" {
		return optional
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func NewOrderStore(pool *pgxpool.Pool, config OrderStoreConfig) (*OrderStore, error) {
	if config.MaxOrderUnits < 0 || config.MaxOrderUnits > int64(maxPaymentUnits) {
		return nil, domain.ErrInvalid
	}
	chain, err := NewTronScanStore(pool, config.Chain, "deposit-config-validation")
	origin, urlErr := url.Parse(config.GatewayOrigin)
	if err != nil || urlErr != nil || !depositURL(config.NotifyURL, false) || !depositURL(config.RedirectURL, true) || !depositURL(config.GatewayOrigin, false) || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || config.PID == "" || len(config.PID) > 128 || strings.ContainsAny(config.PID, "&=\r\n") || config.MaxQuoteLifetime < time.Minute || config.MaxQuoteLifetime > time.Hour || config.MaxQuoteLifetime%time.Second != 0 || config.Quarantine < time.Hour || config.Quarantine > 7*24*time.Hour || config.Quarantine%time.Second != 0 {
		return nil, domain.ErrInvalid
	}
	config.Chain = chain.config
	config.GatewayOrigin = origin.Scheme + "://" + origin.Host
	return &OrderStore{pool: pool, config: config}, nil
}

func depositUnits(raw string) (int64, error) {
	if !depositAmountPattern.MatchString(raw) {
		return 0, domain.ErrInvalid
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, domain.ErrInvalid
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	scaled, err := strconv.ParseInt(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	units := whole*1000000 + scaled
	if err != nil || units <= 10000 || units > int64(maxPaymentUnits) {
		return 0, domain.ErrInvalid
	}
	return units, nil
}

func depositAmount(units int64) string {
	whole := strconv.FormatInt(units/1000000, 10)
	fraction := strconv.FormatInt(units%1000000+1000000, 10)[1:]
	fraction = strings.TrimRight(fraction, "0")
	if fraction != "" {
		return whole + "." + fraction
	}
	return whole
}

func optionalDepositUUID(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(value), Valid: value != uuid.Nil}
}

func rollbackDeposit(transaction pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = transaction.Rollback(ctx)
}

func authorizeDeposit(ctx context.Context, queries *db.Queries, principal userauth.Principal) error {
	if principal.User.ID == uuid.Nil || principal.SessionID == uuid.Nil {
		return domain.ErrUnauthorized
	}
	profile, err := queries.LockUser(ctx, principal.User.ID)
	if err != nil {
		return domain.DatabaseError(err)
	}
	credential, err := queries.LockUserCredential(ctx, profile.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if profile.Status != "ACTIVE" || credential.AuthVersion != principal.AuthVersion {
		return domain.ErrUnauthorized
	}
	if credential.MustChangePassword {
		return domain.ErrPasswordChangeRequired
	}
	session, err := queries.LockActiveUserSession(ctx, db.LockActiveUserSessionParams{ID: principal.SessionID, UserID: profile.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrUnauthorized
		}
		return err
	}
	if session.AuthVersion != credential.AuthVersion {
		return domain.ErrUnauthorized
	}
	return nil
}

func depositEvent(ctx context.Context, queries *db.Queries, orderID, inboxID uuid.UUID, actor string, actorID uuid.UUID, action string, facts any) error {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	return queries.CreateDepositEvent(ctx, db.CreateDepositEventParams{ID: uuid.New(), OrderID: optionalDepositUUID(orderID), InboxID: optionalDepositUUID(inboxID), Actor: actor, ActorID: optionalDepositUUID(actorID), Action: action, Facts: encoded})
}

func (store *OrderStore) Prepare(ctx context.Context, principal userauth.Principal, key, amount string) (db.DepositOrder, error) {
	return store.prepare(ctx, principal, key, amount, 0)
}

func (store *OrderStore) prepare(ctx context.Context, principal userauth.Principal, key, amount string, capacity int64) (db.DepositOrder, error) {
	if !store.config.Enabled {
		return db.DepositOrder{}, ErrDepositsDisabled
	}
	parsedKey, keyErr := uuid.Parse(key)
	units, err := depositUnits(amount)
	if err != nil || keyErr != nil || parsedKey == uuid.Nil {
		return db.DepositOrder{}, domain.ErrInvalid
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	if err := authorizeDeposit(ctx, queries, principal); err != nil {
		return db.DepositOrder{}, err
	}
	previous, err := queries.GetDepositByKey(ctx, db.GetDepositByKeyParams{UserID: principal.User.ID, IdempotencyKey: parsedKey})
	if err == nil {
		if previous.RequestedUnits != units {
			return db.DepositOrder{}, domain.ErrIdempotencyConflict
		}
		return previous, transaction.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.DepositOrder{}, err
	}
	// Preserve original idempotent reads even if a later test limit is lower.
	if store.config.MaxOrderUnits > 0 && units > store.config.MaxOrderUnits {
		return db.DepositOrder{}, domain.ErrInvalid
	}
	if capacity > 0 {
		count, countErr := queries.CountUserOpenDeposits(ctx, principal.User.ID)
		if countErr != nil {
			return db.DepositOrder{}, countErr
		}
		if count >= capacity {
			return db.DepositOrder{}, ErrDepositCapacity
		}
	}
	config := store.config
	order, err := queries.CreateDeposit(ctx, db.CreateDepositParams{ID: uuid.New(), UserID: principal.User.ID, AuthVersion: principal.AuthVersion, IdempotencyKey: parsedKey, RequestedUnits: units, ChainEnvironment: config.Chain.Environment, GenesisID: config.Chain.GenesisID, Contract: config.Chain.Contract, MerchantPid: config.PID, GatewayOrderID: strings.ReplaceAll(uuid.NewString(), "-", ""), NotifyUrl: config.NotifyURL, RedirectUrl: config.RedirectURL, GatewayOrigin: config.GatewayOrigin, MaxQuoteSeconds: int32(config.MaxQuoteLifetime / time.Second), QuarantineSeconds: int32(config.Quarantine / time.Second)})
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	request := createOrderRequest(order)
	payload, _ := json.Marshal(request)
	if err := queries.CreateDepositJob(ctx, db.CreateDepositJobParams{ID: uuid.New(), Kind: "CREATE", DedupeKey: order.ID.String(), OrderID: optionalDepositUUID(order.ID), Payload: payload}); err != nil {
		return db.DepositOrder{}, err
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "USER", principal.User.ID, "ORDER_PREPARED", map[string]any{"requested_units": units}); err != nil {
		return db.DepositOrder{}, err
	}
	return order, transaction.Commit(ctx)
}

func (store *OrderStore) Get(ctx context.Context, principal userauth.Principal, orderID uuid.UUID) (db.DepositOrder, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	if err := authorizeDeposit(ctx, queries, principal); err != nil {
		return db.DepositOrder{}, err
	}
	order, err := queries.GetUserDeposit(ctx, db.GetUserDepositParams{ID: orderID, UserID: principal.User.ID})
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	return order, transaction.Commit(ctx)
}

func (store *OrderStore) StartCreation(ctx context.Context, orderID uuid.UUID) (db.DepositOrder, error) {
	return store.startCreation(ctx, orderID, nil)
}

func (store *OrderStore) startCreation(ctx context.Context, orderID uuid.UUID, validate func(context.Context, *db.Queries, db.DepositOrder) error) (db.DepositOrder, error) {
	if !store.config.Enabled {
		return db.DepositOrder{}, ErrDepositsDisabled
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	order, err := queries.LockDeposit(ctx, orderID)
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	if err := lockCreationScope(ctx, queries, order); err != nil {
		return db.DepositOrder{}, err
	}
	user, err := queries.LockUser(ctx, order.UserID)
	if err != nil {
		return db.DepositOrder{}, err
	}
	credential, err := queries.LockUserCredential(ctx, order.UserID)
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	if user.Status != "ACTIVE" || credential.AuthVersion != order.AuthVersion || credential.MustChangePassword {
		return db.DepositOrder{}, domain.ErrUnauthorized
	}
	if order.Status != "CREATING" || order.CreateToken.Valid {
		return db.DepositOrder{}, domain.ErrInvalidState
	}
	if validate != nil {
		if err := validate(ctx, queries, order); err != nil {
			return db.DepositOrder{}, err
		}
	}
	order, err = queries.StartDepositCreate(ctx, db.StartDepositCreateParams{ID: order.ID, CreateToken: optionalDepositUUID(uuid.New())})
	if err != nil {
		return db.DepositOrder{}, err
	}
	affected, err := queries.SetDepositCreateJobState(ctx, db.SetDepositCreateJobStateParams{OrderID: optionalDepositUUID(order.ID), Status: "IN_FLIGHT", AttemptDelta: 1})
	if err != nil {
		return db.DepositOrder{}, err
	}
	if affected != 1 {
		return db.DepositOrder{}, domain.ErrInvalidState
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, "CREATE_STARTED", map[string]string{"gateway_order_id": order.GatewayOrderID}); err != nil {
		return db.DepositOrder{}, err
	}
	return order, transaction.Commit(ctx)
}

func (store *OrderStore) CreateFailed(ctx context.Context, orderID, token uuid.UUID, ambiguous bool) (db.DepositOrder, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	order, err := queries.LockDeposit(ctx, orderID)
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	if err := lockCreationScope(ctx, queries, order); err != nil {
		return db.DepositOrder{}, err
	}
	state, jobState := "FAILED", "DONE"
	if ambiguous {
		state, jobState = "CREATE_UNKNOWN", "MANUAL_REVIEW"
	}
	if token == uuid.Nil || !order.CreateToken.Valid || uuid.UUID(order.CreateToken.Bytes) != token || order.TradeID.Valid || order.Status != "CREATING" && order.Status != state {
		return db.DepositOrder{}, domain.ErrInvalidState
	}
	if order.Status == state {
		return order, transaction.Commit(ctx)
	}
	order, err = queries.SetDepositState(ctx, db.SetDepositStateParams{ID: order.ID, Status: state, ReviewReason: ""})
	if err != nil {
		return db.DepositOrder{}, err
	}
	if _, err := queries.SetDepositCreateJobState(ctx, db.SetDepositCreateJobStateParams{OrderID: optionalDepositUUID(order.ID), Status: jobState}); err != nil {
		return db.DepositOrder{}, err
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, state, map[string]bool{"ambiguous": ambiguous}); err != nil {
		return db.DepositOrder{}, err
	}
	return order, transaction.Commit(ctx)
}

func normalizeDepositQuote(order db.DepositOrder, quote epusdt.Transaction) (epusdt.Transaction, string, int64, time.Time, error) {
	units, amountErr := depositUnits(quote.Amount.String())
	actual, actualErr := depositUnits(quote.ActualAmount.String())
	address, addressErr := canonicalTronAddress(quote.ReceiveAddress)
	payment, paymentErr := url.Parse(quote.PaymentURL)
	origin, originErr := url.Parse(order.GatewayOrigin)
	expires := time.Unix(quote.ExpirationTime, 0).UTC()
	if amountErr != nil || actualErr != nil || addressErr != nil || paymentErr != nil || originErr != nil || !order.CreateStartedAt.Valid || len(quote.ReceiveAddress) != 34 || quote.ReceiveAddress[0] != 'T' || address == "41"+strings.Repeat("0", 40) || quote.OrderID != order.GatewayOrderID || !depositTradePattern.MatchString(quote.TradeID) || quote.Status != 1 || !strings.EqualFold(quote.Token, "USDT") || !strings.EqualFold(quote.Currency, "USDT") || units != order.RequestedUnits || actual < units || actual-units > 99 || payment.Scheme != origin.Scheme || payment.Host != origin.Host || payment.User != nil || payment.Fragment != "" || !strings.HasPrefix(payment.Path, "/pay/checkout-counter/") || !expires.After(order.CreateStartedAt.Time) || expires.After(order.CreateStartedAt.Time.Add(time.Duration(order.MaxQuoteSeconds)*time.Second)) {
		return epusdt.Transaction{}, "", 0, time.Time{}, epusdt.ErrQuoteMismatch
	}
	quote.Amount, quote.ActualAmount = json.Number(depositAmount(units)), json.Number(depositAmount(actual))
	quote.Token, quote.Currency = "USDT", "USDT"
	return quote, address, actual, expires, nil
}

func lockReservation(ctx context.Context, queries *db.Queries, order db.DepositOrder) (int64, error) {
	key := strings.Join([]string{order.ChainEnvironment, order.GenesisID, order.Network, order.Contract, order.ReceiveAddress.String, strconv.FormatInt(order.ActualUnits.Int64, 10)}, "/")
	if err := queries.LockDepositReservation(ctx, key); err != nil {
		return 0, err
	}
	return queries.CountOverlappingDeposits(ctx, db.CountOverlappingDepositsParams{ID: order.ID, ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, Contract: order.Contract, ReceiveAddress: order.ReceiveAddress, ActualUnits: order.ActualUnits, ReservedUntil: order.ReservedUntil, StartedAt: order.CreateStartedAt})
}

func lockCreationScope(ctx context.Context, queries *db.Queries, order db.DepositOrder) error {
	return queries.LockDepositReservation(ctx, strings.Join([]string{"deposit-create-scope", order.ChainEnvironment, order.GenesisID, order.Network, order.Contract}, "/"))
}

func (store *OrderStore) BindQuote(ctx context.Context, orderID, token uuid.UUID, quote epusdt.Transaction) (db.DepositOrder, error) {
	return store.bindQuote(ctx, orderID, token, quote, nil)
}

func (store *OrderStore) bindQuote(ctx context.Context, orderID, token uuid.UUID, quote epusdt.Transaction, authorize func(context.Context, *db.Queries, db.DepositOrder) error) (db.DepositOrder, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	order, err := queries.LockDeposit(ctx, orderID)
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	if err := lockCreationScope(ctx, queries, order); err != nil {
		return db.DepositOrder{}, err
	}
	if token == uuid.Nil || !order.CreateToken.Valid || uuid.UUID(order.CreateToken.Bytes) != token {
		return db.DepositOrder{}, domain.ErrInvalidState
	}
	if authorize != nil {
		if err := authorize(ctx, queries, order); err != nil {
			return db.DepositOrder{}, err
		}
	}
	quote, address, actual, expires, err := normalizeDepositQuote(order, quote)
	if err != nil {
		return db.DepositOrder{}, err
	}
	encoded, _ := json.Marshal(quote)
	if order.TradeID.Valid {
		var previous epusdt.Transaction
		if json.Unmarshal(order.QuoteJson, &previous) != nil {
			return db.DepositOrder{}, ErrInvalidChainEvidence
		}
		saved, _ := json.Marshal(previous)
		if !bytes.Equal(saved, encoded) {
			return db.DepositOrder{}, domain.ErrConflict
		}
		return order, transaction.Commit(ctx)
	}
	if order.Status != "CREATING" && order.Status != "CREATE_UNKNOWN" {
		return db.DepositOrder{}, domain.ErrInvalidState
	}
	order.ActualUnits = pgtype.Int8{Int64: actual, Valid: true}
	order.ReceiveAddress = pgtype.Text{String: address, Valid: true}
	order.ReservedUntil = pgtype.Timestamptz{Time: expires.Add(time.Duration(order.QuarantineSeconds) * time.Second), Valid: true}
	overlaps, err := lockReservation(ctx, queries, order)
	if err != nil {
		return db.DepositOrder{}, err
	}
	state, reason := "WAITING", ""
	if !expires.After(time.Now()) {
		state = "EXPIRED"
	}
	if overlaps != 0 {
		state, reason = "REVIEW_REQUIRED", "RESERVATION_OVERLAP"
	}
	order, err = queries.SaveDepositQuote(ctx, db.SaveDepositQuoteParams{ID: order.ID, TradeID: pgtype.Text{String: quote.TradeID, Valid: true}, ActualUnits: order.ActualUnits, ReceiveAddress: order.ReceiveAddress, QuoteJson: encoded, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}, ReservedUntil: order.ReservedUntil, Status: state, ReviewReason: reason})
	if err != nil {
		return db.DepositOrder{}, domain.DatabaseError(err)
	}
	if _, err := queries.SetDepositCreateJobState(ctx, db.SetDepositCreateJobStateParams{OrderID: optionalDepositUUID(order.ID), Status: "DONE"}); err != nil {
		return db.DepositOrder{}, err
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, "QUOTE_BOUND", map[string]any{"actual_units": actual, "status": state, "reason": reason}); err != nil {
		return db.DepositOrder{}, err
	}
	return order, transaction.Commit(ctx)
}
