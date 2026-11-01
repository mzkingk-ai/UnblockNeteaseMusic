package common

import (
	"math/rand"
	"net"
	"strings"
	"time"
)

type MapType = map[string]interface{}
type SliceType = []interface{}
type Song struct {
	Id                string
	Size              int64
	Br                int
	Url               string
	Md5               string
	Name              string
	Artist            string
	AlbumName         string
	MatchScore        float32
	Source            string
	PlatformUniqueKey MapType `json:"-"`
}
type SongSlice []*Song

func (a SongSlice) Len() int {
	return len(a)
}
func (a SongSlice) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}
func (a SongSlice) Less(i, j int) bool {
	return a[j].MatchScore < a[i].MatchScore
}

type SearchSong struct {
	Keyword     string
	Name        string
	ArtistsName string
	Quality     MusicQuality
	OrderBy     SearchOrderBy
	Limit       int
	ArtistList  []string
}
type PlatformIdTag string

const (
	StartTag PlatformIdTag = "9000"
	KuWoTag  PlatformIdTag = "90000"
	MiGuTag  PlatformIdTag = "90001"
	KuGouTag PlatformIdTag = "90002"
	QQTag    PlatformIdTag = "90003"
)

type SearchOrderBy int32

const (
	MatchedScoreDesc SearchOrderBy = iota
	PlatformDefault
)

type MusicQuality int32

const (
	Standard MusicQuality = iota
	Higher
	ExHigh
	Lossless
)

type SearchMusic struct {
	Quality MusicQuality
	Id      string
}

func (m MusicQuality) String() string {
	switch m {
	case Standard:
		return "Standard(0)"
	case Higher:
		return "Higher(1)"
	case ExHigh:
		return "ExHigh(2)"
	case Lossless:
		return "Lossless(3)"
	default:
		return "UNKNOWN"
	}
}

// MinMatchScore 低于此分的匹配结果一律丢弃。
// 上游 CalScore 没有阈值下限，ParseSong 会无条件采纳搜索列表第一条，
// 导致「原唱无版权」时静默播放同名翻唱（实测《晴天》匹配到 0.65 分的
// 「陈星晨 - Future War未来战争」）。宁可不给，也不给错的。
var MinMatchScore float32 = 0.75

var (
	ProxyIp = "127.0.0.1"
	// ProxyAddr 是本机 HTTP 代理地址，形如 "http://127.0.0.1:7890"。
	// 启动时探测端口决定：探测到就填上，pyncmd 直接走它（超时 6s）；
	// 探测不到就留空，pyncmd 走直连（超时 20s）。
	// 为什么不直接读 HTTP_PROXY 环境变量：那个变量会影响所有请求
	// （含 kuwo/kugou 这类本来就快的音源），而它们走代理反而更慢。
	ProxyAddr = ""
	// 这张表就是「哪些域名归 UNM 处理」的白名单，serveProxy 靠它决定
	// 走 processor（改写播放地址）还是原样透传。
	// 名单必须覆盖所有客户端版本实际使用的接口域名，漏一个，那个客户端的
	// 全部 API 都会走下面的「透传」分支，播放地址永远拿不到解锁结果。
	//   网页/老版 PC/Android 老版 -> music.163.com, interface.music.163.com
	//   新版 PC 客户端            -> interfacepc.music.163.com（2026-11-01 实测遗漏）
	//   iOS                      -> interface3.music.163.com
	ProxyDomain = map[string]string{
		"music.163.com":             "59.111.181.35",
		"interface.music.163.com":   "59.111.181.35",
		"interface3.music.163.com":  "59.111.181.35",
		"interface4.music.163.com":  "59.111.181.35",
		"interface9.music.163.com":  "59.111.181.35",
		"interfacepc.music.163.com": "59.111.181.35",
		"apm.music.163.com":         "59.111.181.35",
		"apm3.music.163.com":        "59.111.181.35",
	}
	HostDomain = map[string]string{
		"music.163.com":             "59.111.181.35",
		"interface.music.163.com":   "59.111.181.35",
		"interface3.music.163.com":  "59.111.181.35",
		"interface4.music.163.com":  "59.111.181.35",
		"interface9.music.163.com":  "59.111.181.35",
		"interfacepc.music.163.com": "59.111.181.35",
	}

	Source []string
	Rand   = rand.New(
		rand.NewSource(time.Now().UnixNano()))
)

// HostNameOf 按 HostDomain 反查：给定一个 IP（可带端口），返回它对应的网易域名。
// serveProxy 把域名换成 IP 转发后，TLS 的 SNI 会跟着变成 IP，
// 而网易证书只有 DNS SAN、没有 IP SAN，必须靠这里找回原域名填 ServerName。
// 查不到时返回空串，交给调用方按默认处理。
func HostNameOf(hostWithPort string) string {
	h := hostWithPort
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h, "]") {
		h = h[:i]
	}
	if net.ParseIP(h) == nil {
		return h
	}
	for domain, ip := range HostDomain {
		if ip == h {
			return domain
		}
	}
	return ""
}
