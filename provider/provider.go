package provider

import (
	"fmt"
	"log"

	"github.com/cnsilvan/UnblockNeteaseMusic/cache"
	"github.com/cnsilvan/UnblockNeteaseMusic/common"
	"github.com/cnsilvan/UnblockNeteaseMusic/network"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider/base"
	kugou "github.com/cnsilvan/UnblockNeteaseMusic/provider/kugou"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider/kuwo"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider/migu"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider/pyncmd"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider/qq"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnsilvan/UnblockNeteaseMusic/utils"
)

type Provider interface {
	SearchSong(song common.SearchSong) (songs []*common.Song)
	GetSongUrl(searchSong common.SearchMusic, song *common.Song) *common.Song
	//search&get url
	ParseSong(searchSong common.SearchSong) *common.Song
}

var providers map[string]Provider

func Init() {
	providers = make(map[string]Provider)
	for _, source := range common.Source {
		providers[source] = NewProvider(source)
	}
	if len(providers) == 0 {
		// 兜底：正常路径下 common.Source 一定非空，但只要它是空的，
		// 上面的并发搜索就一个源都不会发出去，最终只会静默返回空结果。
		providers["kuwo"] = NewProvider("kuwo")
	}
	log.Printf("providers=%v\n", keysOf(providers))
}

func keysOf(m map[string]Provider) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
func NewProvider(kind string) Provider {
	switch kind {
	case "kuwo":
		return &kuwo.KuWo{}
	case "kugou":
		return &kugou.KuGou{}
	case "migu":
		return &migu.Migu{}
	case "qq":
		return &qq.QQ{}
	case "pyncmd":
		return &pyncmd.PyNcmd{}
	default:
		// 未知音源退回酷我，保持上游行为不变。
		return &kuwo.KuWo{}
	}
}
func GetProvider(kind string) Provider {
	if p, ok := providers[kind]; ok {
		return p
	}
	return NewProvider(kind)

}

func UpdateCacheMd5(music common.SearchMusic, md5 string) {
	if song, ok := cache.GetSong(music); ok {
		song.Md5 = md5
		cache.PutSong(music, song)
	}
}
func Find(music common.SearchMusic) common.Song {
	log.Println(fmt.Sprintf("find song info :%+v", music))
	if song, ok := cache.GetSong(music); ok {
		if len(song.Url) > 0 {
			log.Println("hit cache:", utils.ToJson(song))
			if checkCache(song) {
				return *song
			} else if strings.Index(music.Id, string(common.StartTag)) == 0 {
				log.Println("but cache invalid")
			} else {
				cache.Delete(music)
				log.Println("but cache invalid")
			}
		}
		//log.Println("search:", utils.ToJson(song))
		if strings.Index(music.Id, string(common.StartTag)) == 0 {
			now := time.Now()
			var re *common.Song
			if strings.Index(music.Id, string(common.KuGouTag)) == 0 {
				re = calculateSongInfo(GetProvider("kugou").GetSongUrl(music, song))
			} else if strings.Index(music.Id, string(common.KuWoTag)) == 0 {
				re = calculateSongInfo(GetProvider("kuwo").GetSongUrl(music, song))
			} else if strings.Index(music.Id, string(common.MiGuTag)) == 0 {
				re = calculateSongInfo(GetProvider("migu").GetSongUrl(music, song))
			} else if strings.Index(music.Id, string(common.QQTag)) == 0 {
				re = calculateSongInfo(GetProvider("qq").GetSongUrl(music, song))
			} else {

			}
			log.Println("consumed:", time.Since(now))
			log.Println(utils.ToJson(re))
			if re != nil && len(re.Url) > 0 {
				cache.PutSong(music, re)
				return *re
			}
		}
	}
	var songT common.Song
	songT.Id = music.Id
	clientRequest := network.ClientRequest{
		Method:    http.MethodGet,
		RemoteUrl: "https://" + common.HostDomain["music.163.com"] + "/api/song/detail?ids=[" + songT.Id + "]",
		Host:      "music.163.com",
		Header:    nil,
		Proxy:     false,
	}
	resp, err := network.Request(&clientRequest)
	if err != nil {
		return songT
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		body, err2 := network.StealResponseBody(resp)
		if err2 != nil {
			log.Println("GetResponseBody fail")
			return songT
		}
		oJson := utils.ParseJsonV2(body)
		if songs, ok := oJson["songs"].(common.SliceType); ok && len(songs) > 0 {
			song := songs[0]
			var searchSong = make(common.MapType, 8)
			searchSong["songId"] = songT.Id
			var artists []string
			switch song.(type) {
			case common.MapType:
				neteaseSong := song.(common.MapType)
				searchSong["id"] = neteaseSong["id"]
				searchSong["name"] = neteaseSong["name"]
				searchSong["alias"] = neteaseSong["alias"]
				searchSong["duration"] = neteaseSong["duration"]
				searchSong["album"] = make(common.MapType, 2)
				searchSong["album"].(common.MapType)["id"], ok = neteaseSong["album"].(common.MapType)["id"]
				searchSong["album"].(common.MapType)["name"], ok = neteaseSong["album"].(common.MapType)["name"]
				switch neteaseSong["artists"].(type) {
				case common.SliceType:
					length := len(neteaseSong["artists"].(common.SliceType))
					searchSong["artists"] = make(common.SliceType, length)
					artists = make([]string, length)
					for index, value := range neteaseSong["artists"].(common.SliceType) {
						if searchSong["artists"].(common.SliceType)[index] == nil {
							searchSong["artists"].(common.SliceType)[index] = make(common.MapType, 2)
						}
						searchSong["artists"].(common.SliceType)[index].(common.MapType)["id"], ok = value.(common.MapType)["id"]
						searchSong["artists"].(common.SliceType)[index].(common.MapType)["name"], ok = value.(common.MapType)["name"]
						artists[index], ok = value.(common.MapType)["name"].(string)
					}

				}
			default:

			}
			//if searchSong["name"] != nil {
			//	searchSong["name"] = utils.ReplaceAll(searchSong["name"].(string), `\s*cover[:：\s][^）]+）`, "")
			//	searchSong["name"] = utils.ReplaceAll(searchSong["name"].(string), `(\s*cover[:：\s][^\)]+)`, "")
			//}
			searchSong["artistsName"] = strings.Join(artists, " ")
			searchSong["artistList"] = artists
			// 网易在某些歌曲/未登录环境下会返回「结构完整但内容全 null」的详情
			// （实测 id=34930257：name/artists/album 都是 null）。此时对 name
			// 直接做 .(string) 会 panic: interface conversion: interface {} is nil,
			// not string —— 被 proxy 的 recover 接住，表现为这一次请求返回 0 字节。
			// 没有元数据就做不了歌名匹配，但 pyncmd 只需要 songId，所以这里降级：
			// keyword 留空、跳过换源搜索，只走 pyncmd。
			songName, _ := searchSong["name"].(string)
			artistsName, _ := searchSong["artistsName"].(string)
			searchSong["keyword"] = strings.TrimSpace(songName + " " + artistsName)
			if len(searchSong["keyword"].(string)) == 0 {
				log.Println("song meta empty, fallback to songId only: " + songT.Id)
				base := &common.Song{Id: songT.Id}
				if py := GetProvider("pyncmd").GetSongUrl(common.SearchMusic{Id: songT.Id, Quality: music.Quality}, base); py != nil && len(py.Url) > 0 {
					py = calculateSongInfo(py)
					cache.PutSong(music, py)
					log.Println(utils.ToJson(*py))
					return *py
				}
				return songT
			}
			log.Println("search song:" + searchSong["keyword"].(string))
			// pyncmd 与换源搜索放进同一个并发窗口一起跑：
			//   pyncmd 不需要歌名匹配，直接拿网易 songId 换地址，拿回来的就是
			//   这首歌本身（实测 md5 与网易原址一致），不存在猜原唱/翻唱风险，
			//   所以有结果就优先用它；但它走公网第三方接口，最坏要 3 次 × 8 秒
			//   重试，排在前面串行跑会把整首解析拖到 25 秒以上。改为同时发出，
			//   由 resolveSong 统一收口。
			songT = *resolveSong(searchSong, music)
			log.Println(utils.ToJson(songT))
			return songT

		} else {
			return songT
		}
	} else {
		return songT
	}
}

// findFromPyNcmd 用网易 songId 走 pyncmd 拿直链。
//
// 这不是「换源」，而是「同一首歌换一条不校验 VIP 的取址通道」，所以：
//   - 不需要歌名/歌手匹配，MatchScore 直接给 1.0；
//   - 不需要过 MinMatchScore 阈值；
//   - 拿回来的码率/大小就是网易那份文件的真实值。
//
// key 里必须有 songId（由 Find 从 /api/song/detail 的响应里填进来）；
// 取不到就返回空，交给换源搜索兜底。
func findFromPyNcmd(key common.MapType, music common.SearchMusic) *common.Song {
	empty := &common.Song{}
	songId, ok := key["songId"].(string)
	if !ok || len(songId) == 0 {
		return empty
	}
	now := time.Now()
	base := &common.Song{Id: songId}
	// key["name"] 在元数据缺失时可能是 nil（网易返回 name:null），
	// 直接 .(string) 会 panic，这里用带 ok 的断言。
	if v, ok := key["name"].(string); ok {
		base.Name = v
	}
	if v, ok := key["artistsName"].(string); ok {
		base.Artist = v
	}
	if v, ok := key["album"].(common.MapType); ok {
		if name, ok := v["name"].(string); ok {
			base.AlbumName = name
		}
	}
	re := GetProvider("pyncmd").GetSongUrl(common.SearchMusic{Id: songId, Quality: music.Quality}, base)
	log.Println("pyncmd consumed:", time.Since(now))
	if re == nil || len(re.Url) == 0 {
		// pyncmd 没有这首歌：不写缓存，让换源搜索照常进行。
		return empty
	}
	re = calculateSongInfo(re)
	// pyncmd 拿到的是「同一首网易歌的直链」，缓存键不带平台前缀，
	// 否则下一次同一首歌再进来时命中不了（GetSong 用的是原始 music）。
	cache.PutSong(music, re)
	return re
}

// resolveSong 把 pyncmd 和所有换源放进同一个并发窗口，统一收口。
//
// 之前的写法是先串行跑 pyncmd、没结果再进 getSongFromAllSource 的 6 秒窗口，
// 于是「pyncmd 慢」会直接叠在换源耗时之上：pyncmd 内部是最多 3 次 × 8 秒
// 的重试，最坏能把一次播放请求拖到 25 秒以上，而换源那边其实几百毫秒就
// 已经有结果了。现在两边同时发出：
//   - pyncmd 有结果就用它（它给的是网易原曲，不用猜原唱）；
//   - 否则用换源里 MatchScore 最高、同分取体积大的那条（与老逻辑一致）。
//
// resolveResult 区分结果来自 pyncmd 还是换源，避免靠 MatchScore==1 猜。
type resolveResult struct {
	song   *common.Song
	fromPy bool
}

func resolveSong(key common.MapType, music common.SearchMusic) *common.Song {
	now := time.Now()
	ch := make(chan resolveResult)
	pending := 0 // 换源剩余未返回数

	// pyncmd 走独立通道：它不参与 MatchScore 比较，也不计入 pending。
	go utils.PanicWrapper(func() {
		ch <- resolveResult{song: findFromPyNcmd(key, music), fromPy: true}
	})

	for _, p := range providers {
		pt := p
		go utils.PanicWrapper(func() {
			ch <- resolveResult{song: calculateSongInfo(pt.ParseSong(buildSearchSong(key, music)))}
		})
		pending++
	}

	songs := make([]*common.Song, 0, pending)
	var py *common.Song
	// 定时器必须在循环外建：写在 select 里会每轮重新计时，
	// 收到一条消息就把 deadline 往后推，实测能拖到 14.9s 才超时。
	//
	// 窗口 20s：这是按「无代理直连」的最坏情况定的（实测会挂到 17.9s）。
	// 有代理时 pyncmd 自己 6s 就返回，窗口大小不影响实际耗时 ——
	// 换源全返回且已有结果就提前收工，pyncmd 先回来也会立刻返回。
	// 只有「换源全废 + 直连 pyncmd 慢」这一种情况才真会等满。
	timeout := time.NewTimer(time.Second * 20)
	defer timeout.Stop()
	for {
		select {
		case r := <-ch:
			s := r.song
			if !r.fromPy {
				pending--
			}
			if s != nil && len(s.Url) > 0 {
				if r.fromPy {
					py = s
					// pyncmd 给的是网易原曲，优先级最高，拿到就能收工。
					return pickSong(py, FilterByScore(songs), key, music)
				}
				songs = append(songs, s)
			}
			// 换源已有可用结果，且换源都返回了 → 不必再陪慢的 pyncmd 等。
			if pending <= 0 && len(songs) > 0 {
				return pickSong(py, FilterByScore(songs), key, music)
			}
		case <-timeout.C:
			// 超时后不再等剩下的音源，但已经到的结果仍然可用。
			log.Println("resolve timeout, got:", time.Since(now))
			return pickSong(py, FilterByScore(songs), key, music)
		}
	}
}

// pickSong 在 pyncmd 结果与换源结果之间做最终选择。
func pickSong(py *common.Song, songs []*common.Song, key common.MapType, music common.SearchMusic) *common.Song {
	if py != nil {
		log.Println("resolved by pyncmd:", utils.ToJson(py))
		return py
	}
	id := "0"
	if songId, ok := key["songId"]; ok {
		id = songId.(string)
	}
	result := &common.Song{}
	result.Size = 0
	for _, song := range songs {
		if song.MatchScore > result.MatchScore {
			result = song
		} else if song.MatchScore == result.MatchScore && song.Size > result.Size {
			result = song
		}
	}
	if id != "0" {
		result.Id = id
		if len(result.Url) > 0 {
			result.PlatformUniqueKey = nil
			cache.PutSong(music, result)
		}
	}
	return result
}

// buildSearchSong 由 Find 组装好的 key 生成换源搜索参数（大写的歌名/歌手）。
func buildSearchSong(key common.MapType, music common.SearchMusic) common.SearchSong {
	searchKeyword, _ := key["keyword"].(string)
	artistList, _ := key["artistList"].([]string)
	// 这里原本有一行 key["musicQuality"] = music.Quality。它写进去的值从来没被读过
	// （下面的 Quality 字段直接用 music.Quality），却让 resolveSong 里所有音源的
	// goroutine 并发写同一个 map —— Go 对并发写 map 是 fatal error，进程当场退出，
	// 而且 recover 抓不住。单音源时只有一个 goroutine 写，所以以前没暴露；
	// 改成多音源（pyncmd+kuwo+kugou+migu）后每次播放必崩。删掉即可。
	return common.SearchSong{
		Keyword:     searchKeyword,
		Name:        strings.ToUpper(key["name"].(string)),
		ArtistsName: strings.ToUpper(key["artistsName"].(string)),
		Quality:     music.Quality,
		OrderBy:     common.MatchedScoreDesc,
		Limit:       1,
		ArtistList:  artistList,
	}
}

// FilterByScore 委托给 base 包，保持调用方签名不变。
func FilterByScore(songs []*common.Song) []*common.Song {
	return base.FilterByScore(songs)
}

func SearchSongFromAllSource(key common.SearchSong) []*common.Song {
	var songs []*common.Song
	ch := make(chan []*common.Song)
	sum := 0
	for _, p := range providers {
		pt := p
		go utils.PanicWrapper(func() {
			ch <- pt.SearchSong(key)
		})
		sum++
	}
	for {
		select {
		case song, _ := <-ch:
			songs = append(songs, song...)
			sum--
			if sum <= 0 {
				return FilterByScore(songs)
			}
		case <-time.After(time.Second * 6):
			return FilterByScore(songs)
		}
	}
}
func calculateSongInfo(song *common.Song) *common.Song {
	if len(song.Url) > 0 {
		if len(song.Md5) > 0 && song.Br > 0 && song.Size > 0 {
			return song
		}
		if song.Br > 0 && song.Size > 0 && !strings.Contains(song.Url, "qq.com") && !strings.Contains(song.Url, "xiami.net") && !strings.Contains(song.Url, "qianqian.com") {
			return song
		}
		header := make(http.Header, 1)
		header["range"] = append(header["range"], "bytes=0-8191")
		uri, err := url.Parse(song.Url)
		if err == nil {
			song.Url = uri.String()
		}
		clientRequest := network.ClientRequest{
			Method:         http.MethodGet,
			RemoteUrl:      song.Url,
			Header:         header,
			ConnectTimeout: time.Second * 2,
			Proxy:          true,
		}
		resp, err := network.Request(&clientRequest)
		if err != nil {
			log.Println("processSong fail:", err)
			return song
		}
		defer resp.Body.Close()
		if resp.StatusCode > 199 && resp.StatusCode < 300 {
			if strings.Contains(song.Url, "qq.com") {
				song.Md5 = resp.Header.Get("server-md5")
			} else if strings.Contains(song.Url, "xiami.net") || strings.Contains(song.Url, "qianqian.com") {
				song.Md5 = strings.ToLower(utils.ReplaceAll(resp.Header.Get("etag"), `/"/g`, ""))
				//.replace(/"/g, '').toLowerCase()
			}
			if song.Size == 0 {
				size := resp.Header.Get("content-range")
				if len(size) > 0 {
					sizeSlice := strings.Split(size, "/")
					if len(sizeSlice) > 0 {
						size = sizeSlice[len(sizeSlice)-1]
					}
				} else {
					size = resp.Header.Get("content-length")
					if len(size) < 1 {
						size = "0"
					}
				}
				song.Size, _ = strconv.ParseInt(size, 10, 64)
			}
			if song.Br == 0 {
				//log.Println(utils.LogInterface(resp.Header))
				if resp.Header.Get("content-length") == "8192" || resp.ContentLength == 8192 {
					body, err := network.GetResponseBody(resp, false)
					if err != nil {
						log.Println("song GetResponseBody error:", err)
						return song
					}
					bitrate := decodeBitrate(body)
					if bitrate == 999 || (bitrate > 0 && bitrate < 500) {
						song.Br = bitrate * 1000
					}
				}
			}
		} else {
			return &common.Song{}
		}
	}
	return song
}
func decodeBitrate(data []byte) int {
	bitRateMap := map[int]map[int][]int{
		0: {
			3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 500},
			2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 500},
			1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 500},
		},
		3: {
			3: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 500},
			2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 500},
			1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 500},
		},
		2: {
			3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 500},
			2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 500},
			1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 500},
		},
	}

	var pointer = 0
	if strings.EqualFold(string(data[0:4]), "fLaC") {
		return 999
	}
	if strings.EqualFold(string(data[0:3]), "ID3") {
		pointer = 6
		var size = 0
		for index, value := range data[pointer : pointer+4] {
			size = size + int((value&0x7f)<<(7*(3-index)))
		}
		pointer = 10 + size
	}

	for i := 0; i < len(data); i++ { //fix migu mp3
		if data[pointer] != 0xff { //fail
			pointer = pointer + 1
			continue
		} else {
			break
		}
	}
	if pointer > len(data)-4 {
		return 0
	}
	header := data[pointer : pointer+4]
	// https://www.allegro.cc/forums/thread/591512/674023
	if len(header) == 4 &&
		header[0] == 0xff &&
		((header[1]>>5)&0x7) == 0x7 &&
		((header[1]>>1)&0x3) != 0 &&
		((header[2]>>4)&0xf) != 0xf &&
		((header[2]>>2)&0x3) != 0x3 {
		version := (header[1] >> 3) & 0x3
		layer := (header[1] >> 1) & 0x3
		bitrate := header[2] >> 4
		return bitRateMap[int(version)][int(layer)][int(bitrate)]
	}
	return 0
}
func checkCache(song *common.Song) bool {
	header := make(http.Header, 1)
	header["range"] = append(header["range"], "bytes=0-1")
	clientRequest := network.ClientRequest{
		Method:         http.MethodGet,
		RemoteUrl:      song.Url,
		Header:         header,
		Proxy:          true,
		ConnectTimeout: time.Second * 2,
	}
	resp, err := network.Request(&clientRequest)
	if err != nil {
		log.Println("checkCache fail:", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false
	} else {
		return true
	}
}
