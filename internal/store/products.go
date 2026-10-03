package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// DuplicateError is returned when the user already tracks the same canonical URL.
type DuplicateError struct{ ExistingProductID string }

func (e *DuplicateError) Error() string { return "product already tracked: " + e.ExistingProductID }
func (e *DuplicateError) Unwrap() error { return domain.ErrDuplicate }

// CreateProduct atomically writes the product, its per-user URL uniqueness
// guard, the profile's product counter (enforcing maxProducts), and the
// first QUEUED check.
func (s *Store) CreateProduct(ctx context.Context, p domain.Product, first domain.Check, maxProducts int) error {
	prod, err := attributevalue.MarshalMap(toProductItem(p))
	if err != nil {
		return err
	}
	guard, err := attributevalue.MarshalMap(urlGuardItem{PK: userPK(p.UserID), SK: urlSK(p.URLHash), Entity: EntityURLGuard, ProductID: p.ID})
	if err != nil {
		return err
	}
	chk, err := attributevalue.MarshalMap(newCheckItem(first))
	if err != nil {
		return err
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: &s.table, Item: prod, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
		{Put: &types.Put{TableName: &s.table, Item: guard, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
		{Update: &types.Update{
			TableName:           &s.table,
			Key:                 key(userPK(p.UserID), profileSK),
			UpdateExpression:    aws.String("SET entity = :entity, user_id = :uid, notifications_enabled = if_not_exists(notifications_enabled, :true), created_at = if_not_exists(created_at, :now) ADD product_count :one"),
			ConditionExpression: aws.String("attribute_not_exists(product_count) OR product_count < :max"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":entity": strAttr(EntityProfile), ":uid": strAttr(p.UserID), ":now": strAttr(ts(p.CreatedAt)),
				":true": &types.AttributeValueMemberBOOL{Value: true},
				":one":  &types.AttributeValueMemberN{Value: "1"},
				":max":  &types.AttributeValueMemberN{Value: fmt.Sprint(maxProducts)},
			},
		}},
		{Put: &types.Put{TableName: &s.table, Item: chk, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
	}})
	if err == nil {
		return nil
	}
	if reasons, ok := cancellationReasons(err); ok {
		switch {
		case len(reasons) > 1 && reasons[1] == "ConditionalCheckFailed":
			existing, _ := s.productIDForURL(ctx, p.UserID, p.URLHash)
			return &DuplicateError{ExistingProductID: existing}
		case len(reasons) > 2 && reasons[2] == "ConditionalCheckFailed":
			return domain.ErrQuotaExceeded
		}
	}
	return fmt.Errorf("create product: %w", err)
}

func (s *Store) productIDForURL(ctx context.Context, userID, hash string) (string, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: key(userPK(userID), urlSK(hash))})
	if err != nil || out.Item == nil {
		return "", err
	}
	var it urlGuardItem
	err = attributevalue.UnmarshalMap(out.Item, &it)
	return it.ProductID, err
}

// GetProduct returns domain.ErrNotFound if the product does not exist for this user.
func (s *Store) GetProduct(ctx context.Context, userID, productID string) (domain.Product, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table, Key: key(userPK(userID), productSK(productID)), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return domain.Product{}, fmt.Errorf("get product: %w", err)
	}
	if out.Item == nil {
		return domain.Product{}, domain.ErrNotFound
	}
	var it productItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return domain.Product{}, err
	}
	return it.toDomain(), nil
}

// ListProducts pages through a user's products in creation order.
func (s *Store) ListProducts(ctx context.Context, userID string, limit int, cur string) ([]domain.Product, string, error) {
	start, err := decodeCursor(cur, userPK(userID), "PRODUCT#")
	if err != nil {
		return nil, "", err
	}
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": strAttr(userPK(userID)), ":prefix": strAttr("PRODUCT#"),
		},
		Limit:             pageSize(limit),
		ExclusiveStartKey: start,
	})
	if err != nil {
		return nil, "", fmt.Errorf("list products: %w", err)
	}
	var items []productItem
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &items); err != nil {
		return nil, "", err
	}
	products := make([]domain.Product, len(items))
	for i, it := range items {
		products[i] = it.toDomain()
	}
	return products, encodeCursor(out.LastEvaluatedKey), nil
}

// ProductChanges are user-initiated edits. Only non-nil fields are written,
// so concurrent worker updates to other fields are never clobbered.
type ProductChanges struct {
	Name       *string
	Target     *domain.Money
	Frequency  *domain.Frequency
	Status     *domain.ProductStatus
	AlertState *domain.AlertState
	// Reschedule writes NextCheckAt (nil removes it) and sets or removes the
	// GSI1 "due" attributes according to ResultingStatus.
	Reschedule      bool
	NextCheckAt     *time.Time
	ResultingStatus domain.ProductStatus
}

// UpdateProduct applies user edits if the product is still at expectedVersion,
// and increments the version. Returns the updated product.
func (s *Store) UpdateProduct(ctx context.Context, userID, productID string, expectedVersion int, ch ProductChanges, now time.Time) (domain.Product, error) {
	upd := expression.Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1))).
		Set(expression.Name("updated_at"), expression.Value(ts(now)))
	if ch.Name != nil {
		upd = upd.Set(expression.Name("name"), expression.Value(*ch.Name))
	}
	if ch.Target != nil {
		upd = upd.Set(expression.Name("target_minor"), expression.Value(ch.Target.Minor)).
			Set(expression.Name("currency"), expression.Value(string(ch.Target.Currency)))
	}
	if ch.Frequency != nil {
		upd = upd.Set(expression.Name("frequency_min"), expression.Value(int(ch.Frequency.Duration()/time.Minute)))
	}
	if ch.Status != nil {
		upd = upd.Set(expression.Name("status"), expression.Value(string(*ch.Status)))
	}
	if ch.AlertState != nil {
		upd = upd.Set(expression.Name("alert_state"), expression.Value(string(*ch.AlertState)))
	}
	if ch.Reschedule {
		upd = scheduleUpdate(upd, productID, ch.NextCheckAt, ch.ResultingStatus)
	}
	cond := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("version").Equal(expression.Value(expectedVersion)))
	expr, err := expression.NewBuilder().WithUpdate(upd).WithCondition(cond).Build()
	if err != nil {
		return domain.Product{}, err
	}
	out, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: key(userPK(userID), productSK(productID)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionFailed(err) {
			if _, gerr := s.GetProduct(ctx, userID, productID); errors.Is(gerr, domain.ErrNotFound) {
				return domain.Product{}, domain.ErrNotFound
			}
			return domain.Product{}, domain.ErrVersionConflict
		}
		return domain.Product{}, fmt.Errorf("update product: %w", err)
	}
	var it productItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &it); err != nil {
		return domain.Product{}, err
	}
	return it.toDomain(), nil
}

// scheduleUpdate sets next_check_at and the sparse GSI1 attributes together,
// so a product is in the "due" index exactly when it is schedulable.
func scheduleUpdate(upd expression.UpdateBuilder, productID string, next *time.Time, status domain.ProductStatus) expression.UpdateBuilder {
	if next == nil {
		return upd.Remove(expression.Name("next_check_at")).Remove(expression.Name("GSI1PK")).Remove(expression.Name("GSI1SK"))
	}
	upd = upd.Set(expression.Name("next_check_at"), expression.Value(ts(*next)))
	if status.Schedulable() {
		return upd.Set(expression.Name("GSI1PK"), expression.Value(dueShardPK(ShardFor(productID)))).
			Set(expression.Name("GSI1SK"), expression.Value(ts(*next)))
	}
	return upd.Remove(expression.Name("GSI1PK")).Remove(expression.Name("GSI1SK"))
}

// DeleteProduct removes the product, its URL guard and decrements the
// profile counter. Price history and checks under PRODUCT#<id> are purged
// separately (PurgeProductData), asynchronously in AWS via DynamoDB Streams.
func (s *Store) DeleteProduct(ctx context.Context, userID, productID string) (domain.Product, error) {
	p, err := s.GetProduct(ctx, userID, productID)
	if err != nil {
		return domain.Product{}, err
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: &s.table, Key: key(userPK(userID), productSK(productID)), ConditionExpression: aws.String("attribute_exists(PK)")}},
		{Delete: &types.Delete{TableName: &s.table, Key: key(userPK(userID), urlSK(p.URLHash))}},
		{Update: &types.Update{
			TableName: &s.table, Key: key(userPK(userID), profileSK),
			UpdateExpression:          aws.String("ADD product_count :minus"),
			ConditionExpression:       aws.String("attribute_exists(PK)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":minus": &types.AttributeValueMemberN{Value: "-1"}},
		}},
	}})
	if err != nil {
		if reasons, ok := cancellationReasons(err); ok && len(reasons) > 0 && reasons[0] == "ConditionalCheckFailed" {
			return domain.Product{}, domain.ErrNotFound
		}
		return domain.Product{}, fmt.Errorf("delete product: %w", err)
	}
	return p, nil
}

// RequestManualCheck records a QUEUED manual check if the per-product
// cooldown has elapsed, atomically with stamping last_manual_check_at.
func (s *Store) RequestManualCheck(ctx context.Context, userID, productID string, c domain.Check, cooldown time.Duration) error {
	chk, err := attributevalue.MarshalMap(newCheckItem(c))
	if err != nil {
		return err
	}
	now := c.QueuedAt
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: &s.table, Key: key(userPK(userID), productSK(productID)),
			UpdateExpression:    aws.String("SET last_manual_check_at = :now"),
			ConditionExpression: aws.String("attribute_exists(PK) AND (attribute_not_exists(last_manual_check_at) OR last_manual_check_at < :cutoff)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":now": strAttr(ts(now)), ":cutoff": strAttr(ts(now.Add(-cooldown))),
			},
		}},
		{Put: &types.Put{TableName: &s.table, Item: chk, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
	}})
	if err == nil {
		return nil
	}
	if reasons, ok := cancellationReasons(err); ok && len(reasons) > 0 && reasons[0] == "ConditionalCheckFailed" {
		p, gerr := s.GetProduct(ctx, userID, productID)
		if gerr != nil {
			return gerr
		}
		wait := cooldown
		if p.LastManualCheckAt != nil {
			wait = p.LastManualCheckAt.Add(cooldown).Sub(now)
		}
		return &domain.CooldownError{RetryAfterSeconds: max(1, int(wait.Seconds()+0.999))}
	}
	return fmt.Errorf("request manual check: %w", err)
}

// GetProfile returns the user's profile, or defaults if none exists yet.
func (s *Store) GetProfile(ctx context.Context, userID string) (domain.Profile, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: key(userPK(userID), profileSK)})
	if err != nil {
		return domain.Profile{}, fmt.Errorf("get profile: %w", err)
	}
	if out.Item == nil {
		return domain.Profile{UserID: userID, NotificationsEnabled: true}, nil
	}
	var it profileItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return domain.Profile{}, err
	}
	return it.toDomain(), nil
}

// UpsertProfile records the user's (verified) email, creating the profile if needed.
func (s *Store) UpsertProfile(ctx context.Context, userID, email string, now time.Time) (domain.Profile, error) {
	out, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &s.table,
		Key:              key(userPK(userID), profileSK),
		UpdateExpression: aws.String("SET entity = :entity, user_id = :uid, email = :email, created_at = if_not_exists(created_at, :now), notifications_enabled = if_not_exists(notifications_enabled, :true), product_count = if_not_exists(product_count, :zero)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":entity": strAttr(EntityProfile), ":uid": strAttr(userID), ":email": strAttr(strings.TrimSpace(email)),
			":now": strAttr(ts(now)), ":true": &types.AttributeValueMemberBOOL{Value: true},
			":zero": &types.AttributeValueMemberN{Value: "0"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return domain.Profile{}, fmt.Errorf("upsert profile: %w", err)
	}
	var it profileItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &it); err != nil {
		return domain.Profile{}, err
	}
	return it.toDomain(), nil
}

// SetNotificationsEnabled toggles alert emails for a user.
func (s *Store) SetNotificationsEnabled(ctx context.Context, userID string, enabled bool) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &s.table,
		Key:              key(userPK(userID), profileSK),
		UpdateExpression: aws.String("SET entity = :entity, user_id = :uid, notifications_enabled = :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":entity": strAttr(EntityProfile), ":uid": strAttr(userID), ":v": &types.AttributeValueMemberBOOL{Value: enabled},
		},
	})
	if err != nil {
		return fmt.Errorf("set notifications: %w", err)
	}
	return nil
}
