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

// GetDomainHealth returns the circuit-breaker state for host (zero value if none).
func (s *Store) GetDomainHealth(ctx context.Context, host string) (domain.DomainHealth, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: key(domainPK(host), healthSK)})
	if err != nil {
		return domain.DomainHealth{}, fmt.Errorf("get domain health: %w", err)
	}
	if out.Item == nil {
		return domain.DomainHealth{Host: host}, nil
	}
	var it domainHealthItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return domain.DomainHealth{}, err
	}
	return it.toDomain(), nil
}

// RecordDomainFailure atomically increments the host's consecutive failure
// count and returns the new state.
func (s *Store) RecordDomainFailure(ctx context.Context, host string, outcome domain.OutcomeCode, now time.Time) (domain.DomainHealth, error) {
	out, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &s.table,
		Key:              key(domainPK(host), healthSK),
		UpdateExpression: aws.String("SET entity = :e, host = :h, last_outcome = :o, updated_at = :now, open_count = if_not_exists(open_count, :zero) ADD consecutive_failures :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":e": strAttr(EntityDomainHealth), ":h": strAttr(host), ":o": strAttr(string(outcome)), ":now": strAttr(ts(now)),
			":one": &types.AttributeValueMemberN{Value: "1"}, ":zero": &types.AttributeValueMemberN{Value: "0"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return domain.DomainHealth{}, fmt.Errorf("record domain failure: %w", err)
	}
	var it domainHealthItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &it); err != nil {
		return domain.DomainHealth{}, err
	}
	return it.toDomain(), nil
}

// OpenCircuit opens the breaker until `until`, unless it is already open.
// Returns false if another worker opened it first.
func (s *Store) OpenCircuit(ctx context.Context, host string, until, now time.Time) (bool, error) {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 key(domainPK(host), healthSK),
		UpdateExpression:    aws.String("SET open_until = :until, consecutive_failures = :zero, updated_at = :now ADD open_count :one"),
		ConditionExpression: aws.String("attribute_not_exists(open_until) OR open_until < :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":until": strAttr(ts(until)), ":now": strAttr(ts(now)),
			":zero": &types.AttributeValueMemberN{Value: "0"}, ":one": &types.AttributeValueMemberN{Value: "1"},
		},
	})
	if err != nil {
		if isConditionFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("open circuit: %w", err)
	}
	return true, nil
}

// RecordDomainSuccess closes the breaker and resets counters. Callers only
// invoke it when their cached state shows failures, to avoid a write per check.
func (s *Store) RecordDomainSuccess(ctx context.Context, host string, now time.Time) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &s.table,
		Key:              key(domainPK(host), healthSK),
		UpdateExpression: aws.String("SET entity = :e, host = :h, consecutive_failures = :zero, open_count = :zero, last_outcome = :ok, updated_at = :now REMOVE open_until"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":e": strAttr(EntityDomainHealth), ":h": strAttr(host), ":zero": &types.AttributeValueMemberN{Value: "0"},
			":ok": strAttr(string(domain.OutcomeOK)), ":now": strAttr(ts(now)),
		},
	})
	if err != nil {
		return fmt.Errorf("record domain success: %w", err)
	}
	return nil
}
