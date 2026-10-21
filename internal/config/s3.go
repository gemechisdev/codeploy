package config

import (
	"fmt"
	"os"
	"strings"
)

// S3 is the off-host backup destination this daemon was started with. It arrives entirely from the environment:
// a bucket's key pair has no place in a plaintext settings table, and the threat model forbids a route that could
// write one after creation. Nothing here is secret-revealing — the values are held in memory by the process that
// needs them and are never printed.
type S3 struct {
	Endpoint  string // empty means the AWS regional endpoint for Region, which objectstore derives itself
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	Prefix    string // optional namespace inside the bucket, e.g. codeplo/backups
}

// LoadS3 reads CODEPLO_S3_*. The second result says whether a destination was configured at all.
//
// A half-set bucket is an error rather than an absent one. An operator who named a bucket and forgot the secret
// believes their dumps are going off-host; starting without one would let a schedule created as `s3` fail at 03:00
// for a reason that has nothing to do with the database it is dumping.
func LoadS3() (S3, bool, error) {
	v := S3{
		Endpoint:  strings.TrimSpace(os.Getenv("CODEPLO_S3_ENDPOINT")),
		Region:    strings.TrimSpace(os.Getenv("CODEPLO_S3_REGION")),
		Bucket:    strings.TrimSpace(os.Getenv("CODEPLO_S3_BUCKET")),
		AccessKey: strings.TrimSpace(os.Getenv("CODEPLO_S3_ACCESS_KEY")),
		Prefix:    strings.TrimSpace(os.Getenv("CODEPLO_S3_PREFIX")),
		// The secret is not trimmed: it is opaque, and reshaping it would sign with a value that is not the one
		// the operator pasted while still looking like it in every log line.
		SecretKey: os.Getenv("CODEPLO_S3_SECRET_KEY"),
	}
	if v.Endpoint == "" && v.Region == "" && v.Bucket == "" && v.AccessKey == "" && v.Prefix == "" && strings.TrimSpace(v.SecretKey) == "" {
		return S3{}, false, nil
	}
	missing := []string{}
	if v.Bucket == "" {
		missing = append(missing, "CODEPLO_S3_BUCKET")
	}
	if v.Region == "" {
		missing = append(missing, "CODEPLO_S3_REGION (it is part of the request signature, so it cannot be guessed)")
	}
	if v.AccessKey == "" {
		missing = append(missing, "CODEPLO_S3_ACCESS_KEY")
	}
	if strings.TrimSpace(v.SecretKey) == "" {
		missing = append(missing, "CODEPLO_S3_SECRET_KEY")
	}
	if len(missing) > 0 {
		return S3{}, false, fmt.Errorf("an s3 backup destination is half-configured: set %s, or unset the CODEPLO_S3_* variables entirely to keep backups local", strings.Join(missing, " and "))
	}
	return v, true, nil
}
