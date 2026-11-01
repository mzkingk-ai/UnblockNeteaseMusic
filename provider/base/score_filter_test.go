package base

import (
	"testing"

	"github.com/cnsilvan/UnblockNeteaseMusic/common"
)

func TestFilterByScore(t *testing.T) {
	old := common.MinMatchScore
	defer func() { common.MinMatchScore = old }()

	common.MinMatchScore = 0.75
	in := []*common.Song{
		{Name: "晴天", Artist: "周杰伦", MatchScore: 1.0, Source: "kuwo"},
		{Name: "晴天", Artist: "陈星晨", MatchScore: 0.65, Source: "kuwo"},
		{Name: "晴天", Artist: "某人", MatchScore: 0.74, Source: "kugou"},
		{Name: "晴天", Artist: "边界", MatchScore: 0.75, Source: "migu"},
	}
	out := FilterByScore(in)
	if len(out) != 2 {
		t.Fatalf("want 2 kept, got %d", len(out))
	}
	if out[0].Artist != "周杰伦" || out[1].Artist != "边界" {
		t.Fatalf("wrong keep set: %+v", out)
	}

	// 阈值 0 表示关闭过滤，恢复上游行为
	common.MinMatchScore = 0
	if got := FilterByScore(in); len(got) != len(in) {
		t.Fatalf("disabled filter should keep all, got %d", len(got))
	}
}
