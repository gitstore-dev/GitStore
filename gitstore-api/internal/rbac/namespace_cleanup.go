// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

const NamespaceCleanupBuckets = 64

func ValidateNamespaceCleanup(job NamespaceCleanup) error {
	if !validUUID(job.NamespaceUID) || !resourceNamePattern.MatchString(job.Namespace) {
		return ErrInvalid
	}
	return nil
}

func NamespaceCleanupBucket(uid string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(uid))
	return int(hash.Sum32() % NamespaceCleanupBuckets)
}

func NamespaceCleanupCursor(bucket int, uid string) string {
	return EncodePageCursor("rbac-namespace-cleanup", fmt.Sprintf("%02d/%s", bucket, uid))
}

func DecodeNamespaceCleanupCursor(after string, limit int) (int, string, error) {
	key, err := DecodePageCursor("rbac-namespace-cleanup", after, limit)
	if err != nil || after == "" {
		return 0, "", err
	}
	bucketText, uid, ok := strings.Cut(key, "/")
	bucket, err := strconv.Atoi(bucketText)
	if !ok || err != nil || bucket < 0 || bucket >= NamespaceCleanupBuckets || !validUUID(uid) ||
		NamespaceCleanupBucket(uid) != bucket {
		return 0, "", ErrInvalid
	}
	return bucket, uid, nil
}
