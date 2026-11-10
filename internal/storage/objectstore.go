package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/lydiabox/lydiabox/internal/config"
)

// ErrObjectNotFound is returned by ObjectStore.Get for a missing key.
var ErrObjectNotFound = errors.New("storage: object not found")

// ObjectStore is the subset of S3 used for metadata backups.
type ObjectStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
}

// SystemPrefix is the bucket prefix that holds LydiaBox system objects.
const SystemPrefix = ".lydiabox-system"

// TenantPrefix returns the bucket prefix of a tenant's system objects.
func TenantPrefix(tenantID string) string {
	return SystemPrefix + "/tenants/" + tenantID + "/"
}

// MetaBackupKey is the JuiceFS metadata dump of a tenant.
func MetaBackupKey(tenantID string) string { return TenantPrefix(tenantID) + "jfs-meta.json.gz" }

// StateBackupKey is the host state database snapshot of a tenant.
func StateBackupKey(tenantID string) string { return TenantPrefix(tenantID) + "lydiabox.db.gz" }

// S3Store implements ObjectStore with AWS SDK Go v2. It never reads shared
// AWS config files or environment credentials; only the LydiaBox settings.
type S3Store struct {
	client *s3.Client
	bucket string
}

// NewS3Store builds a client for an AWS or S3-compatible endpoint. AWS
// endpoints use virtual-hosted addressing; others (R2, MinIO) use path style.
func NewS3Store(cfg config.S3Config, httpClient *http.Client) (*S3Store, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("storage: invalid S3 endpoint %q", cfg.Endpoint)
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	isAWS := strings.HasSuffix(u.Hostname(), ".amazonaws.com")
	opts := s3.Options{
		Region:                     region,
		BaseEndpoint:               aws.String(strings.TrimRight(cfg.Endpoint, "/")),
		UsePathStyle:               !isAWS,
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if httpClient != nil {
		opts.HTTPClient = httpClient
	}
	return &S3Store{client: s3.New(opts), bucket: cfg.Bucket}, nil
}

// Get implements ObjectStore.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("storage: get s3://%s/%s: %w", s.bucket, key, err)
	}
	return out.Body, nil
}

// Put implements ObjectStore.
func (s *S3Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("storage: put s3://%s/%s: %w", s.bucket, key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	var respErr *smithyhttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound
}
