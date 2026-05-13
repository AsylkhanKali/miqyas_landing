// Package s3store — тонкая обёртка над AWS SDK v2 для работы с S3-совместимым
// хранилищем (в локальной среде — MinIO).
//
// В корпоративной среде ожидается включённый bucket versioning и Object Lock
// (governance/compliance) для соответствия требованиям 5–7-летнего хранения.
package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Config struct {
	Endpoint   string // например http://localhost:9000
	Region     string // для MinIO достаточно us-east-1
	AccessKey  string
	SecretKey  string
	UsePathStyle bool // MinIO требует true
	Bucket     string
}

type Client struct {
	s3     *s3.Client
	bucket string
}

func New(ctx context.Context, c Config) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(c.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.UsePathStyle
	})
	return &Client{s3: client, bucket: c.Bucket}, nil
}

// PutResult — результат загрузки объекта.
type PutResult struct {
	Bucket string
	Key    string
	ETag   string
	SHA256 []byte
	Size   int64
}

// Put загружает байты под указанным ключом. Возвращает ETag и SHA-256 содержимого.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) (PutResult, error) {
	sum := sha256.Sum256(body)

	out, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		Metadata: map[string]string{
			"sha256":     fmt.Sprintf("%x", sum),
			"uploaded-at": time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return PutResult{}, fmt.Errorf("put object: %w", err)
	}
	etag := ""
	if out.ETag != nil {
		etag = *out.ETag
	}
	return PutResult{
		Bucket: c.bucket,
		Key:    key,
		ETag:   etag,
		SHA256: sum[:],
		Size:   int64(len(body)),
	}, nil
}

// Get читает объект полностью в память.
// Подходит для документов размером до десятков МБ; для крупных PDF —
// добавим streaming-вариант с io.Reader.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (c *Client) Bucket() string { return c.bucket }
