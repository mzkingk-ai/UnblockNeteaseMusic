package base

import (
	"errors"
	"fmt"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/network"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func PreSearchSong(song common.SearchSong) common.SearchSong {
	song.Keyword = strings.ToUpper(song.Keyword)
	song.Name = strings.ToUpper(song.Name)
	song.ArtistsName = strings.ToUpper(song.ArtistsName)
	return song
}
func Fetch(url string, cookies []*http.Cookie, header http.Header, proxy bool) (result map[string]interface{}, err error) {
	clientRequest := network.ClientRequest{
		Method:    http.MethodGet,
		RemoteUrl: url,
		Cookies:   cookies,
		Header:    header,
		Proxy:     proxy,
	}
	resp, err := network.Request(&clientRequest)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err = errors.New("StatusCode :" + strconv.Itoa(resp.StatusCode))
		return nil, err
	}
	defer resp.Body.Close()
	body, err := network.StealResponseBody(resp)
	if err != nil {
		return nil, err
	}
	result = utils.ParseJsonV2(body)
	return result, nil
}
func CalScore(song common.SearchSong, songName string, singerName string, index int, maxIndex int) (float32, bool) {
	if song.OrderBy == common.MatchedScoreDesc {
		if strings.Contains(songName, "伴奏") && !strings.Contains(song.Keyword, "伴奏") {
			return 0, false
		}
		if strings.Contains(strings.ToUpper(songName), "DJ") &&
			!strings.Contains(strings.ToUpper(song.Keyword), "DJ") {
			return 0, false
		}
		if strings.Contains(strings.ToUpper(songName), "COVER") &&
			!strings.Contains(strings.ToUpper(song.Keyword), "COVER") {
			return 0, false
		}
		var songNameSores float32 = 0.0
		if len(songName) > 0 {
			songNameSores = utils.CalMatchScoresV2(song.Name, songName, "songName")
		}
		// 歌名必须命中。CalMatchScoresV2 是按「长度更长的那个」来算分母的，
		// 当一个极短的候选名（如「晴天」）恰好完整出现在很长的目标歌名里时，
		// 仅靠歌名就能拿到接近满分，歌手也对不上也照样入选。
		// 此处显式要求歌名必须高度命中：短候选名完全嵌在长目标名里
		// （「晴天」vs「晴天 周杰伦」）时，0.5 的门槛仍不足以区分
		// 「晴天 (KTV版伴奏)」（0.29）这种擦边结果，直接抬到 0.7。
		if songNameSores < 0.7 && len(songName) > 0 {
			return 0, false
		}
		var artistsNameSores float32 = 0.0
		if len(singerName) > 0 {
			singerName = strings.ReplaceAll(singerName, "&", "、")
			singerName = strings.ReplaceAll(singerName, "·", "、")
			artistsNameSores = utils.CalMatchScoresV2(song.ArtistsName, singerName, "singerName")
		}
		songMatchScore := songNameSores*0.55 + artistsNameSores*0.35 + 0.1*float32(maxIndex-index)/float32(maxIndex)
		return songMatchScore, true
	} else if song.OrderBy == common.PlatformDefault {

	}
	return 0, true
}
func AfterSearchSong(song common.SearchSong, songs []*common.Song) []*common.Song {
	if song.OrderBy == common.MatchedScoreDesc && len(songs) > 1 {
		sort.Sort(common.SongSlice(songs))
	}
	if song.Limit > 0 && len(songs) > song.Limit {
		songs = songs[:song.Limit]
	}
	return songs
}

// FilterByScore 是「别放错歌」的闸：CalScore 只给分不设下限，各 provider 的
// ParseSong 又会无条件取搜索结果第一条，于是原唱无版权时会静默播放同名翻唱。
// 阈值 common.MinMatchScore 设为 0 表示关闭过滤（恢复上游行为）。
// 放在 base 包是因为 kuwo/kugou 等 provider 都要用，而它们不能被 provider 包反向引用。
func FilterByScore(songs []*common.Song) []*common.Song {
	if common.MinMatchScore <= 0 || len(songs) == 0 {
		return songs
	}
	kept := make([]*common.Song, 0, len(songs))
	for _, song := range songs {
		if song == nil {
			continue
		}
		if song.MatchScore >= common.MinMatchScore {
			kept = append(kept, song)
		} else {
			log.Println(fmt.Sprintf("reject low score %.2f < %.2f: %s - %s [%s]",
				song.MatchScore, common.MinMatchScore, song.Artist, song.Name, song.Source))
		}
	}
	return kept
}
