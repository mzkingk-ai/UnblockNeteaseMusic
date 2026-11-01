package processor

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/cnsilvan/UnblockNeteaseMusic/cache"
	"github.com/cnsilvan/UnblockNeteaseMusic/common"
	"github.com/cnsilvan/UnblockNeteaseMusic/config"
	"github.com/cnsilvan/UnblockNeteaseMusic/network"
	"github.com/cnsilvan/UnblockNeteaseMusic/processor/crypto"
	"github.com/cnsilvan/UnblockNeteaseMusic/provider"
	"github.com/cnsilvan/UnblockNeteaseMusic/utils"
	"golang.org/x/text/width"
)

var (
	eApiKey     = "e82ckenh8dichen8"
	linuxApiKey = "rFgB&h#%2?^eDg:Q"
	// /api/song/enhance/player/url
	// /eapi/mlivestream/entrance/playlist/get
	Path = map[string]int{
		"/api/v3/playlist/detail":                  1,
		"/api/v3/song/detail":                      1,
		"/api/v6/playlist/detail":                  1,
		"/api/album/play":                          1,
		"/api/artist/privilege":                    1,
		"/api/album/privilege":                     1,
		"/api/v1/artist":                           1,
		"/api/v1/artist/songs":                     1,
		"/api/artist/top/song":                     1,
		"/api/v1/album":                            1,
		"/api/album/v3/detail":                     1,
		"/api/playlist/privilege":                  1,
		"/api/song/enhance/player/url":             1,
		"/api/song/enhance/player/url/v1":          1,
		"/api/song/enhance/download/url":           1,
		"/batch":                                   2, // Search
		"/api/batch":                               1,
		"/api/v1/search/get":                       2, // IOS
		"/api/v1/search/song/get":                  2,
		"/api/search/complex/get":                  1,
		"/api/search/complex/get/v2":               2, // Android
		"/api/cloudsearch/pc":                      3, // PC Value
		"/api/v1/playlist/manipulate/tracks":       1,
		"/api/song/like":                           1,
		"/api/v1/play/record":                      1,
		"/api/playlist/v4/detail":                  1,
		"/api/v1/radio/get":                        1,
		"/api/v1/discovery/recommend/songs":        1,
		"/api/cloudsearch/get/web":                 1,
		"/api/song/enhance/privilege":              1,
		"/api/osx/version":                         1,
		"/api/usertool/sound/mobile/promote":       1,
		"/api/usertool/sound/mobile/theme":         1,
		"/api/usertool/sound/mobile/animationList": 1,
		"/api/usertool/sound/mobile/all":           1,
		"/api/usertool/sound/mobile/detail":        1,
		"/api/pc/upgrade/get":                      1,
	}
)

type Netease struct {
	Path           string
	Params         map[string]interface{}
	JsonBody       map[string]interface{}
	Web            bool
	Encrypted      bool
	Forward        bool
	EndPoint       string
	MusicQuality   common.MusicQuality
	SearchPath     string
	SearchKey      string
	SearchTaskChan chan []*common.Song `json:"-"`
	SearchSongs    []*common.Song
}

// hasApiPrefix 判断路径是不是网易云的标准 API 前缀。
// 上游只认 /weapi/ 和 /api/ 两种，于是 iOS 版客户端的 /xeapi/... 一条都进不来，
// 播放地址永远原样透传（实测 iOS 全部请求都是 xeapi）。
// /weapi/ 会被下面 normalize 成 /api/，/xeapi/ 同理。
func hasApiPrefix(path string) bool {
	return strings.Index(path, "/weapi/") == 0 ||
		strings.Index(path, "/api/") == 0 ||
		strings.Index(path, "/xeapi/") == 0
}

func RequestBefore(request *http.Request) *Netease {
	netease := &Netease{Path: request.URL.Path}

	if request.Method == http.MethodPost && (strings.Contains(netease.Path, "/eapi/") || strings.Contains(netease.Path, "/api/linux/forward")) {
		if *config.BlockAds && (strings.Contains(netease.Path, "api/ad/") || strings.Contains(netease.Path, "api/clientlog/upload") || strings.Contains(netease.Path, "api/feedback/weblog")) {
			return nil
		}
		request.Header.Del("x-napm-retry")
		request.Header.Set("X-Real-IP", "118.66.66.66")
		request.Header.Set("Accept-Encoding", "gzip, deflate")
		requestBody, _ := ioutil.ReadAll(request.Body)
		requestHold := ioutil.NopCloser(bytes.NewBuffer(requestBody))
		request.Body = requestHold
		pad := make([]byte, 0)
		reg := regexp.MustCompile(`%0+$`)
		if matched := reg.Find(requestBody); len(matched) > 0 {
			pad = requestBody
		}
		if netease.Path == "/api/linux/forward" {
			netease.Forward = true
			requestBodyH := make([]byte, len(requestBody))
			length, _ := hex.Decode(requestBodyH, requestBody[8:len(requestBody)-len(pad)])
			decryptECBBytes, _ := crypto.AesDecryptECB(requestBodyH[:length], []byte(linuxApiKey))
			var result common.MapType
			result = utils.ParseJson(decryptECBBytes)
			urlM, ok := result["url"].(string)
			if ok {
				netease.Path = urlM
			}
			params, ok := result["params"].(common.MapType)
			if ok {
				netease.Params = params
			}
		} else if len(requestBody) > 7 {
			requestBodyH := make([]byte, len(requestBody))
			length, err := hex.Decode(requestBodyH, requestBody[7:len(requestBody)-len(pad)])
			if err != nil || length == 0 {
				// 请求体不是合法的 eapi 密文（比如调试时漏了长度前缀）：保留原
				// Path 原样转发，别让后面 data[1] 越界把整个请求打成空响应。
				log.Printf("eapi body decode fail(%s) len=%d err=%v\n", netease.Path, len(requestBody), err)
			} else {
				decryptECBBytes, _ := crypto.AesDecryptECB(requestBodyH[:length], []byte(eApiKey))
				decryptString := string(decryptECBBytes)
				data := strings.Split(decryptString, "-36cd479b6b5-")
				if len(data) >= 2 {
					netease.Path = data[0]
					netease.Params = utils.ParseJson(bytes.NewBufferString(data[1]).Bytes())
				} else {
					log.Printf("eapi body decrypt fail(%s): %q\n", netease.Path, decryptString)
				}
			}
		}
		netease.Path = strings.ReplaceAll(netease.Path, "https://music.163.com", "")
		netease.Path = strings.ReplaceAll(netease.Path, "http://music.163.com", "")
		// 解出来的明文路径同样可能带前缀（/xeapi/ 是 iOS 的新写法），统一归一化。
		netease.Path = utils.ReplaceAll(netease.Path, `^\/eapi\/`, "/api/")
		netease.Path = utils.ReplaceAll(netease.Path, `^\/weapi\/`, "/api/")
		netease.Path = utils.ReplaceAll(netease.Path, `^\/xeapi\/`, "/api/")
		netease.Path = utils.ReplaceAll(netease.Path, `\/\d*$`, "")
	} else if hasApiPrefix(netease.Path) {
		request.Header.Set("X-Real-IP", "118.66.66.66")
		netease.Web = true
		// 普通 /api/ GET 的码率等参数全在 query 里，而这条路原来不解析 Params，
		// 导致 unifiedMusicQuality 永远读到空 map、拿默认的 Lossless 去取歌：
		// pyncmd 会回一份 37MB 的 flac，手机要 128k 也照给，路由器白白转发几十兆。
		// 注意 netease.Path 在 RequestBefore 开头就是 request.URL.Path（不含 query），
		// 所以必须从 request.URL.Query() 取，不能对 Path 做 url.Parse。
		if rawQuery := request.URL.RawQuery; len(rawQuery) > 0 {
			q := common.MapType{}
			for k, v := range request.URL.Query() {
				if len(v) > 0 {
					q[k] = v[0]
				}
			}
			if len(q) > 0 {
				netease.Params = q
			}
		}
		netease.Path = utils.ReplaceAll(netease.Path, `^\/weapi\/`, "/api/")
		netease.Path = utils.ReplaceAll(netease.Path, `^\/xeapi\/`, "/api/")
		netease.Path = utils.ReplaceAll(netease.Path, `\?.+$`, "")
		netease.Path = utils.ReplaceAll(netease.Path, `\/\d*$`, "")
	} else if strings.Contains(netease.Path, "package") {

	}
	unifiedMusicQuality(netease)
	searchOtherPlatform(netease)
	return netease
}
func Request(request *http.Request, remoteUrl string) (*http.Response, error) {
	clientRequest := network.ClientRequest{
		Method:    request.Method,
		RemoteUrl: remoteUrl,
		Host:      request.Host,
		Header:    request.Header,
		Body:      request.Body,
		Proxy:     true,
	}
	return network.Request(&clientRequest)
}
func RequestAfter(request *http.Request, response *http.Response, netease *Netease) {
	pass := false
	if _, ok := Path[netease.Path]; ok {
		pass = true
	}
	if pass && response.StatusCode == 200 {

		encode := response.Header.Get("Content-Encoding")
		enableGzip := false
		if len(encode) > 0 && (strings.Contains(encode, "gzip") || strings.Contains(encode, "deflate")) {
			enableGzip = true
		}
		body, _ := ioutil.ReadAll(response.Body)
		response.Body.Close()
		tmpBody := make([]byte, len(body))
		copy(tmpBody, body)
		if len(body) > 0 {
			decryptECBBytes := body
			if enableGzip {
				decryptECBBytes, _ = utils.UnGzip(decryptECBBytes)
			}
			aeskey := eApiKey
			if netease.Forward {
				aeskey = linuxApiKey
			}
			result := utils.ParseJson(decryptECBBytes)
			netease.Encrypted = false
			if result == nil {
				decryptECBBytes, encrypted := crypto.AesDecryptECB(decryptECBBytes, []byte(aeskey))
				netease.Encrypted = encrypted
				result = utils.ParseJson(decryptECBBytes)
			}
			netease.JsonBody = result

			modified := false
			codeN, ok := netease.JsonBody["code"].(json.Number)
			code := "200"
			if ok {
				code = codeN.String()
			}

			logResponse(netease)

			if strings.EqualFold(netease.Path, "/api/osx/version") ||
				strings.EqualFold(netease.Path, "/api/pc/upgrade/get") {
				modified = disableUpdate(netease)
			} else if strings.Contains(netease.Path, "/usertool/sound/") {
				modified = unblockSoundEffects(netease.JsonBody)
			} else if strings.Contains(netease.Path, "/batch") {
				modified = localVIP(netease)
				for key, resp := range netease.JsonBody {
					if strings.Contains(key, "/usertool/sound/") {
						modified = unblockSoundEffects(resp.(map[string]interface{}))
					} else if *config.BlockAds && strings.Contains(key, "api/ad/") {
						log.Println("block Ad has been triggered(" + key + ").")
						resp = &common.MapType{}
						modified = true
					} else if *config.BlockAds && strings.EqualFold(key, "/api/v2/banner/get") {
						newInfo := make(common.SliceType, 0)
						info := netease.JsonBody[key]
						for _, data := range info.(common.MapType)["banners"].(common.SliceType) {
							if banner, ok := data.(common.MapType); ok {
								if banner["adid"] == nil {
									newInfo = append(newInfo, banner)
								} else {
									log.Println("block banner Ad has been triggered.")
									modified = true
								}
							}
						}
						info.(common.MapType)["banners"] = newInfo
					}
				}
			} else if !netease.Web && (code == "401" || code == "512") && strings.Contains(netease.Path, "manipulate") {
				modified = tryCollect(netease, request)
			} else if !netease.Web && (code == "401" || code == "512") && strings.EqualFold(netease.Path, "/api/song/like") {
				modified = tryLike(netease, request)
			} else if strings.Contains(netease.Path, "url") {
				modified = tryMatch(netease)
			} else {
				modified = tryAddOtherPlatformResult(netease)
			}

			if processMapJson(netease.JsonBody) || modified {
				response.Header.Del("transfer-encoding")
				response.Header.Del("content-encoding")
				response.Header.Del("content-length")
				// netease.JsonBody = netease.JsonBody
				// log.Println("NeedRepackage")
				modifiedJson, _ := json.Marshal(netease.JsonBody)
				// log.Println(netease)
				if *config.LogWebTraffic {
					log.Println("modified =>\n" + string(modifiedJson))
				}
				if netease.Encrypted {
					modifiedJson = crypto.AesEncryptECB(modifiedJson, []byte(aeskey))
				}
				response.Body = ioutil.NopCloser(bytes.NewBuffer(modifiedJson))
			} else {
				// log.Println("NotNeedRepackage")
				responseHold := ioutil.NopCloser(bytes.NewBuffer(tmpBody))
				response.Body = responseHold
			}

			// log.Println("netease.Path: " + netease.Path)
			// log.Println("netease.JsonBody: " + utils.ToJson(netease.JsonBody) +
			// 	"\nrequestRequestURI: " + request.RequestURI +
			// 	"\nrequestHeader: " + utils.ToJson(request.Header) +
			// 	"\nrequestMethod: " + request.Method +
			// 	"\nrequestUserAgent: " + request.UserAgent())
		} else {
			responseHold := ioutil.NopCloser(bytes.NewBuffer(tmpBody))
			response.Body = responseHold
		}
	} else {
		// log.Println("Not Process: " + netease.Path)
	}
}

func disableUpdate(netease *Netease) bool {
	if !*config.BlockUpdate {
		return false
	}
	modified := false
	jsonBody := netease.JsonBody
	if value, ok := jsonBody["updateFiles"]; ok {
		switch value.(type) {
		case common.SliceType:
			if len(value.(common.SliceType)) > 0 {
				modified = true
				jsonBody["updateFiles"] = make(common.SliceType, 0)
				log.Println("disable update has been triggered.")
			}
		default:
		}
	} else if value, ok = jsonBody["data"]; ok {
		modified = true
		jsonBody["data"].(common.MapType)["packageVO"] = nil
	}
	return modified
}

func logResponse(netease *Netease) {
	if *config.LogWebTraffic {
		reqUrl := netease.Path
		jsonBody := netease.JsonBody
		modifiedJson, _ := json.Marshal(jsonBody)
		sep := "===================================\n"
		log.Println(sep + reqUrl + " => \n" + string(modifiedJson) + "\n")
	}
}

func localVIP(netease *Netease) bool {
	if !*config.EnableLocalVip {
		return false
	}
	modified := false
	if utils.Exist("/api/music-vip-membership/client/vip/info", netease.JsonBody) {
		log.Println("localVIP has been triggered.")
		modified = true
		info := netease.JsonBody["/api/music-vip-membership/client/vip/info"]
		expireTime, _ := info.(common.MapType)["data"].(common.MapType)["now"].(json.Number).Int64()
		expireTime += 3162240000000
		info.(common.MapType)["data"].(common.MapType)["redVipLevel"] = 7
		info.(common.MapType)["data"].(common.MapType)["redVipAnnualCount"] = 1
		info.(common.MapType)["data"].(common.MapType)["musicPackage"] = &common.MapType{"expireTime": expireTime, "vipCode": 230}
		info.(common.MapType)["data"].(common.MapType)["associator"] = &common.MapType{"expireTime": expireTime}
	}
	return modified
}
func unblockSoundEffects(jsonBody map[string]interface{}) bool {
	if !*config.UnlockSoundEffects {
		return false
	}
	// JsonBody,_ := json.Marshal(jsonBody)
	modified := false
	if value, ok := jsonBody["data"]; ok {
		switch value.(type) {
		case common.SliceType:
			if len(value.(common.SliceType)) > 0 {
				modified = true
				for _, data := range value.(common.SliceType) {
					if datum, ok := data.(common.MapType); ok {
						datum["type"] = 1
					}
				}
			}
		case common.MapType:
			if utils.Exist("type", value.(common.MapType)) {
				modified = true
				value.(common.MapType)["type"] = 1
			}
		default:
		}
	}
	// modifiedJson, _ := json.Marshal(jsonBody)
	// log.Println("netease.JsonBody: " + string(JsonBody))
	// log.Println("netease.modifiedJson: " + string(modifiedJson))
	if modified {
		log.Println("unblockSoundEffects has been triggered.")
	}

	return modified

}

func tryCollect(netease *Netease, request *http.Request) bool {
	modified := false
	// log.Println(utils.ToJson(netease))
	if utils.Exist("trackIds", netease.Params) {
		trackId := ""
		switch netease.Params["trackIds"].(type) {
		case string:
			var result common.SliceType
			err := utils.ParseJsonV3(bytes.NewBufferString(netease.Params["trackIds"].(string)).Bytes(), &result)
			if err == nil {
				trackId = result[0].(string)
			} else {
				log.Println(err)
				return false
			}

		case common.SliceType:
			trackId = netease.Params["trackIds"].(common.SliceType)[0].(json.Number).String()
		}
		pid := netease.Params["pid"].(string)
		op := netease.Params["op"].(string)
		proxyRemoteHost := common.HostDomain["music.163.com"]
		clientRequest := network.ClientRequest{
			Method:    http.MethodPost,
			Host:      "music.163.com",
			RemoteUrl: "http://" + proxyRemoteHost + "/api/playlist/manipulate/tracks",
			Header:    request.Header,
			Body:      ioutil.NopCloser(bytes.NewBufferString("trackIds=[" + trackId + "," + trackId + "]&pid=" + pid + "&op=" + op)),
			Proxy:     true,
		}
		resp, err := network.Request(&clientRequest)
		if err != nil {
			return modified
		}
		defer resp.Body.Close()
		body, err := network.StealResponseBody(resp)
		if err != nil {
			return modified
		}
		netease.JsonBody = utils.ParseJsonV2(body)
		modified = true
	}
	return modified
}
func tryLike(netease *Netease, request *http.Request) bool {
	// log.Println("try like")
	modified := false
	if utils.Exist("trackId", netease.Params) {
		trackId := netease.Params["trackId"].(string)
		proxyRemoteHost := common.HostDomain["music.163.com"]
		clientRequest := network.ClientRequest{
			Method:    http.MethodGet,
			Host:      "music.163.com",
			RemoteUrl: "http://" + proxyRemoteHost + "/api/v1/user/info",
			Header:    request.Header,
			Proxy:     true,
		}
		resp, err := network.Request(&clientRequest)
		if err != nil {
			return modified
		}
		defer resp.Body.Close()
		body, err := network.StealResponseBody(resp)
		if err != nil {
			return modified
		}
		jsonBody := utils.ParseJsonV2(body)
		if utils.Exist("userPoint", jsonBody) && utils.Exist("userId", jsonBody["userPoint"].(common.MapType)) {
			userId := jsonBody["userPoint"].(common.MapType)["userId"].(json.Number).String()
			clientRequest.RemoteUrl = "http://" + proxyRemoteHost + "/api/user/playlist?uid=" + userId + "&limit=1"
			resp, err = network.Request(&clientRequest)
			if err != nil {
				return modified
			}
			defer resp.Body.Close()
			body, err = network.StealResponseBody(resp)
			if err != nil {
				return modified
			}
			jsonBody = utils.ParseJsonV2(body)
			if utils.Exist("playlist", jsonBody) {
				pid := jsonBody["playlist"].(common.SliceType)[0].(common.MapType)["id"].(json.Number).String()
				clientRequest.Method = http.MethodPost
				clientRequest.RemoteUrl = "http://" + proxyRemoteHost + "/api/playlist/manipulate/tracks"
				clientRequest.Body = ioutil.NopCloser(bytes.NewBufferString("trackIds=[" + trackId + "," + trackId + "]&pid=" + pid + "&op=add"))
				resp, err = network.Request(&clientRequest)
				if err != nil {
					return modified
				}
				defer resp.Body.Close()
				body, err = network.StealResponseBody(resp)
				if err != nil {
					return modified
				}
				jsonBody = utils.ParseJsonV2(body)
				code := jsonBody["code"].(json.Number).String()
				if code == "200" || code == "502" {
					netease.JsonBody = make(common.MapType)
					netease.JsonBody["code"] = 200
					netease.JsonBody["playlistId"] = pid
					modified = true
				}
			}
		}
	}

	return modified
}
func tryMatch(netease *Netease) bool {
	modified := false
	jsonBody := netease.JsonBody
	if value, ok := jsonBody["data"]; ok {
		switch value.(type) {
		case common.SliceType:
			if strings.Contains(netease.Path, "download") {
				for index, data := range value.(common.SliceType) {
					if index == 0 {
						modified = searchGreySong(data.(common.MapType), netease) || modified
						break
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
	if data["url"] == nil || data["freeTrialInfo"] != nil {
		data["flag"] = 0
		songId := data["id"].(json.Number).String()
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
func processSliceJson(jsonSlice common.SliceType) bool {
	needModify := false
	for _, value := range jsonSlice {
		switch value.(type) {
		case common.MapType:
			needModify = processMapJson(value.(common.MapType)) || needModify

		case common.SliceType:
			needModify = processSliceJson(value.(common.SliceType)) || needModify

		default:
			// log.Printf("index(%T):%v\n", index, index)
			// log.Printf("value(%T):%v\n", value, value)
		}
	}
	return needModify
}
func processMapJson(jsonMap common.MapType) bool {
	needModify := false
	if utils.Exists([]string{"st", "subp", "pl", "dl", "chargeType", "cannotListenReason", "paidBigBang", "resConsumable", "cp", "fee", "payed", "fl", "flLevel", "plLevel"}, jsonMap) {
		if v, _ := jsonMap["st"]; v != nil && v.(json.Number).String() != "0" {
			// open gray song
			jsonMap["st"] = 0
			needModify = true
		}
		if v, _ := jsonMap["subp"]; v != nil && v.(json.Number).String() != "1" {
			jsonMap["subp"] = 1
			needModify = true
		}
		if v, _ := jsonMap["pl"]; v != nil && v.(json.Number).String() == "0" {
			jsonMap["pl"] = 320000
			needModify = true
		}
		if v, _ := jsonMap["dl"]; v != nil && v.(json.Number).String() == "0" {
			jsonMap["dl"] = 320000
			needModify = true
		}
		if v, _ := jsonMap["chargeType"]; v != nil && v.(json.Number).String() == "1" {
			jsonMap["chargeType"] = 0
			needModify = true
		}
		if v, _ := jsonMap["paidBigBang"]; v != nil {
			jsonMap["paidBigBang"] = true
			needModify = true
		}
		if v, _ := jsonMap["resConsumable"]; v != nil {
			jsonMap["resConsumable"] = false
			needModify = true
		}
		if v, _ := jsonMap["cannotListenReason"]; v != nil {
			jsonMap["cannotListenReason"] = nil
			needModify = true
		}
		if v, _ := jsonMap["cp"]; v != nil && v.(json.Number).String() == "0" {
			jsonMap["cp"] = 1
			needModify = true
		}
		//if v, _ := jsonMap["fee"]; v != nil && v.(json.Number).String() == "1" {
		//	jsonMap["fee"] = 8
		//	needModify = true
		//}
		if v, _ := jsonMap["fl"]; v != nil && v.(json.Number).String() == "0" {
			jsonMap["fl"] = 320000
			needModify = true
		}
		if v, _ := jsonMap["flLevel"]; v != nil && v == "none" {
			jsonMap["flLevel"] = "exhigh"
			needModify = true
		}
		if v, _ := jsonMap["plLevel"]; v != nil && v == "none" {
			jsonMap["plLevel"] = "exhigh"
			needModify = true
		}
		if v, _ := jsonMap["payed"]; v != nil {
			jsonMap["payed"] = 1
			needModify = true
		}
	}
	for _, value := range jsonMap {
		switch value.(type) {
		case common.MapType:
			needModify = processMapJson(value.(common.MapType)) || needModify
		case common.SliceType:
			needModify = processSliceJson(value.(common.SliceType)) || needModify
		default:
			// if key == "fee" {
			//	fee := "0"
			//	switch value.(type) {
			//	case int:
			//		fee = strconv.Itoa(value.(int))
			//	case json.Number:
			//		fee = value.(json.Number).String()
			//	case string:
			//		fee = value.(string)
			//	}
			//	if fee != "0" && fee != "8" {
			//		jsonMap[key] = 0
			//		needModify = true
			//	}
			// }
		}
	}
	return needModify
}

func unifiedMusicQuality(netease *Netease) {
	// log.Println(fmt.Sprintf("%+v\n", utils.ToJson(netease.Params)))
	netease.MusicQuality = common.Lossless
	if !*config.ForceBestQuality {
		// 上游这里写的是 if level { ... } else if br { ... }，
		// 但网易客户端的取址请求同时带 level 和 br，level 分支一命中 br 就永远读不到，
		// 而 level 只是客户端的档位标记、并不等于实际要的码率（实测 level=standard
		// 配 br=320000 时网易自己回的就是 320k）。所以这里改成两者独立判断、br 优先：
		// br 是数字型，必须用 fmt.Sprint 抹平类型，否则 strconv 断言失败会一路跌回 Lossless。
		level := ""
		if v, ok := netease.Params["level"].(string); ok {
			level = strings.ToLower(v)
		}
		br := ""
		if v, ok := netease.Params["br"]; ok {
			br = strings.ToLower(fmt.Sprint(v))
		}
		switch {
		case isLosslessRate(br):
			netease.MusicQuality = common.Lossless
		case isExHighRate(br):
			netease.MusicQuality = common.ExHigh
		case isHigherRate(br):
			netease.MusicQuality = common.Higher
		case isStandardRate(br):
			netease.MusicQuality = common.Standard
		case strings.Contains(level, "lossless"):
			netease.MusicQuality = common.Lossless
		case strings.Contains(level, "exhigh"):
			netease.MusicQuality = common.ExHigh
		case strings.Contains(level, "higher"):
			netease.MusicQuality = common.Higher
		case strings.Contains(level, "standard"):
			netease.MusicQuality = common.Standard
		}
		// log.Println(fmt.Sprintf("%+v\n", utils.ToJson(netease.MusicQuality)))
	}
}

// 下面四个函数把 br 的各种写法（128000 / 128 / "128k"）归到同一档。
// 用前缀匹配而不是 Contains：客户端偶发传 "br=320000,999000" 这类组合值。
func isLosslessRate(br string) bool {
	return strings.HasPrefix(br, "999") || strings.HasPrefix(br, "1999") || strings.HasPrefix(br, "1411")
}

func isExHighRate(br string) bool {
	return strings.HasPrefix(br, "320")
}

func isHigherRate(br string) bool {
	return strings.HasPrefix(br, "192")
}

func isStandardRate(br string) bool {
	return strings.HasPrefix(br, "128")
}
func generateEndpoint(netease *Netease) string {
	protocol := "https"
	endPoint := "://music.163.com/unblockmusic/"
	if headerParam, ok := netease.Params["header"]; ok {
		header := make(map[string]interface{})
		if headerStr, ok := headerParam.(string); ok {
			header = utils.ParseJson([]byte(headerStr))
		} else if header, ok = headerParam.(map[string]interface{}); ok {

		}
		if len(header) > 0 {
			if headerValue, ok := header["os"]; ok {
				if os, ok := headerValue.(string); ok && strings.Contains(strings.ToLower(os), "pc") {
					protocol = "http"
				}
			}
		}

	}
	if osParam, ok := netease.Params["os"]; ok {
		if os, ok := osParam.(string); ok && strings.Contains(strings.ToLower(os), "pc") {
			protocol = "http"
		}
	}
	netease.EndPoint = protocol + endPoint
	// log.Println(fmt.Sprintf("%+v\n", utils.ToJson(netease.EndPoint)))
	return netease.EndPoint
}
func searchOtherPlatform(netease *Netease) *Netease {
	// log.Println(utils.ToJson(netease))
	if *config.SearchLimit > 0 && Path[netease.Path] == 2 {
		var paramsMap map[string]interface{}
		if utils.Exists([]string{"offset", "s"}, netease.Params) { // 单曲
			netease.SearchPath = netease.Path
			paramsMap = netease.Params
		} else if utils.Exists([]string{"keyword", "scene"}, netease.Params) { // 综合
			netease.SearchPath = netease.Path
			paramsMap = netease.Params
		} else { // pc
			for k, v := range netease.Params {
				// pc
				if t, ok := Path[k]; ok {
					if t == 3 { // search
						if searchValue, ok := v.(string); ok {
							paramsMap = utils.ParseJson([]byte(searchValue))
							netease.SearchPath = k
							break
						}
					}
				}
			}
		}
		if paramsMap != nil {
			var offset int64
			if offsetJson, ok := paramsMap["offset"].(json.Number); ok {
				// just offset=0
				if i, err := offsetJson.Int64(); err == nil {
					offset = i
				}

			} else if offsetS, ok := paramsMap["offset"].(string); ok {
				// just offset=0
				if i, err := strconv.ParseInt(offsetS, 10, 64); err == nil {
					offset = i
				}

			}
			if offset == 0 {
				if searchS, ok := paramsMap["s"].(string); ok {
					netease.SearchKey = searchS

				} else if searchS, ok := paramsMap["keyword"].(string); ok {
					netease.SearchKey = searchS

				}
			}

		}
		if len(netease.SearchKey) > 0 {
			var ch = make(chan []*common.Song, 1)
			netease.SearchTaskChan = ch
			go utils.PanicWrapper(func() {
				trySearch(netease, ch)
			})
		}
	}
	return netease
}
func trySearch(netease *Netease, ch chan []*common.Song) {
	ch <- provider.SearchSongFromAllSource(common.SearchSong{Keyword: netease.SearchKey, Limit: *config.SearchLimit, OrderBy: common.PlatformDefault})
	// log.Println(utils.ToJson(songs))
}
func tryAddOtherPlatformResult(netease *Netease) bool {
	modified := false
	if *config.SearchLimit <= 0 {
		return false
	}
	if len(netease.SearchSongs) == 0 && netease.SearchTaskChan != nil {
		if result, ok := <-netease.SearchTaskChan; ok {
			netease.SearchSongs = result
			close(netease.SearchTaskChan)
		}
	}
	if len(netease.SearchKey) > 0 && netease.JsonBody != nil && len(netease.SearchSongs) > 0 { // 搜索页面
		var orginalMap common.MapType
		var orginalSongsKey string
		var neteaseSongs common.SliceType
		if jBody, ok := netease.JsonBody[netease.SearchPath].(common.MapType); ok {
			if result, ok := jBody["result"].(common.MapType); ok {
				if neteaseSongs, ok = result["songs"].(common.SliceType); ok {
					orginalMap = result
					orginalSongsKey = "songs"

				}

			}
		} else if jBody, ok := netease.JsonBody["data"].(common.MapType); ok { // android 综合
			if result, ok := jBody["complete"].(common.MapType); ok {
				if s, ok := result["song"].(common.MapType); ok {
					if neteaseSongs, ok = s["songs"].(common.SliceType); ok {
						orginalMap = s
						orginalSongsKey = "songs"
					}

				}

			}
		} else if jBody, ok := netease.JsonBody["result"].(common.MapType); ok {
			if neteaseSongs, ok = jBody["songs"].(common.SliceType); ok { // android&ios 单曲
				orginalMap = jBody
				orginalSongsKey = "songs"
			} else if s, ok := jBody["song"].(common.MapType); ok { // ios 综合
				if neteaseSongs, ok = s["songs"].(common.SliceType); ok {
					orginalMap = s
					orginalSongsKey = "songs"
				}
			}

		}
		if orginalMap != nil && len(orginalSongsKey) > 0 && neteaseSongs != nil && len(neteaseSongs) > 0 {
			if template, ok := neteaseSongs[0].(common.MapType); ok {
				var newSongs common.SliceType
				for _, song := range netease.SearchSongs {
					var copySong common.MapType
					err := utils.ParseJsonV4(bytes.NewBufferString(utils.ToJson(template)), &copySong)
					if err != nil {
						log.Println(err)
						continue
					}
					source := "来自block"
					idTag := common.KuWoTag
					switch song.Source {
					case "kugou":
						source = "来自酷狗音乐"
						idTag = common.KuGouTag
					case "migu":
						source = "来自咪咕音乐"
						idTag = common.MiGuTag
					case "kuwo":
						source = "来自酷我音乐"
						idTag = common.KuWoTag
					default:
						source = "来自block"
						idTag = common.KuWoTag

					}
					if _, ok := copySong["name"]; ok { // make sure ok
						copySong["alia"] = []string{source}
						if ar, ok := copySong["ar"]; ok {
							artMap := make(common.MapType)
							artMap["name"] = song.Artist
							if ars, ok := ar.(common.SliceType); ok {
								var a []interface{}
								if len(ars) > 0 {
									if b, ok := ars[0].(common.MapType); ok {
										b["name"] = song.Artist
										a = append(a, b)
									} else {
										a = append(a, artMap)
									}
								} else {
									a = append(a, artMap)
								}
								copySong["ar"] = a
							} else {
								copySong["ar"] = []common.MapType{artMap}
							}
						}
						if al, ok := copySong["al"]; ok {
							if alMap, ok := al.(common.MapType); ok {
								alMap["name"] = song.AlbumName
							}
						}
						idS := song.Id
						if len(idS) == 0 {
							idS = string(idTag) + cache.GetPlatFormIdTag(idTag)
						}
						songSelfId, err := strconv.ParseInt(idS, 10, 64)
						if err != nil {
							log.Println(err.Error())
							continue
						} else {
							cache.PutSong(common.SearchMusic{Id: idS, Quality: common.Standard}, song)
							cache.PutSong(common.SearchMusic{Id: idS, Quality: common.Higher}, song)
							cache.PutSong(common.SearchMusic{Id: idS, Quality: common.ExHigh}, song)
							cache.PutSong(common.SearchMusic{Id: idS, Quality: common.Lossless}, song)
							copySong["id"] = songSelfId
						}
						copySong["name"] = song.Name

						newSongs = append(newSongs, copySong)
						modified = true
					}
				}
				newSongs = append(newSongs, neteaseSongs...)
				orginalMap[orginalSongsKey] = newSongs

			}

		}

	}
	return modified
}
