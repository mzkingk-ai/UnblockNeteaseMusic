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
	// /api/v1/user_info 是「我的」页和会员入口的共同数据源。它的字段名和
	// vip/info 不一样：viptype=0 表示非会员，expiretime/backupExpireTime 是
	// 上一次到期的时间戳。客户端就是拿 backupExpireTime 和现在比，算出
	// 「已失去黑胶VIP特权 N 天」这句话（服务端从不下发这句话，所以抓包搜不到）。
	// 客户端的写法不统一：PC 走 /api/v1/user_info，iOS 走 /api/v1/user/info，
	// 冷启动还多一层 /api/page/cold/start/user/info/get（都带同样的字段）。
	userInfoKeywords = []string{
		"user/info",
		"user_info",
	}
	// userInfoVipType 是非会员账号改成会员时写的类型值：11 = 黑胶VIP。
	userInfoVipType = 11
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
	if *config.EnableLocalVip && (isVipPath(path) || isUserInfoPath(path)) {
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
	case *config.EnableLocalVip && isUserInfoPath(netease.Path):
		return patchUserInfo(netease)
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
	if cur, ok := netease.JsonBody["ads"]; ok {
		// 原来是什么形状就还它什么形状：/api/ad/loading/get 的 ads 是数组，
		// 换成对象的话，按强类型解析的客户端（iOS/Swift）会整包当成坏的，
		// 于是退回本地缓存的旧素材，开屏广告照样弹。
		netease.JsonBody["ads"] = emptyLike(cur)
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

// emptyLike 返回和原值同类型的空容器（数组给空数组，对象给空对象）。
func emptyLike(v interface{}) interface{} {
	switch v.(type) {
	case common.SliceType:
		return make(common.SliceType, 0)
	case common.MapType:
		return common.MapType{}
	}
	return common.MapType{}
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

// isUserInfoPath 判断是不是「当前用户信息」类接口。
func isUserInfoPath(path string) bool {
	for _, kw := range userInfoKeywords {
		if strings.Contains(path, kw) {
			return true
		}
	}
	return false
}

// patchUserInfo 把「我的」页拿到的账号状态改成会员。
//
// 字段不一定在顶层：冷启动那个 /page/cold/start/user/info/get 把它们包在
// data 里，所以这里按 key 递归找，只认 viptype / expiretime / backupExpireTime
// 这几个专有名字，不会误伤同一份报文里的其它字段。
func patchUserInfo(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	if !patchUserInfoMap(netease.JsonBody) {
		return false
	}
	log.Printf("localVIP has been triggered(%s).\n", netease.Path)
	return true
}

func patchUserInfoMap(m map[string]interface{}) bool {
	now := nowMillis()
	modified := false
	// 类型值只往上提，不往下压：真实会员（viptype 已经是正数）不动。
	if n, ok := toInt(m["viptype"]); ok && n <= 0 {
		m["viptype"] = json.Number(strconv.Itoa(userInfoVipType))
		modified = true
	}
	// expiretime 是当前有效期，backupExpireTime 是上一次到期时间。两个都推到
	// 100 年后，客户端才算「一直是会员」，不会去算「已失去 N 天」。
	for _, key := range []string{"expiretime", "expireTime", "backupExpireTime"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		n, ok := toInt(v)
		if !ok || n >= now+vipExtendMillis {
			continue
		}
		m[key] = json.Number(strconv.FormatInt(now+vipExtendMillis, 10))
		modified = true
	}
	for _, v := range m {
		switch child := v.(type) {
		case map[string]interface{}:
			modified = patchUserInfoMap(child) || modified
		case []interface{}:
			for _, item := range child {
				if sub, ok := item.(map[string]interface{}); ok {
					modified = patchUserInfoMap(sub) || modified
				}
			}
		}
	}
	return modified
}

func nowMillis() int64 { return time.Now().UnixNano() / 1e6 }
