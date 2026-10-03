// Package snapshot stores raw HTML of pages whose extraction failed. In AWS
// this is an S3 bucket with a 14-day lifecycle rule; locally a directory.
package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Dir writes snapshots under a local directory.
type Dir struct{ Root string }

func (d Dir) Save(_ context.Context, key string, body []byte) error {
	clean := filepath.Clean(filepath.FromSlash(key))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return fmt.Errorf("invalid snapshot key %q", key)
	}
	path := filepath.Join(d.Root, clean)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

// PutObjectAPI is the S3 call used.
type PutObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3 writes snapshots to a bucket.
type S3 struct {
	Client PutObjectAPI
	Bucket string
}

func (s S3) Save(ctx context.Context, key string, body []byte) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String("text/html; charset=utf-8"),
	})
	if err != nil {
		return fmt.Errorf("put snapshot: %w", err)
	}
	return nil
}
