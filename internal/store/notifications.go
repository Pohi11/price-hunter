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

// GetNotification returns one outbox entry.
func (s *Store) GetNotification(ctx context.Context, userID, id string) (domain.Notification, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table, Key: key(userPK(userID), notifSK(id)), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return domain.Notification{}, fmt.Errorf("get notification: %w", err)
	}
	if out.Item == nil {
		return domain.Notification{}, domain.ErrNotFound
	}
	var it notificationItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return domain.Notification{}, err
	}
	return it.toDomain(), nil
}

// ClaimNotification moves PENDING (or abandoned SENDING) to SENDING. Only
// the claimant may send, which keeps duplicate deliveries of the same stream
// record from sending two emails.
func (s *Store) ClaimNotification(ctx context.Context, userID, id string, now time.Time, staleAfter time.Duration) (bool, error) {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                &s.table,
		Key:                      key(userPK(userID), notifSK(id)),
		UpdateExpression:         aws.String("SET #s = :sending, sending_at = :now ADD attempts :one"),
		ConditionExpression:      aws.String("#s = :pending OR (#s = :sending AND sending_at < :stale)"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":sending": strAttr(string(domain.NotificationSending)), ":pending": strAttr(string(domain.NotificationPending)),
			":now": strAttr(ts(now)), ":stale": strAttr(ts(now.Add(-staleAfter))),
			":one": &types.AttributeValueMemberN{Value: "1"},
		},
	})
	if err != nil {
		if isConditionFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("claim notification: %w", err)
	}
	return true, nil
}

// CompleteNotification records the final delivery state of a claimed notification.
func (s *Store) CompleteNotification(ctx context.Context, userID, id string, status domain.NotificationStatus, providerID, errMsg string, now time.Time) error {
	expr := "SET #s = :status, sent_at = :now"
	vals := map[string]types.AttributeValue{
		":status": strAttr(string(status)), ":now": strAttr(ts(now)), ":sending": strAttr(string(domain.NotificationSending)),
	}
	if providerID != "" {
		expr += ", provider_id = :pid"
		vals[":pid"] = strAttr(providerID)
	}
	if errMsg != "" {
		expr += ", error_message = :err"
		vals[":err"] = strAttr(truncateMsg(errMsg))
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: key(userPK(userID), notifSK(id)),
		UpdateExpression: aws.String(expr), ConditionExpression: aws.String("#s = :sending"),
		ExpressionAttributeNames: map[string]string{"#s": "status"}, ExpressionAttributeValues: vals,
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("complete notification: %w", err)
	}
	return nil
}

// ReleaseNotification returns a claimed notification to PENDING after a
// transient failure so the next retry can claim it.
func (s *Store) ReleaseNotification(ctx context.Context, userID, id, errMsg string) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: key(userPK(userID), notifSK(id)),
		UpdateExpression:         aws.String("SET #s = :pending, error_message = :err REMOVE sending_at"),
		ConditionExpression:      aws.String("#s = :sending"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending": strAttr(string(domain.NotificationPending)), ":sending": strAttr(string(domain.NotificationSending)),
			":err": strAttr(truncateMsg(errMsg)),
		},
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("release notification: %w", err)
	}
	return nil
}

// ListPendingNotifications finds a user's undelivered notifications (used by
// local mode's outbox sweep and by operators).
func (s *Store) ListPendingNotifications(ctx context.Context, userID string, limit int) ([]domain.Notification, error) {
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:                 &s.table,
		KeyConditionExpression:    aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		FilterExpression:          aws.String("#s = :pending"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": strAttr(userPK(userID)), ":prefix": strAttr("NOTIF#"), ":pending": strAttr(string(domain.NotificationPending))},
		Limit:                     pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	var items []notificationItem
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &items); err != nil {
		return nil, err
	}
	res := make([]domain.Notification, len(items))
	for i, it := range items {
		res[i] = it.toDomain()
	}
	return res, nil
}
