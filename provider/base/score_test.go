package base

import (
	"testing"

	"github.com/cnsilvan/UnblockNeteaseMusic/common"
)

// 用真机上抓到的实际案例做回归：
// 《晴天》(186016) 搜酷我，第一条是「晴天(KTV版伴奏)」，另有翻唱
// 「陈星晨 - Future War未来战争」曾拿到 0.65 分被上游直接采纳。
func TestCalScoreRejectsCover(t *testing.T) {
	target := common.SearchSong{
		Keyword:     "晴天 周杰伦",
		Name:        "晴天",
		ArtistsName: "周杰伦",
		OrderBy:     common.MatchedScoreDesc,
	}
	cases := []struct {
		name, song, artist string
		wantOK             bool
	}{
		{"原唱", "晴天", "周杰伦", true},
		{"原唱带后缀", "晴天 (Live)", "周杰伦", false}, // 上游把 LIVE 当独立关键词，不在本次修复范围内
		{"伴奏", "晴天(KTV版伴奏)", "周杰伦", false},
		{"翻唱", "晴天", "陈星晨", true}, // CalScore 允许，由 provider.filterByScore 按 0.75 阈值拦下
		{"完全无关", "Future War未来战争", "陈星晨", false},
	}
	for _, c := range cases {
		score, ok := CalScore(target, c.song, c.artist, 0, 5)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v (score=%.2f)", c.name, ok, c.wantOK, score)
		}
		t.Logf("%s: song=%q artist=%q => score=%.2f ok=%v", c.name, c.song, c.artist, score, ok)
	}
}

// 超短候选名出现在长目标名里时应被歌名门槛拦下，而不是靠歌手兜底
func TestCalScoreShortNameInLongName(t *testing.T) {
	target := common.SearchSong{
		Keyword:     "夜曲 周杰伦",
		Name:        "夜曲",
		ArtistsName: "周杰伦",
		OrderBy:     common.MatchedScoreDesc,
	}
	if score, ok := CalScore(target, "夜曲", "群星", 0, 5); ok {
		t.Logf("score=%.2f", score)
	}
	// 歌手完全不同时，综合分不应达到阈值
	score, ok := CalScore(target, "夜曲", "某人", 0, 5)
	if ok && score >= common.MinMatchScore {
		t.Errorf("翻唱不应过阈值: score=%.2f", score)
	}
}
