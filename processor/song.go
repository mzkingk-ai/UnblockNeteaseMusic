package processor

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/network"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/provider"
	"golang.org/x/text/width"
)

// 换源相关：从播放地址响应里挑出「需要换源」的那一项，去别的平台找同款，
// 再把结果改写回响应。原本和 RequestAfter 挤在同一个文件里。

func tryMatch(netease *Netease) bool {
	modified := false
	jsonBody := netease.JsonBody
	if value, ok := jsonBody["data"]; ok {
		switch value.(type) {
		case common.SliceType:
			if strings.Contains(netease.Path, "download") {
				for index, data := range value.(common.SliceType) {
					if index != 0 {
						break
					}
					if item, ok := data.(map[string]interface{}); ok {
						modified = searchGreySong(item, netease) || modified
					}
				}
			} else {
				modified = searchGreySongs(value.(common.SliceType), netease) || modified
			}
		case common.MapType:
			modified = searchGreySong(value.(common.MapType), netease) || modified
		default:
		}
	}
	// modifiedJson, _ := json.Marshal(jsonBody)
	// log.Println(string(modifiedJson))
	return modified
}
func searchGreySongs(data common.SliceType, netease *Netease) bool {
	modified := false
	for _, value := range data {
		switch value.(type) {
		case common.MapType:
			modified = searchGreySong(value.(common.MapType), netease) || modified
		}
	}
	return modified
}
func searchGreySong(data common.MapType, netease *Netease) bool {
	modified := false
	// 触发换源的条件，除了「没地址」「试听片段」，还要算上「VIP 歌曲降质」：
	// 未开通会员时新版接口会返回 code=200 + 96kbps aac 的完整地址，
	// freeTrialInfo 却是 null（实测 songId=38576323: fee=1 payed=0
	// encodeType=aac br=96012 level=standard），只按 url==nil 判断会漏掉这一类，
	// 表现就是「能播，但一直是标准/低音质」，用户仍然认为没解锁。
	if data["url"] == nil || data["freeTrialInfo"] != nil || isVipDowngraded(data) {
		data["flag"] = 0
		songId, ok := idOf(data)
		if !ok {
			// 没有 id 就没法去别的平台找，硬断言会 panic，然后整包响应写不回去。
			log.Printf("player/url: 响应里没有可用的 id，跳过换源(%v)\n", data["id"])
			return false
		}
		searchMusic := common.SearchMusic{Id: songId, Quality: netease.MusicQuality}
		song := provider.Find(searchMusic)
		haveSongMd5 := false
		if song.Size > 0 {
			modified = true
			if index := strings.LastIndex(song.Url, "."); index != -1 {
				songType := song.Url[index+1:]
				songType = width.Narrow.String(songType)
				if len(songType) > 5 && strings.Contains(songType, "?") {
					songType = songType[0:strings.Index(songType, "?")]
				}
				if songType == "mp3" || songType == "flac" || songType == "ape" || songType == "wav" || songType == "aac" || songType == "mp4" {
					data["type"] = songType
				} else {
					log.Println("unrecognized format:", songType)
					if song.Br > 320000 {
						data["type"] = "flac"
					} else {
						data["type"] = "mp3"
					}
				}
			} else if song.Br > 320000 {
				data["type"] = "flac"
			} else {
				data["type"] = "mp3"
			}
			if song.Br == 0 {
				if data["type"] == "flac" || data["type"] == "ape" || data["type"] == "wav" {
					song.Br = 999000
				} else {
					song.Br = 128000
				}
			}
			data["encodeType"] = data["type"] // web
			data["level"] = "standard"        // web
			data["fee"] = 8                   // web
			uri, err := url.Parse(song.Url)
			if err != nil {
				log.Println("url.Parse error:", song.Url)
				data["url"] = song.Url
			} else {
				if *config.EndPoint {
					data["url"] = generateEndpoint(netease) + uri.String()
				} else {
					data["url"] = uri.String()
				}
			}
			if len(song.Md5) > 0 {
				data["md5"] = song.Md5
				haveSongMd5 = true
			} else {
				h := md5.New()
				h.Write([]byte(song.Url))
				data["md5"] = hex.EncodeToString(h.Sum(nil))
				haveSongMd5 = false
			}
			if song.Br > 0 {
				data["br"] = song.Br
			} else {
				data["br"] = 128000
			}
			data["size"] = song.Size
			data["freeTrialInfo"] = nil
			data["code"] = 200
			if strings.Contains(netease.Path, "download") { // calculate the file md5
				if !haveSongMd5 {
					data["md5"] = calculateSongMd5(searchMusic, song.Url)
				}
			} else if !haveSongMd5 {
				go calculateSongMd5(searchMusic, song.Url)
			}
		}
	} else {

	}
	// log.Println(utils.ToJson(data))
	return modified
}
func calculateSongMd5(music common.SearchMusic, songUrl string) string {
	songMd5 := ""
	clientRequest := network.ClientRequest{
		Method:    http.MethodGet,
		RemoteUrl: songUrl,
	}
	resp, err := network.Request(&clientRequest)
	if err != nil {
		log.Println(err)
		return songMd5
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	h := md5.New()
	_, err = io.Copy(h, r)
	if err != nil {
		log.Println(err)
		return songMd5
	}
	songMd5 = hex.EncodeToString(h.Sum(nil))
	provider.UpdateCacheMd5(music, songMd5)
	// log.Println("calculateSongMd5 songId:", songId, ",songUrl:", songUrl, ",md5:", songMd5)
	return songMd5
}
