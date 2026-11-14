package processor

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
)

// 这一组接口平时不在 Path 白名单里，请求体不会被处理。
// -lv（本地 VIP）/ -ba（去广告）打开时把它们也纳入处理流程，
// 否则客户端看到的一直是"未开通会员"的原始响应。
var (
	// 会员状态不只出现在 vip/info 一个接口里：续费页签读的是
	// vip-center-bff/float/data 里的 data.vipInfo，等级卡读的是
	// vipnewcenter/.../growhpoint/basic 里的 data.userLevel，音质/权益卡读
	// vip-center-bff/quality/auth/info。它们都是「带 vip 字样的接口」，
	// 所以这里按路径关键字统一放行，再对响应做深度改写。
	vipPathKeywords = []string{
		"vip",
	}
	adPathPrefixes = []string{
		"/api/ad/",
	}
	bannerPaths = []string{
		"/api/v2/banner/get",
	}
	deliveryPaths = []string{
		"/api/delivery/deliver",
		"/api/delivery/batch-deliver",
		"/api/delivery/cashier/alg/popup",
	}
	// 会员到期时间往后推 100 年（毫秒）。
	vipExtendMillis int64 = 3162240000000
)

func pathIn(path string, list []string) bool {
	for _, p := range list {
		if strings.EqualFold(path, p) {
			return true
		}
	}
	return false
}

func pathHasPrefix(path string, list []string) bool {
	for _, p := range list {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// patchablePath 判断这个接口在当前的开关下是否值得进处理流程。
func patchablePath(path string) bool {
	if *config.EnableLocalVip && isVipPath(path) {
		return true
	}
	if *config.BlockAds && (pathHasPrefix(path, adPathPrefixes) ||
		pathIn(path, bannerPaths) || pathIn(path, deliveryPaths)) {
		return true
	}
	return false
}

// processInterest 处理会员状态与广告/弹窗类响应。返回是否改写了响应。
func processInterest(netease *Netease) bool {
	switch {
	case *config.EnableLocalVip && isVipPath(netease.Path):
		return patchVipData(netease)
	case *config.BlockAds && pathHasPrefix(netease.Path, adPathPrefixes):
		return clearAds(netease)
	case *config.BlockAds && pathIn(netease.Path, bannerPaths):
		return clearBanners(netease)
	case *config.BlockAds && pathIn(netease.Path, deliveryPaths):
		return clearDelivery(netease)
	}
	return false
}

func isVipPath(path string) bool {
	for _, kw := range vipPathKeywords {
		if strings.Contains(path, kw) {
			return true
		}
	}
	return false
}

// patchVipData 对会员相关响应做深度改写。
//
// 为什么不再只改 vip/info：客户端判断「是不是会员」用的是好几个接口里的
// expireTime，只要有一处还是过去的日期，页面就会显示「已失去黑胶VIP特权 41 天」。
// 实测带 expireTime 的接口一共六类（vip/info 两版、vip-center-bff/float/data 里的
// data.vipInfo、vip-center-bff/quality/auth/info 里的 data.vipInfo、
// vipnewcenter/.../growhpoint/basic 里的 data.userLevel、vipauth/app/auth/query
// 里的每条权益），所以这里顺着 JSON 树走一遍，见到会员结构就改。
func patchVipData(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	now := time.Now().UnixNano() / 1e6
	if n, ok := netease.JsonBody["data"].(common.MapType); ok {
		// client/vip/info 会带服务端时间，优先用它，避免设备时钟偏差。
		if v, ok := n["now"].(json.Number); ok {
			if ms, err := v.Int64(); err == nil && ms > 0 {
				now = ms
			}
		}
	}
	if !patchVipNode("", netease.JsonBody, now) {
		return false
	}
	log.Println("localVIP has been triggered(" + netease.Path + ").")
	return true
}

// 这些键下面挂的是「会员包」对象，到期时间一律往后推（包括从没开通过的 0）。
var vipPackageKeys = []string{"musicPackage", "associator", "redVip", "vipPackage", "redplus", "vipInfo"}

// patchVipNode 递归走 JSON 树，按结构特征识别会员数据并改写。
func patchVipNode(key string, node interface{}, now int64) bool {
	switch v := node.(type) {
	case common.MapType:
		return patchVipMap(key, v, now)
	case *common.MapType:
		if v == nil {
			return false
		}
		return patchVipMap(key, *v, now)
	case common.SliceType:
		modified := false
		for _, item := range v {
			if patchVipNode(key, item, now) {
				modified = true
			}
		}
		return modified
	}
	return false
}

func patchVipMap(key string, m common.MapType, now int64) bool {
	expire := json.Number(strconv.FormatInt(now+vipExtendMillis, 10))
	modified := false

	// 1) 会员包：musicPackage / associator / redVip / vipInfo ...
	if pathIn(key, vipPackageKeys) {
		if _, ok := m["expireTime"]; ok {
			m["expireTime"] = expire
			modified = true
		}
		if _, ok := m["isSign"]; ok {
			m["isSign"] = false
			modified = true
		}
		if lv, ok := toInt(m["vipLevel"]); ok && lv > 0 {
			m["vipLevel"] = json.Number("7")
			modified = true
		}
	}

	// 2) 任何自带 vipCode 的对象（包括上面那些包，幂等）
	if _, ok := m["vipCode"]; ok {
		if old, ok := toInt(m["expireTime"]); ok && old > 0 && old < now {
			m["expireTime"] = expire
			modified = true
		}
	}

	// 3) 黑胶等级字段（vip/info 里有，float/data 里也有）
	if _, ok := m["redVipLevel"]; ok {
		m["redVipLevel"] = json.Number("7")
		modified = true
	}
	if _, ok := m["redVipAnnualCount"]; ok {
		m["redVipAnnualCount"] = json.Number("1")
		modified = true
	}

	// 4) 等级卡 userLevel：vipType 为负数表示「已过期」，latestVipStatus 0 同义
	if _, ok := m["growthPoint"]; ok {
		if _, ok := m["expireTime"]; ok {
			m["expireTime"] = expire
			modified = true
		}
		if old, ok := toInt(m["vipType"]); ok && old < 0 {
			vipType := json.Number("100")
			if latest, ok := toInt(m["latestVipType"]); ok && latest > 0 {
				vipType = json.Number(strconv.FormatInt(latest, 10))
			}
			m["vipType"] = vipType
			modified = true
		}
		if _, ok := m["latestVipStatus"]; ok {
			m["latestVipStatus"] = json.Number("1")
			modified = true
		}
	}

	// 5) 权益清单（vipauth/app/auth/query）：canUse=false + 过去日期
	if _, ok := m["resourceType"]; ok {
		if canUse, ok := m["canUse"].(bool); ok && !canUse {
			m["canUse"] = true
			modified = true
		}
		if _, ok := m["canNotUseReasonCode"]; ok {
			m["canNotUseReasonCode"] = json.Number("0")
			modified = true
		}
		if old, ok := toInt(m["expireTime"]); ok && old < now {
			m["expireTime"] = expire
			modified = true
		}
	}

	for k, v := range m {
		if patchVipNode(k, v, now) {
			modified = true
		}
	}
	return modified
}

// toInt 把 JSON 里的数字字段读成 int64（ParseJson 用 UseNumber，所以是 json.Number）。
func toInt(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// clearAds 把广告接口的 ads/data 清空，客户端拿不到东西就不会渲染。
func clearAds(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	modified := false
	if _, ok := netease.JsonBody["ads"]; ok {
		netease.JsonBody["ads"] = &common.MapType{}
		modified = true
	}
	if data, ok := netease.JsonBody["data"].(common.MapType); ok {
		for _, key := range []string{"ads", "adList", "banners", "resources"} {
			if _, ok := data[key]; ok {
				data[key] = make(common.SliceType, 0)
				modified = true
			}
		}
	}
	if modified {
		log.Println("block Ad has been triggered(" + netease.Path + ").")
	}
	return modified
}

// clearBanners 清掉横幅里的广告位（trp.rules 是新的投放描述，光看 adid 会漏）。
func clearBanners(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	modified := false
	if banners, ok := netease.JsonBody["banners"].(common.SliceType); ok && len(banners) > 0 {
		netease.JsonBody["banners"] = make(common.SliceType, 0)
		modified = true
	}
	if trp, ok := netease.JsonBody["trp"].(common.MapType); ok {
		if rules, ok := trp["rules"].(common.SliceType); ok && len(rules) > 0 {
			trp["rules"] = make(common.SliceType, 0)
			modified = true
		}
	}
	if modified {
		log.Println("block banner Ad has been triggered(" + netease.Path + ").")
	}
	return modified
}

// clearDelivery 把投放位（试听弹窗、续费卡片）清空。
// 实测服务端自己也会返回 resourceData:null，说明 null 是客户端认的合法值。
func clearDelivery(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	data, ok := netease.JsonBody["data"].(common.MapType)
	if !ok {
		return false
	}
	modified := false
	if res, ok := data["resourceData"]; ok && res != nil {
		data["resourceData"] = nil
		modified = true
	}
	if list, ok := data["resourceDataList"].(common.SliceType); ok && len(list) > 0 {
		data["resourceDataList"] = make(common.SliceType, 0)
		modified = true
	}
	// /api/delivery/cashier/alg/popup 的 data 本身就是「位置 ID -> 弹窗内容」。
	if strings.HasSuffix(netease.Path, "/alg/popup") && len(data) > 0 {
		for k := range data {
			delete(data, k)
		}
		modified = true
	}
	if modified {
		log.Println("block delivery has been triggered(" + netease.Path + ").")
	}
	return modified
}
