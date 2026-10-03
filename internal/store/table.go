package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TableAdmin is the DynamoDB API needed to create tables locally.
type TableAdmin interface {
	CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	DeleteTable(context.Context, *dynamodb.DeleteTableInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteTableOutput, error)
}

// TableDefinition is the single-table schema. Terraform declares the same
// thing for AWS (infra/modules/app/dynamodb.tf); keep them in sync.
func TableDefinition(name string) *dynamodb.CreateTableInput {
	return &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI1PK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI1SK"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName: aws.String(GSI1),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("GSI1PK"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("GSI1SK"), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeKeysOnly},
		}},
		StreamSpecification: &types.StreamSpecification{
			StreamEnabled: aws.Bool(true), StreamViewType: types.StreamViewTypeNewAndOldImages,
		},
	}
}

// GSI1 is the sparse "due for a check" index.
const GSI1 = "GSI1"

// EnsureTable creates the table if it does not exist and waits until it is active.
func EnsureTable(ctx context.Context, db TableAdmin, name string) error {
	_, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	if err == nil {
		return nil
	}
	var nf *types.ResourceNotFoundException
	if !errors.As(err, &nf) {
		return fmt.Errorf("describe table: %w", err)
	}
	if _, err := db.CreateTable(ctx, TableDefinition(name)); err != nil {
		var inUse *types.ResourceInUseException
		if !errors.As(err, &inUse) {
			return fmt.Errorf("create table: %w", err)
		}
	}
	for i := 0; i < 50; i++ {
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
		if err == nil && out.Table.TableStatus == types.TableStatusActive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("table %s did not become active", name)
}
