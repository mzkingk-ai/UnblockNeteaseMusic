package kuwo

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider/base"
	"html"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/cnsilvan/UnblockNeteaseMusic/common"
	"github.com/cnsilvan/UnblockNeteaseMusic/network"
	"github.com/cnsilvan/UnblockNeteaseMusic/utils"
)

type KuWo struct{}

// SearchSongURL 是带一个 %s 占位符的模板，可用 file:// 指向本地样本做回归测试。
//
// 用 PC 接口（itemset=web_2013&rn=30）而不是上游的 mobi 接口，原因（实测 2026-10-31）：
//   - mobi 档只有 7~20 条结果，且上游只取前 min(len/2+1,5) 条。歌名+歌手组合搜时
//     酷我把大量翻唱顶在前面，真正的原唱经常排在 5 条之后（如「成都 赵雷」原唱在第 2 条，
//     但同名前 5 条全是伴奏/DJ/合唱版本）；
//   - PC 接口固定返回 30 条，还多了 COPYRIGHT/FORMATS/PLAYCNT 等字段。
//
// 注意这个接口返回的是单引号「伪 JSON」，json.Unmarshal 解析不了，必须走下面的
// ParsePcAbsList 手解，不能像上游那样直接用 base.Fetch。
var SearchSongURL = "http://search.kuwo.cn/r.s?&all=%s&ft=music&itemset=web_2013&client=kt&pn=0&rn=30&rformat=json&encoding=utf8"

var blockSongUrl = map[string]json.Number{
	"2914632520.mp3":  "7",
	"2272659253.mp3":  "7",
	"3992993176.flac": "7",
}

var lock sync.Mutex

func (m *KuWo) SearchSong(song common.SearchSong) (songs []*common.Song) {
	song = base.PreSearchSong(song)
	// 每次搜索都重新建表：包级 map 跨请求复用会把上一次的结果带过来
	// （曾出现同一批 7 条候选被重复扫描两遍）。
	seen := make(map[string]struct{}, 32)
	keyWordList := utils.Combination(song.ArtistList)
	wg := sync.WaitGroup{}
	for _, v := range keyWordList {
		wg.Add(1)
		// use goroutine to deal multiple request
		go func(word string) {
			defer wg.Done()
			keyWord := song.Name
			if len(word) != 0 {
				keyWord = fmt.Sprintf("%s %s", song.Name, word)
			}
			//token := getToken(keyWord)
			header := make(http.Header, 4)
			//header["referer"] = append(header["referer"], "http://www.kuwo.cn/search/list?key="+url.QueryEscape(keyWord))
			//header["csrf"] = append(header["csrf"], token)
			//header["cookie"] = append(header["cookie"], "kw_token="+token)
			searchUrl := fmt.Sprintf(SearchSongURL, url.QueryEscape(keyWord))
			list, err := FetchPcAbsList(searchUrl, header)
			if err != nil {
				log.Println(err)
				return
			}
			if len(list) > 0 {
				// 上游只取前 min(len/2+1,5) 条。PC 接口固定 30 条，
				// 原唱经常被一堆伴奏/DJ/合唱顶到第 6 条以后，故放宽到 kuwoMaxScan。
				// 真正的把关交给 CalScore 的歌名门槛 + MinMatchScore 阈值。
				maxIndex := kuwoMaxScan
				if len(list) < maxIndex {
					maxIndex = len(list)
				}
				for index, matched := range list {
					if index >= maxIndex {
						break
					}
					kuWoSong := matched
					{
						{
							musicRid, ok := kuWoSong["MUSICRID"].(string)
							if ok {
								rids := strings.Split(musicRid, "_")
								rid := rids[len(rids)-1]
								songResult := &common.Song{}
								singerName := html.UnescapeString(kuWoSong["ARTIST"].(string))
								songName := html.UnescapeString(kuWoSong["SONGNAME"].(string))
								//musicSlice := strings.Split(musicrid, "_")
								//musicId := musicSlice[len(musicSlice)-1]
								songResult.PlatformUniqueKey = kuWoSong
								songResult.PlatformUniqueKey["UnKeyWord"] = song.Keyword
								songResult.Source = "kuwo"
								songResult.PlatformUniqueKey["header"] = header
								songResult.PlatformUniqueKey["musicId"] = rid
								songResult.Id = rid
								if len(songResult.Id) > 0 {
									songResult.Id = string(common.KuWoTag) + songResult.Id
								}
								songResult.Name = songName
								songResult.Artist = singerName
								// MINFO 里带着这首在各档位是否有资源（level:ff/bitrate:2000/format:flac 等），
								// 以前丢掉了。GetSongUrl 只按 MINFO 要真实存在的最高档位，
								// 否则 mobi.s 对没有 flac 的歌只会回 128k 的 M500 链接（见 kuwoBestFormat）。
								if v, ok := kuWoSong["MINFO"].(string); ok {
									NMinfoOf(songResult)["MINFO"] = html.UnescapeString(v)
								}
								songResult.AlbumName = html.UnescapeString(kuWoSong["ALBUM"].(string))
								// 同一个 rid 会被「歌名」「歌名+歌手」等多路检索重复命中，去重后再计分，
								// 否则同一条候选会被打分两次、在结果里出现两遍。
								lock.Lock()
								if _, dup := seen[rid]; dup {
									lock.Unlock()
									continue
								}
								seen[rid] = struct{}{}
								lock.Unlock()
								songResult.Artist = strings.ReplaceAll(singerName, " ", "")
								songResult.MatchScore, ok = base.CalScore(song, songName, singerName, index, maxIndex)
								if !ok {
									continue
								}
								// protect slice thread safe
								lock.Lock()
								songs = append(songs, songResult)
								lock.Unlock()
							}
						}
					}
				}
			}
		}(v)
	}
	wg.Wait()
	return base.AfterSearchSong(song, songs)
}
func (m *KuWo) GetSongUrl(searchSong common.SearchMusic, song *common.Song) *common.Song {
	if id, ok := song.PlatformUniqueKey["musicId"]; ok {
		if musicId, ok := id.(string); ok {
			if httpHeader, ok := song.PlatformUniqueKey["header"]; ok {
				if header, ok := httpHeader.(http.Header); ok {
					header["user-agent"] = append(header["user-agent"], "okhttp/3.10.0")
					format := "flac|mp3"
					br := ""
					retry := true
					for retry {
						retry = false
						// 优先要真实存在的最高档位。实测酷我 mobi.s 对这首
						// 《晴天》无论要什么都没 flac，直接给 128k 的 M500，
						// 而客户端默认请求 flac，结果永远是 128k。
						format, br = kuwoBestFormat(song, searchSong.Quality)

						clientRequest := network.ClientRequest{
							Method:               http.MethodGet,
							ForbiddenEncodeQuery: true,
							RemoteUrl:            "http://mobi.kuwo.cn/mobi.s?f=kuwo&q=" + base64.StdEncoding.EncodeToString(Encrypt([]byte("corp=kuwo&p2p=1&type=convert_url2&sig=0&format="+format+"&rid="+musicId+br))),
							Header:               header,
							Proxy:                true,
						}
						resp, err := network.Request(&clientRequest)
						if err != nil {
							log.Println(err)
							return song
						}
						defer resp.Body.Close()
						body, err := network.GetResponseBody(resp, false)
						reg := regexp.MustCompile(`http[^\s$"]+`)
						address := string(body)
						params := reg.FindStringSubmatch(address)
						if len(params) > 0 {
							u, _ := url.Parse(params[0])
							if _, ok := blockSongUrl[path.Base(u.Path)]; ok {
								log.Println(song.PlatformUniqueKey["UnKeyWord"].(string) + ` ` + format + "，该歌曲酷我版权保护")
								if searchSong.Quality > 0 {
									retry = true
									log.Println(song.PlatformUniqueKey["UnKeyWord"].(string) + " ，降低音质重试")
									searchSong.Quality = searchSong.Quality - 1
								} else {
									return song
								}
							} else {
								song.Url = params[0]
								return song
							}
						}
					}
				}
			}
		}
	}
	return song
}
func (m *KuWo) ParseSong(searchSong common.SearchSong) *common.Song {
	song := &common.Song{}
	songs := m.SearchSong(searchSong)
	// ParseSong 是「按歌曲详情反查音源」的入口（provider.Find 走这条路），
	// 它同样会拿到低分候选，必须和 SearchSongFromAllSource 过同一个闸，
	// 否则阈值只在搜索页生效，播放页照样会播错歌。
	songs = base.FilterByScore(songs)
	if len(songs) > 0 {
		song = m.GetSongUrl(common.SearchMusic{Quality: searchSong.Quality}, songs[0])
	}
	return song
}
func getToken(keyword string) string {
	var token = ""
	clientRequest := network.ClientRequest{
		Method:    http.MethodGet,
		RemoteUrl: "http://kuwo.cn/search/list?key=" + keyword,
		Host:      "kuwo.cn",
		Header:    nil,
		Proxy:     false,
	}
	resp, err := network.Request(&clientRequest)
	if err != nil {
		log.Println(err)
		return token
	}
	defer resp.Body.Close()
	cookies := resp.Header.Get("set-cookie")
	if strings.Contains(cookies, "kw_token") {
		cookies = utils.ReplaceAll(cookies, ";.*", "")
		splitSlice := strings.Split(cookies, "=")
		token = splitSlice[len(splitSlice)-1]
	}
	return token
}

// NMinfoOf 返回一个挂在 song.PlatformUniqueKey 下的子 map，用来暂存 MINFO，
// 不污染搜索结果原始字段（原始字段会被直接当 musicId/header 容器使用）。
func NMinfoOf(song *common.Song) common.MapType {
	if song.PlatformUniqueKey == nil {
		song.PlatformUniqueKey = make(common.MapType, 8)
	}
	if v, ok := song.PlatformUniqueKey["minfo"].(common.MapType); ok {
		return v
	}
	m := make(common.MapType, 2)
	song.PlatformUniqueKey["minfo"] = m
	return m
}

// kuwoBestFormat 依据搜索结果里的 MINFO 选一个「真的存在的」档位。
//
// 背景（实测 2026-10-31，《晴天》rid=228908）：
//   - format=flac|mp3 与 format=flac 一律回空，只剩 type=1；
//   - format=mp3 不带 br、带 &br=320kmp3、带 &br=192kmp3 全都返回
//     同一个 M500...mp3（bitrate=1，128k）。
//
// 所以「强行要最高音质」在酷我这里是无效的，硬要 flac 只会退化成 128k。
// 这里按 MINFO/format 位选：有无损就 flac，否则用能拿到的最高 mp3 码率。
func kuwoBestFormat(song *common.Song, quality common.MusicQuality) (format string, br string) {
	nminfo := ""
	if song != nil && song.PlatformUniqueKey != nil {
		if m, ok := song.PlatformUniqueKey["minfo"].(common.MapType); ok {
			if v, ok := m["MINFO"].(string); ok {
				nminfo = v
			}
		}
	}
	lower := strings.ToLower(nminfo)
	// 只认确实可下到的档位。MINFO 里 level:p 同时对应 ogg/mp3，
	// 早期版本把 level:p 当成「有 320k mp3」会误判，这里只按 bitrate+format 判定。
	hasLossless := strings.Contains(lower, "format:flac") && strings.Contains(lower, "bitrate:2000")
	has320 := strings.Contains(lower, "format:mp3") && strings.Contains(lower, "bitrate:320")
	has192 := strings.Contains(lower, "format:mp3") && strings.Contains(lower, "bitrate:192")

	switch quality {
	case common.Lossless:
		if hasLossless {
			return "flac|mp3", ""
		}
		fallthrough
	case common.ExHigh:
		if has320 {
			return "mp3", "&br=320kmp3"
		}
		fallthrough
	case common.Higher:
		if has192 {
			return "mp3", "&br=192kmp3"
		}
		fallthrough
	default:
		// Standard，或高音质确实没有资源：直接要 mp3，别再用 flac|mp3 换一个
		// 空的回来（空响应会让上层走到 6 秒超时并返回空结果）。
		return "mp3", ""
	}
}
