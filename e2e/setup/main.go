// Command setup prepares external state the app expects before boot: the
// MinIO bucket named in the storage config. Idempotent; run it after the
// compose stack is healthy and before starting the app binary.
package main

import (
	"context"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	endpoint := envOr("QA_S3_ENDPOINT", "http://127.0.0.1:59000")
	accessKey := envOr("QA_S3_ACCESS_KEY", "minioadmin")
	secretKey := envOr("QA_S3_SECRET_KEY", "minioadmin")
	bucket := envOr("QA_S3_BUCKET", "warehouse")

	u, err := url.Parse(endpoint)
	if err != nil {
		log.Fatalf("e2e setup: bad QA_S3_ENDPOINT %q: %v", endpoint, err)
	}

	client, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: u.Scheme == "https",
	})
	if err != nil {
		log.Fatalf("e2e setup: minio client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		log.Fatalf("e2e setup: bucket check: %v", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			log.Fatalf("e2e setup: create bucket %q: %v", bucket, err)
		}
		log.Printf("e2e setup: created bucket %q", bucket)
		return
	}
	log.Printf("e2e setup: bucket %q already exists", bucket)
}
