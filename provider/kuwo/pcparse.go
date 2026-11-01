package kuwo

import (
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/network"
)

// kuwoMaxScan 是每条搜索结果里最多看多少条。上游是 min(len/2+1,5)，
// 在「歌名 + 歌手」这种组合检索下酷我经常把伴奏/DJ/合唱顶在前面，
// 原唱排到第 6 条以后就被截掉了（实测「成都 赵雷」原唱在第 2 条，
// 而同名前 5 条全是各种改版）。
const kuwoMaxScan = 8

// 酷我 PC 搜索接口返回的是单引号「伪 JSON」：
//
//	{'ARTISTPIC':'','HIT':'3860','abslist':[{'AARTIST':'Jay&nbsp;Chou', ...}, ...
//
// 双引号版（rformat=json + mobi=1）是合法 JSON 但结果被裁到 7~20 条，
// 所以这里手解 PC 版。字段值里不含单引号，按 'KEY':'VALUE' 取即可。
var (
	kuwoItemSep = regexp.MustCompile(`\{'AARTIST'`)
	kuwoField   = regexp.MustCompile(`'([A-Z0-9_]+)':'([^']*)'`)
)

// ParsePcAbsList 把 PC 接口的伪 JSON 解成与 mobi 接口同构的 common.MapType 列表，
// 这样下游取字段的代码不用改：MUSICRID='MUSIC_<rid>'、ARTIST/SONGNAME 仍是 HTML 实体。
func ParsePcAbsList(body string) []common.MapType {
	parts := kuwoItemSep.Split(body, -1)
	list := make([]common.MapType, 0, len(parts))
	for _, part := range parts[1:] {
		item := make(common.MapType, 8)
		found := false
		for _, m := range kuwoField.FindAllStringSubmatch(part, -1) {
			key := m[1]
			if key == "abslist" || key == "searchgroup" {
				break
			}
			item[key] = html.UnescapeString(m[2])
			if key == "MUSICRID" {
				found = true
			}
		}
		if found {
			list = append(list, item)
		}
	}
	return list
}

// FetchPcAbsList 拉取一次搜索结果并解析。network 层带 Referer 的默认头不值得，
// 这个接口裸请求即可；出错时返回 err 让调用方打日志跳过。
func FetchPcAbsList(searchUrl string, header http.Header) ([]common.MapType, error) {
	resp, err := network.Request(&network.ClientRequest{
		Method:    http.MethodGet,
		RemoteUrl: searchUrl,
		Header:    header,
		Proxy:     true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Println("kuwo search status:", resp.StatusCode)
		return nil, err
	}
	body, err := network.GetResponseBody(resp, false)
	if err != nil {
		return nil, err
	}
	return ParsePcAbsList(string(body)), nil
}

// CopyrightFree 判断这首歌在酷我上是不是「无版权」条目。
// PC 接口的 COPYRIGHT 字段：0 表示有版权/可播放，非 0 一般是无版权占位。
// 目前只在日志里用，便于排障时一眼看出为什么选/没选某条。
func CopyrightFree(item common.MapType) bool {
	if v, ok := item["COPYRIGHT"].(string); ok {
		return strings.TrimSpace(v) == "0"
	}
	return false
}
