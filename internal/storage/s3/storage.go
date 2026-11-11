// Package s3 - объектное хранилище вложений (MinIO или любой S3-совместимый сервис).
//
// Файлы не проходят через сервис: клиент получает временную подписанную ссылку
// и загружает или скачивает файл напрямую из хранилища.
package s3

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"main/internal/configs"
	domainComm "main/internal/domain"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Storage struct {
	// internal ходит в хранилище из сервиса
	internal *minio.Client
	// public только подписывает ссылки. Адрес хранилища для клиента может отличаться
	// от внутреннего (в docker-сети это minio:9000, снаружи - localhost:9000),
	// а хост входит в подпись, поэтому подписывать нужно именно внешним адресом.
	public *minio.Client
	bucket string
}

// New подключается к хранилищу и создает бакет, если его еще нет
func New(ctx context.Context, cfg configs.S3Config) (*Storage, error) {
	internal, err := newClient(cfg.Endpoint, cfg)
	if err != nil {
		return nil, err
	}

	publicEndpoint := cfg.PublicEndpoint
	if publicEndpoint == "" {
		publicEndpoint = cfg.Endpoint
	}
	public, err := newClient(publicEndpoint, cfg)
	if err != nil {
		return nil, err
	}

	exists, err := internal.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check S3 bucket %s: %w", cfg.Bucket, err)
	}
	if !exists {
		err := internal.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region})
		if err != nil {
			return nil, fmt.Errorf("create S3 bucket %s: %w", cfg.Bucket, err)
		}
	}

	return &Storage{internal: internal, public: public, bucket: cfg.Bucket}, nil
}

// newClient принимает адрес как URL (http://localhost:9000) или как host:port
func newClient(endpoint string, cfg configs.S3Config) (*minio.Client, error) {
	host, secure := endpoint, false
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse S3 endpoint %q: %w", endpoint, err)
		}
		host, secure = u.Host, u.Scheme == "https"
	}

	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: secure,
		// Регион задан явно, поэтому для подписи ссылок клиент не ходит в сеть
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client for %q: %w", endpoint, err)
	}
	return client, nil
}

// PresignPut возвращает временную ссылку для загрузки объекта методом PUT
func (s *Storage) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.public.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("s3.PresignPut: %w", err)
	}
	return u.String(), nil
}

// PresignGet возвращает временную ссылку для скачивания объекта
func (s *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.public.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("s3.PresignGet: %w", err)
	}
	return u.String(), nil
}

// Size возвращает размер загруженного объекта или ErrNotFound, если объекта нет
func (s *Storage) Size(ctx context.Context, key string) (int64, error) {
	info, err := s.internal.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return 0, fmt.Errorf("s3.Size: %w", domainComm.ErrNotFound)
		}
		return 0, fmt.Errorf("s3.Size: %w", err)
	}
	return info.Size, nil
}
