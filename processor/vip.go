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
	vipInfoPaths = []string{
		"/api/music-vip-membership/client/vip/info",
		"/api/music-vip-membership/front/vip/info",
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
	if *config.EnableLocalVip && pathIn(path, vipInfoPaths) {
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
	case *config.EnableLocalVip && pathIn(netease.Path, vipInfoPaths):
		return patchVipInfo(netease)
	case *config.BlockAds && pathHasPrefix(netease.Path, adPathPrefixes):
		return clearAds(netease)
	case *config.BlockAds && pathIn(netease.Path, bannerPaths):
		return clearBanners(netease)
	case *config.BlockAds && pathIn(netease.Path, deliveryPaths):
		return clearDelivery(netease)
	}
	return false
}

// patchVipInfo 把会员信息里的到期时间挪到 100 年后，并补齐等级字段。
// 只改这几个字段、保留其它字段（客户端有些页面会读 iconUrl / vipCode 之类）。
func patchVipInfo(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	data, ok := netease.JsonBody["data"].(common.MapType)
	if !ok {
		return false
	}
	// front/vip/info 的 data 里没有 now 字段，直接 n.(json.Number).Int64()
	// 会 panic（interface conversion: interface {} is nil）——这里必须用双返回值断言。
	now := int64(0)
	if n, ok := data["now"].(json.Number); ok {
		now, _ = n.Int64()
	}
	if now <= 0 {
		now = time.Now().UnixNano() / 1e6
	}
	expire := now + vipExtendMillis
	modified := false
	if _, ok := data["redVipLevel"]; ok {
		data["redVipLevel"] = json.Number("7")
		data["redVipAnnualCount"] = json.Number("1")
		modified = true
	}
	for _, key := range []string{"musicPackage", "associator", "redVip", "vipPackage"} {
		pkg, ok := data[key].(common.MapType)
		if !ok {
			continue
		}
		pkg["expireTime"] = json.Number(strconv.FormatInt(expire, 10))
		if _, ok := pkg["vipLevel"]; ok {
			pkg["vipLevel"] = json.Number("7")
		}
		if _, ok := pkg["isSign"]; ok {
			pkg["isSign"] = false
		}
		modified = true
	}
	if modified {
		log.Println("localVIP has been triggered(" + netease.Path + ").")
	}
	return modified
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
