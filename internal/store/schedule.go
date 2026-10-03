package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DueItem identifies a product whose next_check_at has passed.
type DueItem struct {
	UserID    string
	ProductID string
	Seen      string    // raw GSI1SK value read; the lease is conditional on it
	DueAt     time.Time // parsed Seen
}

// QueryDue returns up to limit products in one shard of GSI1 whose
// next_check_at <= now, oldest first. GSI1 is sparse: paused and gone
// products have no GSI1 attributes and never appear.
func (s *Store) QueryDue(ctx context.Context, shard int, now time.Time, limit int) ([]DueItem, error) {
	var items []DueItem
	var start map[string]types.AttributeValue
	for len(items) < limit {
		out, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:              &s.table,
			IndexName:              aws.String(GSI1),
			KeyConditionExpression: aws.String("GSI1PK = :pk AND GSI1SK <= :now"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": strAttr(dueShardPK(shard)), ":now": strAttr(ts(now)),
			},
			Limit:             pageSize(limit - len(items)),
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, fmt.Errorf("query due shard %d: %w", shard, err)
		}
		for _, it := range out.Items {
			pk, _ := it["PK"].(*types.AttributeValueMemberS)
			sk, _ := it["SK"].(*types.AttributeValueMemberS)
			gsk, _ := it["GSI1SK"].(*types.AttributeValueMemberS)
			if pk == nil || sk == nil || gsk == nil || !strings.HasPrefix(sk.Value, "PRODUCT#") {
				continue
			}
			due, _ := parseTS(gsk.Value)
			items = append(items, DueItem{
				UserID: trimPrefix(pk.Value, "USER#"), ProductID: trimPrefix(sk.Value, "PRODUCT#"),
				Seen: gsk.Value, DueAt: due,
			})
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		start = out.LastEvaluatedKey
	}
	return items, nil
}

// LeaseDue pushes next_check_at to until, but only if it still equals the
// value the scheduler read. next_check_at doubles as the lease: if the
// enqueue or the worker fails, the product becomes due again at `until`
// with no extra bookkeeping. Returns false if another scheduler won.
func (s *Store) LeaseDue(ctx context.Context, it DueItem, until time.Time) (bool, error) {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 key(userPK(it.UserID), productSK(it.ProductID)),
		UpdateExpression:    aws.String("SET next_check_at = :until, GSI1SK = :until"),
		ConditionExpression: aws.String("GSI1SK = :seen"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":until": strAttr(ts(until)), ":seen": strAttr(it.Seen),
		},
	})
	if err != nil {
		if isConditionFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("lease due: %w", err)
	}
	return true, nil
}
