package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Pohi11/price-hunter/internal/domain"
)

func newCheckItem(c domain.Check) checkItem {
	return checkItem{
		PK: productPK(c.ProductID), SK: checkSK(c.ID), Entity: EntityCheck,
		CheckID: c.ID, ProductID: c.ProductID, UserID: c.UserID,
		Status: string(c.Status), Trigger: string(c.Trigger), QueuedAt: ts(c.QueuedAt),
		TTL: ttlAt(c.QueuedAt.Add(checkTTL)),
	}
}

// BeginCheck is the worker's idempotency guard. It moves the check to
// RUNNING if it is new, QUEUED, or RUNNING but abandoned (started before
// now-staleAfter, i.e. a crashed worker). It returns false when another
// delivery of the same message already ran or is running it.
func (s *Store) BeginCheck(ctx context.Context, msg domain.CheckMessage, now time.Time, staleAfter time.Duration) (bool, error) {
	enq := msg.EnqueuedAt
	if enq.IsZero() {
		enq = now
	}
	upd := expression.Set(expression.Name("entity"), expression.Value(EntityCheck)).
		Set(expression.Name("check_id"), expression.Value(msg.CheckID)).
		Set(expression.Name("product_id"), expression.Value(msg.ProductID)).
		Set(expression.Name("user_id"), expression.Value(msg.UserID)).
		Set(expression.Name("status"), expression.Value(string(domain.CheckRunning))).
		Set(expression.Name("trigger"), expression.Value(string(msg.Trigger))).
		Set(expression.Name("queued_at"), expression.IfNotExists(expression.Name("queued_at"), expression.Value(ts(enq)))).
		Set(expression.Name("started_at"), expression.Value(ts(now))).
		Set(expression.Name("ttl"), expression.IfNotExists(expression.Name("ttl"), expression.Value(ttlAt(now.Add(checkTTL)))))
	cond := expression.AttributeNotExists(expression.Name("PK")).
		Or(expression.Name("status").Equal(expression.Value(string(domain.CheckQueued)))).
		Or(expression.Name("status").Equal(expression.Value(string(domain.CheckRunning))).
			And(expression.Name("started_at").LessThan(expression.Value(ts(now.Add(-staleAfter))))))
	expr, err := expression.NewBuilder().WithUpdate(upd).WithCondition(cond).Build()
	if err != nil {
		return false, err
	}
	_, err = s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: key(productPK(msg.ProductID), checkSK(msg.CheckID)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		if isConditionFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("begin check: %w", err)
	}
	return true, nil
}

// CheckCommit is everything a finished check writes, in one transaction.
type CheckCommit struct {
	Check           domain.Check         // final status/outcome; must currently be RUNNING
	Product         *domain.Product      // new tracking state; nil leaves the product untouched
	ExpectedVersion int                  // product version read at the start of the check
	Point           *domain.PricePoint   // optional price observation
	Notification    *domain.Notification // optional outbox entry (an alert)
}

// CommitCheck atomically finishes a check. The transaction is the commit
// point of the whole pipeline:
//   - the check must still be RUNNING (else ErrCheckNotRunning: a duplicate won)
//   - the product must still be at ExpectedVersion (else ErrVersionConflict:
//     the user edited it mid-check, so re-evaluate against the new settings)
//   - the notification is written in the same transaction as the alert
//     state change (transactional outbox, ADR-004)
func (s *Store) CommitCheck(ctx context.Context, c CheckCommit) error {
	items := make([]types.TransactWriteItem, 0, 4)

	chkUpd, err := checkFinishUpdate(c.Check)
	if err != nil {
		return err
	}
	chkUpd.TableName = &s.table
	items = append(items, types.TransactWriteItem{Update: chkUpd})

	if c.Product != nil {
		pu, err := productTrackingUpdate(*c.Product, c.ExpectedVersion)
		if err != nil {
			return err
		}
		pu.TableName = &s.table
		items = append(items, types.TransactWriteItem{Update: pu})
	}
	if c.Point != nil {
		item, err := attributevalue.MarshalMap(toPricePointItem(*c.Point))
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName: &s.table, Item: item, ConditionExpression: aws.String("attribute_not_exists(PK)"),
		}})
	}
	if c.Notification != nil {
		item, err := attributevalue.MarshalMap(toNotificationItem(*c.Notification))
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName: &s.table, Item: item, ConditionExpression: aws.String("attribute_not_exists(PK)"),
		}})
	}

	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	if reasons, ok := cancellationReasons(err); ok {
		if len(reasons) > 0 && reasons[0] == "ConditionalCheckFailed" {
			return ErrCheckNotRunning
		}
		if c.Product != nil && len(reasons) > 1 && reasons[1] == "ConditionalCheckFailed" {
			if _, gerr := s.GetProduct(ctx, c.Product.UserID, c.Product.ID); errors.Is(gerr, domain.ErrNotFound) {
				return domain.ErrNotFound
			}
			return domain.ErrVersionConflict
		}
	}
	return fmt.Errorf("commit check: %w", err)
}

func checkFinishUpdate(c domain.Check) (*types.Update, error) {
	finished := time.Now().UTC()
	if c.FinishedAt != nil {
		finished = *c.FinishedAt
	}
	upd := expression.Set(expression.Name("status"), expression.Value(string(c.Status))).
		Set(expression.Name("finished_at"), expression.Value(ts(finished))).
		Set(expression.Name("duration_ms"), expression.Value(c.DurationMS)).
		Set(expression.Name("outcome"), expression.Value(string(c.Outcome))).
		Set(expression.Name("alerted"), expression.Value(c.Alerted))
	if c.Message != "" {
		upd = upd.Set(expression.Name("message"), expression.Value(truncateMsg(c.Message)))
	}
	if c.Price != nil {
		upd = upd.Set(expression.Name("price_minor"), expression.Value(c.Price.Minor)).
			Set(expression.Name("currency"), expression.Value(string(c.Price.Currency)))
	}
	if c.Strategy != "" {
		upd = upd.Set(expression.Name("strategy"), expression.Value(c.Strategy))
	}
	cond := expression.Name("status").Equal(expression.Value(string(domain.CheckRunning)))
	expr, err := expression.NewBuilder().WithUpdate(upd).WithCondition(cond).Build()
	if err != nil {
		return nil, err
	}
	return &types.Update{
		Key: key(productPK(c.ProductID), checkSK(c.ID)), UpdateExpression: expr.Update(),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// productTrackingUpdate writes only the fields the checker owns. It does not
// bump version (version tracks user edits, for If-Match) but requires it.
func productTrackingUpdate(p domain.Product, expectedVersion int) (*types.Update, error) {
	setOrRemove := func(u expression.UpdateBuilder, name string, v *int64) expression.UpdateBuilder {
		if v == nil {
			return u.Remove(expression.Name(name))
		}
		return u.Set(expression.Name(name), expression.Value(*v))
	}
	setOrRemoveTS := func(u expression.UpdateBuilder, name string, t *time.Time) expression.UpdateBuilder {
		if t == nil {
			return u.Remove(expression.Name(name))
		}
		return u.Set(expression.Name(name), expression.Value(ts(*t)))
	}
	upd := expression.Set(expression.Name("status"), expression.Value(string(p.Status))).
		Set(expression.Name("alert_state"), expression.Value(string(p.AlertState))).
		Set(expression.Name("consecutive_failures"), expression.Value(p.ConsecutiveFailures)).
		Set(expression.Name("updated_at"), expression.Value(ts(orNow(p.UpdatedAt))))
	if p.Availability != "" {
		upd = upd.Set(expression.Name("availability"), expression.Value(string(p.Availability)))
	}
	if p.LastErrorCode != "" {
		upd = upd.Set(expression.Name("last_error_code"), expression.Value(string(p.LastErrorCode)))
	} else {
		upd = upd.Remove(expression.Name("last_error_code"))
	}
	upd = setOrRemove(upd, "current_minor", minorPtr(p.Current))
	upd = setOrRemove(upd, "lowest_minor", minorPtr(p.Lowest))
	upd = setOrRemove(upd, "pending_suspect_minor", minorPtr(p.PendingSuspect))
	upd = setOrRemoveTS(upd, "last_alert_at", p.LastAlertAt)
	upd = setOrRemoveTS(upd, "last_checked_at", p.LastCheckedAt)
	upd = setOrRemoveTS(upd, "last_success_at", p.LastSuccessAt)
	upd = scheduleUpdate(upd, p.ID, p.NextCheckAt, p.Status)

	cond := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("version").Equal(expression.Value(expectedVersion)))
	expr, err := expression.NewBuilder().WithUpdate(upd).WithCondition(cond).Build()
	if err != nil {
		return nil, err
	}
	return &types.Update{
		Key: key(userPK(p.UserID), productSK(p.ID)), UpdateExpression: expr.Update(),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// FinishCheckOnly marks a RUNNING check finished without touching the
// product (e.g. the product was deleted mid-check).
func (s *Store) FinishCheckOnly(ctx context.Context, c domain.Check) error {
	err := s.CommitCheck(ctx, CheckCommit{Check: c})
	if errors.Is(err, ErrCheckNotRunning) {
		return nil
	}
	return err
}

func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func truncateMsg(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// GetCheck returns one check.
func (s *Store) GetCheck(ctx context.Context, productID, checkID string) (domain.Check, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: key(productPK(productID), checkSK(checkID))})
	if err != nil {
		return domain.Check{}, fmt.Errorf("get check: %w", err)
	}
	if out.Item == nil {
		return domain.Check{}, domain.ErrNotFound
	}
	var it checkItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return domain.Check{}, err
	}
	return it.toDomain(), nil
}

// ListChecks returns a product's most recent checks, newest first. Check IDs
// are ULIDs, so sort order is creation order.
func (s *Store) ListChecks(ctx context.Context, productID string, limit int) ([]domain.Check, error) {
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": strAttr(productPK(productID)), ":prefix": strAttr("CHECK#"),
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list checks: %w", err)
	}
	var items []checkItem
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &items); err != nil {
		return nil, err
	}
	checks := make([]domain.Check, len(items))
	for i, it := range items {
		checks[i] = it.toDomain()
	}
	return checks, nil
}
