package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// PriceQuery selects a product's price history.
type PriceQuery struct {
	ProductID   string
	From, To    time.Time
	Limit       int
	Cursor      string
	NewestFirst bool
}

// ListPrices queries PK=PRODUCT#id with SK BETWEEN PRICE#from AND PRICE#to:
// one partition, contiguous sort-key range, no filtering.
func (s *Store) ListPrices(ctx context.Context, q PriceQuery) ([]domain.PricePoint, string, error) {
	pk := productPK(q.ProductID)
	start, err := decodeCursor(q.Cursor, pk, "PRICE#")
	if err != nil {
		return nil, "", err
	}
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("PK = :pk AND SK BETWEEN :from AND :to"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": strAttr(pk), ":from": strAttr(priceSK(q.From)), ":to": strAttr(priceSK(q.To)),
		},
		ScanIndexForward:  aws.Bool(!q.NewestFirst),
		Limit:             pageSize(q.Limit),
		ExclusiveStartKey: start,
	})
	if err != nil {
		return nil, "", fmt.Errorf("list prices: %w", err)
	}
	var items []pricePointItem
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &items); err != nil {
		return nil, "", err
	}
	points := make([]domain.PricePoint, len(items))
	for i, it := range items {
		points[i] = it.toDomain()
	}
	return points, encodeCursor(out.LastEvaluatedKey), nil
}

// PutPricePoints writes historical points directly (used by the demo seeder).
func (s *Store) PutPricePoints(ctx context.Context, points []domain.PricePoint) error {
	for start := 0; start < len(points); start += 25 {
		end := min(start+25, len(points))
		reqs := make([]types.WriteRequest, 0, end-start)
		for _, p := range points[start:end] {
			item, err := attributevalue.MarshalMap(toPricePointItem(p))
			if err != nil {
				return err
			}
			reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
		}
		if err := s.batchWrite(ctx, reqs); err != nil {
			return err
		}
	}
	return nil
}

// PurgeProductData deletes every item in the PRODUCT#<id> partition (price
// history and checks). Idempotent; safe to retry.
func (s *Store) PurgeProductData(ctx context.Context, productID string) (int, error) {
	pk := productPK(productID)
	deleted := 0
	var start map[string]types.AttributeValue
	for {
		out, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:                 &s.table,
			KeyConditionExpression:    aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": strAttr(pk)},
			ProjectionExpression:      aws.String("PK, SK"),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return deleted, fmt.Errorf("purge query: %w", err)
		}
		for i := 0; i < len(out.Items); i += 25 {
			end := min(i+25, len(out.Items))
			reqs := make([]types.WriteRequest, 0, end-i)
			for _, it := range out.Items[i:end] {
				reqs = append(reqs, types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: map[string]types.AttributeValue{"PK": it["PK"], "SK": it["SK"]}}})
			}
			if err := s.batchWrite(ctx, reqs); err != nil {
				return deleted, err
			}
			deleted += len(reqs)
		}
		if out.LastEvaluatedKey == nil {
			return deleted, nil
		}
		start = out.LastEvaluatedKey
	}
}

// batchWrite retries unprocessed items with backoff.
func (s *Store) batchWrite(ctx context.Context, reqs []types.WriteRequest) error {
	pending := map[string][]types.WriteRequest{s.table: reqs}
	for attempt := 0; len(pending[s.table]) > 0; attempt++ {
		if attempt > 0 {
			if attempt > 8 {
				return fmt.Errorf("batch write: %d items unprocessed after retries", len(pending[s.table]))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(25<<attempt) * time.Millisecond):
			}
		}
		out, err := s.db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
		if err != nil {
			return fmt.Errorf("batch write: %w", err)
		}
		pending = out.UnprocessedItems
		if pending == nil {
			return nil
		}
	}
	return nil
}

// SetTrackingState writes a product's tracking fields directly. It exists
// for the demo seeder, which imports history without running checks.
func (s *Store) SetTrackingState(ctx context.Context, p domain.Product) error {
	upd, err := productTrackingUpdate(p, p.Version)
	if err != nil {
		return err
	}
	upd.TableName = &s.table
	_, err = s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: upd.TableName, Key: upd.Key, UpdateExpression: upd.UpdateExpression,
		ConditionExpression: upd.ConditionExpression, ExpressionAttributeNames: upd.ExpressionAttributeNames,
		ExpressionAttributeValues: upd.ExpressionAttributeValues,
	})
	if err != nil {
		return fmt.Errorf("set tracking state: %w", err)
	}
	return nil
}
