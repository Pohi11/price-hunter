// Package store is the DynamoDB persistence layer. All entities live in one
// table (see ADR-002); this package owns every key format so the rest of the
// code never builds a "USER#..." string.
//
//	Entity        PK               SK
//	Profile       USER#<sub>       PROFILE
//	Product       USER#<sub>       PRODUCT#<ulid>      (+ GSI1 "due" while schedulable)
//	URL guard     USER#<sub>       URL#<sha256>
//	Notification  USER#<sub>       NOTIF#<ulid>
//	Price point   PRODUCT#<ulid>   PRICE#<timestamp>
//	Check         PRODUCT#<ulid>   CHECK#<ulid>
//	Domain health DOMAIN#<host>    HEALTH
package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DueShards is the number of GSI1 partitions products are spread across.
// Fixed forever: changing it would require re-keying every product.
const DueShards = 4

// API is the subset of the DynamoDB client the store uses (eases faking).
type API interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

// Store implements persistence for every service.
type Store struct {
	db    API
	table string
}

// New returns a Store for table.
func New(db API, table string) *Store { return &Store{db: db, table: table} }

// Table returns the table name.
func (s *Store) Table() string { return s.table }

// Errors specific to the check pipeline.
var (
	// ErrCheckNotRunning means the check was already finished by someone
	// else (a duplicate delivery) and this result must be dropped.
	ErrCheckNotRunning = errors.New("check is not running")
	// ErrInvalidCursor is returned for tampered or mismatched page cursors.
	ErrInvalidCursor = errors.New("invalid cursor")
)

// Key builders.
func userPK(userID string) string        { return "USER#" + userID }
func productSK(productID string) string  { return "PRODUCT#" + productID }
func productPK(productID string) string  { return "PRODUCT#" + productID }
func urlSK(hash string) string           { return "URL#" + hash }
func notifSK(id string) string           { return "NOTIF#" + id }
func checkSK(id string) string           { return "CHECK#" + id }
func priceSK(t time.Time) string         { return "PRICE#" + ts(t) }
func domainPK(host string) string        { return "DOMAIN#" + host }
func dueShardPK(shard int) string        { return fmt.Sprintf("DUE#%d", shard) }
func trimPrefix(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

const (
	profileSK = "PROFILE"
	healthSK  = "HEALTH"
)

// ShardFor returns the GSI1 shard for a product.
func ShardFor(productID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(productID))
	return int(h.Sum32() % DueShards)
}

// tsLayout is a fixed-width UTC timestamp; lexical order equals time order,
// which sort keys and GSI1SK rely on.
const tsLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

func parseTS(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(tsLayout, s)
}

func parseTSPtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := parseTS(*s)
	if err != nil {
		return nil
	}
	return &t
}

// pageSize converts a caller-supplied limit to a DynamoDB Limit, clamped
// to [1, 1000] so no request can ask for an unbounded page.
func pageSize(n int) *int32 {
	switch {
	case n < 1:
		n = 1
	case n > 1000:
		n = 1000
	}
	return aws.Int32(int32(n)) //nolint:gosec // clamped above
}

// ttlAt returns an epoch-seconds TTL attribute value.
func ttlAt(t time.Time) int64 { return t.Unix() }

func strAttr(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }

func key(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": strAttr(pk), "SK": strAttr(sk)}
}

// Cursors are opaque to clients but carry only a sort key; the partition key
// is always rebuilt from the authenticated user, so a forged cursor cannot
// reach another user's data.
type cursor struct {
	SK string `json:"sk"`
}

func encodeCursor(lek map[string]types.AttributeValue) string {
	if lek == nil {
		return ""
	}
	sk, ok := lek["SK"].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	b, _ := json.Marshal(cursor{SK: sk.Value})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(c, pk, wantPrefix string) (map[string]types.AttributeValue, error) {
	if c == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var cur cursor
	if err := json.Unmarshal(raw, &cur); err != nil || !strings.HasPrefix(cur.SK, wantPrefix) {
		return nil, ErrInvalidCursor
	}
	return key(pk, cur.SK), nil
}

// isConditionFailed reports a ConditionalCheckFailedException.
func isConditionFailed(err error) bool {
	var ccf *types.ConditionalCheckFailedException
	return errors.As(err, &ccf)
}

// cancellationReasons returns the per-item reason codes of a cancelled transaction.
func cancellationReasons(err error) ([]string, bool) {
	var tce *types.TransactionCanceledException
	if !errors.As(err, &tce) {
		return nil, false
	}
	codes := make([]string, len(tce.CancellationReasons))
	for i, r := range tce.CancellationReasons {
		codes[i] = aws.ToString(r.Code)
	}
	return codes, true
}
