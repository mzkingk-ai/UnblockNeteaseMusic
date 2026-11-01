package kuwo

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnsilvan/UnblockNeteaseMusic/common"
)

// 这段是 2026-10-31 从酷我 PC 搜索接口实际抓下来的「晴天 周杰伦」原始响应片段，
// 7 条里没有正版原唱（周杰伦的歌在酷我基本无版权），最能代表真实困境。
// 注意它是单引号伪 JSON，不能用 json.Unmarshal，正好也验证 ParsePcAbsList 的手解能力。
const samplePcBody = `{'ARTISTPIC':'','HIT':'3589','abslist':[
{'AARTIST':'Jay&nbsp;Chou','ALBUM':'','ARTIST':'周杰伦','COPYRIGHT':'0','DC_TARGETID':'51685512','FORMATS':'AAC48|MP3128|MP3H','MINFO':'level:p,bitrate:320,format:mp3,size:10.3Mb','MUSICRID':'MUSIC_51685512','SONGNAME':'晴天&nbsp;(KTV版伴奏)'},
{'AARTIST':'','ALBUM':'','ARTIST':'周杰伦\\u0026五月天','COPYRIGHT':'0','DC_TARGETID':'152809941','FORMATS':'AAC48|MP3128','MINFO':'level:h,bitrate:128,format:mp3,size:12.01Mb','MUSICRID':'MUSIC_152809941','SONGNAME':'志明与春娇+晴天&nbsp;(2013小巨蛋演唱会第三场)'},
{'AARTIST':'','ALBUM':'','ARTIST':'周杰伦\\u0026인피니트','COPYRIGHT':'0','DC_TARGETID':'190706031','FORMATS':'AAC48','MINFO':'level:s,bitrate:48,format:aac,size:88.79Kb','MUSICRID':'MUSIC_190706031','SONGNAME':'新歌前奏&nbsp;+&nbsp;晴天&nbsp;(片段)'},
{'AARTIST':'','ALBUM':'','ARTIST':'周杰伦','COPYRIGHT':'0','DC_TARGETID':'474678847','FORMATS':'AAC48|MP3128','MINFO':'level:p,bitrate:192,format:ogg,size:4.32Mb','MUSICRID':'MUSIC_474678847','SONGNAME':'花海&nbsp;(DJ&nbsp;阿若版)'},
{'AARTIST':'','ALBUM':'','ARTIST':'周杰伦','COPYRIGHT':'0','DC_TARGETID':'349354644','FORMATS':'AAC48|MP3128','MINFO':'level:h,bitrate:128,format:mp3,size:4.16Mb','MUSICRID':'MUSIC_349354644','SONGNAME':'听妈妈的话&nbsp;(完整版|DJ彭鹏版)'},
{'AARTIST':'','ALBUM':'','ARTIST':'周杰伦','COPYRIGHT':'0','DC_TARGETID':'367887397','FORMATS':'AAC48|MP3128|MP3H','MINFO':'level:p,bitrate:320,format:mp3,size:8.26Mb','MUSICRID':'MUSIC_367887397','SONGNAME':'蜗牛'},
{'AARTIST':'','ALBUM':'','ARTIST':'冬菇滑稽','COPYRIGHT':'0','DC_TARGETID':'380476932','FORMATS':'AAC48','MINFO':'level:s,bitrate:48,format:aac,size:100Kb','MUSICRID':'MUSIC_380476932','SONGNAME':'晴天&nbsp;(原唱:周杰伦)&nbsp;激情吃力版'}
]}`

func startSampleServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(samplePcBody))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/r.s?all=%s"
}

// 修复前：songs[0]（KTV 版伴奏）被无条件采纳，用户听到的是伴奏。
// 修复后：解析真实搜索结果，所有候选都过不了阈值，结果必须是空——宁可无歌。
func TestParseSongRejectsAllFromRealSample(t *testing.T) {
	tmpl := startSampleServer(t)
	old := SearchSongURL
	SearchSongURL = tmpl
	defer func() { SearchSongURL = old }()

	searchSong := common.SearchSong{
		Keyword:     "晴天 周杰伦",
		Name:        "晴天",
		ArtistsName: "周杰伦",
		ArtistList:  []string{"周杰伦"},
		Quality:     common.ExHigh,
		OrderBy:     common.MatchedScoreDesc,
	}

	songs := (&KuWo{}).SearchSong(searchSong)
	for _, s := range songs {
		t.Logf("scored: %.2f %q - %q", s.MatchScore, s.Artist, s.Name)
	}
	if len(songs) != 0 {
		t.Fatalf("样本里没有原唱，应当全部拒绝，却得到 %d 条，首条是 %q - %q (%.2f)",
			len(songs), songs[0].Artist, songs[0].Name, songs[0].MatchScore)
	}
}

// 阈值关闭时应恢复上游行为：连 KTV 版伴奏都会入选（上游就是这样把伴奏当原唱返回的）
func TestParseSongFilterIsThresholdDriven(t *testing.T) {
	tmpl := startSampleServer(t)
	oldURL := SearchSongURL
	oldScore := common.MinMatchScore
	SearchSongURL = tmpl
	common.MinMatchScore = 0
	defer func() {
		SearchSongURL = oldURL
		common.MinMatchScore = oldScore
	}()

	searchSong := common.SearchSong{
		Keyword:     "晴天 周杰伦",
		Name:        "晴天",
		ArtistsName: "周杰伦",
		ArtistList:  []string{"周杰伦"},
		Quality:     common.ExHigh,
		OrderBy:     common.PlatformDefault,
	}
	songs := (&KuWo{}).SearchSong(searchSong)
	if len(songs) == 0 {
		t.Fatal("阈值关闭时应当仍能列出候选")
	}
	for _, s := range songs {
		t.Logf("threshold off -> %q - %q %.2f", s.Artist, s.Name, s.MatchScore)
	}
}
