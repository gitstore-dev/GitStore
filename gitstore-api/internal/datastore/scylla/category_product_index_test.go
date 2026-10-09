package scylla

import "testing"

func TestCategoryProductShardIsStableAndBounded(t *testing.T) {
	uid := "00000000-0000-0000-0000-000000000001"
	got := categoryProductShard(uid)
	if got != categoryProductShard(uid) {
		t.Fatal("category product shard is not stable")
	}
	if got < 0 || got >= categoryProductShardCount {
		t.Fatalf("shard %d outside [0,%d)", got, categoryProductShardCount)
	}
}
