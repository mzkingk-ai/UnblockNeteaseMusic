// Package pyncmd 对接第三方网易云 API（music-api.gdstudio.xyz），用网易自己的
// songId 直接换一条可播地址。
//
// 它和 kuwo/kugou/migu/qq 这些音源有本质区别，也是它存在的理由：
//
//	换源类音源的流程是「拿歌名+歌手去别的平台搜 → 打分 → 赌哪条是原唱」，
//	原唱没版权时要么搜不到，要么搜到同名翻唱（《晴天》曾匹配到 0.65 分的
//	「陈星晨 - Future War未来战争」，《黄昏》前 8 条全是翻唱）。
//
//	pyncmd 不做搜索，直接用 songId 换地址，拿回来的就是这首歌本身。
//	实测 md5 与网易原址一致（《分身情人》0e32bb2e…、《少年游》432dd838…），
//	不存在翻唱冒充原唱的可能。
//
// 代价是单点依赖第三方服务，实测约 1.6s 返回、偶发 503，故这里做了重试；
// 解析失败时返回空 Song，由上层回落其它音源，绝不返回一条错的地址。
package pyncmd

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/network"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

type PyNcmd struct{}

// ApiURL 是带 %s(id) 和 %s(br) 两个占位符的模板，可用 httptest 指向本地样本做回归测试。
// 用 var 而非 const：测试要把它替换成本地 server。
var ApiURL = "https://music-api.gdstudio.xyz/api.php?types=url&source=netease&id=%s&br=%s"

const (
	// 只依赖网易 songId，所以只对 netease 有意义（joox 的 id 体系在这里用不上）。
	sourceName = "pyncmd"
	// 单次请求上限，两档：
	//   走代理时 6s —— 实测代理下 0.5~1.1s 就回，6s 足够，超了就是代理本身有问题
	//   直连时  20s —— 实测直连 1.2~3.3s，但会偶发挂到 17.9s，窗口必须够长
	// 它同时也是 responseHeaderTimeout 的取值，否则会被 network 里全局的 5s 提前截断。
	proxyTimeout  = 6 * time.Second
	directTimeout = 20 * time.Second
	// 503 是实测到的偶发失败（20 次里 1 次）。有代理时两档都试一遍（直连/代理
	// 各一次），没代理时就只有直连这一个来源，重试一次兜网络抖动。
	maxAttempts = 2
)

// brOf 把网易的码率请求映射成第三方 API 的 br 参数。
// 不传 br 时它直接给 flac（br=993），所以不指定音质时不要传值。
func brOf(q common.MusicQuality) string {
	switch q {
	case common.Lossless:
		return "999"
	case common.ExHigh:
		return "320"
	case common.Higher:
		return "192"
	default:
		return "128"
	}
}

// GetSongUrl 用 songId 换地址。searchSong.Id 必须是纯网易数字 id，
// 带 9000x 前缀的平台 id（酷我/酷狗结果）在这里没有意义。
func (p *PyNcmd) GetSongUrl(searchSong common.SearchMusic, song *common.Song) *common.Song {
	if song == nil {
		return &common.Song{}
	}
	id := song.Id
	if len(id) == 0 {
		id = searchSong.Id
	}
	id = strings.TrimSpace(id)
	if !isNeteaseId(id) {
		log.Printf("pyncmd: skip non-netease id %q\n", id)
		return &common.Song{}
	}

	br := brOf(searchSong.Quality)
	apiUrl := fmt.Sprintf(ApiURL, id, br)

	// 走哪条路由在启动时就定了（见 config.resolveProxy），这里不再二选一：
	//   有代理 → 全程走代理，6s 超时（实测 0.5~1.1s，超了就是代理坏了）
	//   没代理 → 全程直连，20s 超时（实测 1.2~3.3s，偶发挂到 17.9s）
	// 以前写成「直连失败再走代理兜底」，但直连最坏要等满 20s 才失败，
	// 那时候 resolveSong 的窗口早关了，兜底等于没兜。
	proxyAddr := common.ProxyAddr
	timeout := directTimeout
	via := "direct"
	if proxyAddr != "" {
		timeout = proxyTimeout
		via = "proxy " + proxyAddr
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := fetch(apiUrl, proxyAddr, timeout)
		if err != nil {
			lastErr = err
			log.Printf("pyncmd: id=%s attempt %d/%d via %s failed: %v\n", id, attempt, maxAttempts, via, err)
			// 只有超时和 5xx 值得重试；参数错误重试也没用。
			if !retryable(err) {
				break
			}
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		url, _ := result["url"].(string)
		if len(url) == 0 {
			// 该服务没有这个 id 的资源（实测《冬天的秘密》《成都》返回空）。
			// 这不是错误，直接放行给其它音源。
			log.Printf("pyncmd: id=%s has no url\n", id)
			return &common.Song{}
		}
		if !isPublicUrl(url) {
			log.Printf("pyncmd: id=%s returned non-public url %q\n", id, url)
			return &common.Song{}
		}

		out := *song
		out.Url = url
		out.Source = sourceName
		// 换回来的就是网易原文件，HTTP 上不做任何匹配，给满分。
		// 这样 provider.Find 里「MatchScore 最高者胜」和 FilterByScore 的 0.75 闸门
		// 都不会把它误杀。
		out.MatchScore = 1.0
		if br, ok := numberFrom(result["br"]); ok && br > 0 {
			out.Br = int(br)
		}
		if size, ok := numberFrom(result["size"]); ok && size > 0 {
			out.Size = int64(size)
		}
		// 校验响应里说的 id 和我们要的一致，防止第三方服务串号。
		if got, ok := result["id"]; ok {
			if gid, ok := numberFrom(got); ok && gid > 0 {
				var want int64
				if _, err := fmt.Sscanf(id, "%d", &want); err == nil && int64(gid) != want {
					log.Printf("pyncmd: id mismatch want=%s got=%v\n", id, gid)
					return &common.Song{}
				}
			}
		}
		log.Printf("pyncmd: id=%s ok br=%d size=%d\n", id, out.Br, out.Size)
		return &out
	}
	if lastErr != nil {
		log.Printf("pyncmd: id=%s give up: %v\n", id, lastErr)
	}
	return &common.Song{}
}

// SearchSong 故意返回空：pyncmd 不是搜索型音源。
// 让它参与 getSongFromAllSource 只会白白增加一次网络请求和 6s 超时窗口。
func (p *PyNcmd) SearchSong(searchSong common.SearchSong) (songs []*common.Song) {
	return nil
}

// ParseSong 走 provider.Find 的「按歌曲详情反查音源」入口。
// 只有 Id 是纯网易 id 时才有意义，否则返回空交给换源音源。
//
// 注意 common.SearchSong 没有 Id 字段，songId 在处理器里是走
// provider.Find(common.SearchMusic{Id: ...}) 传进来的；这里的 songId
// 只是作为兜底，正常情况下 Find 已经先行调用过 GetSongUrl。
func (p *PyNcmd) ParseSong(searchSong common.SearchSong) *common.Song {
	return &common.Song{}
}

type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("pyncmd http %d: %s", e.code, e.msg)
}

func retryable(err error) bool {
	if e, ok := err.(*apiError); ok {
		return e.code >= 500
	}
	// 网络层错误（超时、连接被重置）值得重试。
	return true
}

// fetch 发一次请求。proxyAddr 非空时走该代理（形如 http://127.0.0.1:7890），
// 空串表示按默认（直连 / 环境变量）。
func fetch(apiUrl string, proxyAddr string, timeout time.Duration) (common.MapType, error) {
	header := make(http.Header, 2)
	header["accept"] = append(header["accept"], "application/json")
	clientRequest := network.ClientRequest{
		Method:         http.MethodGet,
		RemoteUrl:      apiUrl,
		Header:         header,
		Proxy:          false,
		ConnectTimeout: timeout,
		// 全局的 ResponseHeaderTimeout 是 5s，对这个慢接口会把每次请求都截断，
		// 必须显式放宽，否则会被全局 5s 截断。
		ResponseHeaderTimeout: timeout,
		HTTPProxy:             proxyAddr,
		ForbiddenEncodeQuery:  true, // 查询串已编码好，别让 http 再编码一遍
	}
	resp, err := network.Request(&clientRequest)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &apiError{code: resp.StatusCode, msg: resp.Status}
	}
	body, err := network.GetResponseBody(resp, false)
	if err != nil {
		return nil, err
	}
	result := utils.ParseJson(body)
	if result == nil {
		return nil, fmt.Errorf("pyncmd: unparsable body")
	}
	return result, nil
}

// isNeteaseId 判断是否纯数字且不是平台前缀 id。
//
// 90000/90001/90002/90003 是 UNM 给酷我/咪咕/酷狗/QQ 结果自造的 id 前缀，
// 拿它去问网易必然是空，白白多一次网络请求。
// 不能靠长度判断：酷我 id 是 90000 + 7 位 rid，正好也是 10 位，
// 和 10 位的网易超大 id（如 25641019 是 8 位，1998358259 也是 10 位）撞车。
func isNeteaseId(id string) bool {
	if len(id) == 0 || len(id) > 10 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	if id == "0" {
		return false
	}
	// 平台前缀：9000 + 一位源号（0=酷我 1=咪咕 2=酷狗 3=QQ）。
	// 网易 id 从 9000 万起跳到这个量级时要 9 位以上，且不可能恰好以
	// 90000/90001/90002/90003 开头后还紧跟 6~7 位——真碰上就多一次空查询，无害。
	if len(id) >= 8 {
		switch id[:5] {
		case "90000", "90001", "90002", "90003":
			return false
		}
	}
	return true
}

// isPublicUrl 拒绝内网/回环地址。
// 这条护栏直接对应本轮修复的那个 bug：上游 UNM 曾把 music.163.com 解析到
// 192.168.3.1（路由器自己），拿到一个指向自己的地址后无限回环超时。
func isPublicUrl(raw string) bool {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return false
	}
	host := raw
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if len(host) == 0 {
		return false
	}
	switch host {
	case "localhost", "0.0.0.0", "::1", "::":
		return false
	}
	// 只拦 IPv4 字面量；域名交给 DNS 和 network 层。
	parts := strings.Split(host, ".")
	if len(parts) == 4 {
		var a, b int
		if _, err := fmt.Sscanf(host, "%d.%d", &a, &b); err == nil {
			if a == 10 || a == 127 || (a == 192 && b == 168) || (a == 172 && b >= 16 && b <= 31) || (a == 169 && b == 254) {
				return false
			}
		}
	}
	return true
}

func numberFrom(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case string:
		var i int64
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}
