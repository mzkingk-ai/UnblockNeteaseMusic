package processor

import (
	"encoding/json"
	"testing"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

// 真机抓到的 /api/music-vip-membership/client/vip/info（PC 端，账号是过期黑胶 V3）。
const vipInfoClientSample = `{"message":"成功","code":200,"data":{"relationType":0,"relationOtherUserId":0,
"redVipLevel":3,"now":1700000000000,"redVipAnnualCount":-1,
"musicPackage":{"vipCode":220,"expireTime":1700000000000,"iconUrl":null,"dynamicIconUrl":null,"vipLevel":3,
"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false},
"associator":{"vipCode":100,"expireTime":1700000000000,"iconUrl":null,"dynamicIconUrl":null,"vipLevel":3,
"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false},
"oldCacheProtocol":false,"relationOtherUserRedVipExpireTime":0,"userId":0}}`

// 真机抓到的 /api/music-vip-membership/front/vip/info（iOS 端）。
// 关键区别：data 里没有 now 字段，早期实现直接 n.(json.Number).Int64() 会 panic。
const vipInfoFrontSample = `{"code":200,"data":{
"associator":{"dynamicIconUrl":"","expireTime":1700000000000,"iconUrl":"https://example.com/asset.png",
"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false,"vipCode":100,"vipLevel":3},
"familyVip":{"dynamicIconUrl":null,"expireTime":0,"iconUrl":null,"isSign":false,"isSignDeduct":false,
"isSignIap":false,"isSignIapDeduct":false,"vipCode":600,"vipLevel":0},
"musicPackage":{"dynamicIconUrl":"","expireTime":1700000000000,"iconUrl":"https://example.com/asset.png",
"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false,"vipCode":220,"vipLevel":3},
"redVipAnnualCount":-1,"redVipLevel":3,"userId":0}}`

func newTestNetease(path, body string) *Netease {
	netease := &Netease{Path: path}
	netease.JsonBody = utils.ParseJson([]byte(body))
	return netease
}

func withFlags(t *testing.T, vip, ads bool) {
	oldVip, oldAds := *config.EnableLocalVip, *config.BlockAds
	*config.EnableLocalVip, *config.BlockAds = vip, ads
	t.Cleanup(func() { *config.EnableLocalVip, *config.BlockAds = oldVip, oldAds })
}

func TestPatchVipInfoClient(t *testing.T) {
	withFlags(t, true, false)
	netease := newTestNetease("/api/music-vip-membership/client/vip/info", vipInfoClientSample)
	if !processInterest(netease) {
		t.Fatal("client/vip/info 应该被改写")
	}
	data := netease.JsonBody["data"].(common.MapType)
	for _, key := range []string{"musicPackage", "associator"} {
		pkg := data[key].(common.MapType)
		if got, _ := pkg["expireTime"].(json.Number).Int64(); got < 4_000_000_000_000 {
			t.Fatalf("%s.expireTime 没有被推到远期: %v", key, pkg["expireTime"])
		}
		if pkg["vipLevel"].(json.Number).String() != "7" {
			t.Fatalf("%s.vipLevel 不是 7: %v", key, pkg["vipLevel"])
		}
	}
}

func TestPatchVipInfoFrontWithoutNow(t *testing.T) {
	withFlags(t, true, false)
	// 这一条在早期实现里会 panic：interface conversion: interface {} is nil, not json.Number
	netease := newTestNetease("/api/music-vip-membership/front/vip/info", vipInfoFrontSample)
	if !processInterest(netease) {
		t.Fatal("front/vip/info 应该被改写")
	}
	data := netease.JsonBody["data"].(common.MapType)
	pkg := data["musicPackage"].(common.MapType)
	if got, _ := pkg["expireTime"].(json.Number).Int64(); got < 4_000_000_000_000 {
		t.Fatalf("expireTime 没被改写: %v", pkg["expireTime"])
	}
	// iconUrl 这类字段必须保留，旧实现整体替换对象会丢
	if pkg["iconUrl"] == nil {
		t.Fatal("musicPackage.iconUrl 被抹掉了")
	}
	// familyVip 没开过，不应该被顺手改成会员
	family := data["familyVip"].(common.MapType)
	if got, _ := family["expireTime"].(json.Number).Int64(); got != 0 {
		t.Fatalf("familyVip.expireTime 不该被改: %v", got)
	}
}

func TestClearDeliveryPopup(t *testing.T) {
	withFlags(t, false, true)
	sample := `{"message":"成功","code":200,"data":{"resourceData":{"title":"开通VIP畅听完整版",
"buttonText":"VIP仅¥0.22/天","positionName":"试听弹窗"},"logContext":"{}"}}`
	netease := newTestNetease("/api/delivery/deliver", sample)
	if !processInterest(netease) {
		t.Fatal("delivery 投放位应该被清空")
	}
	if got := netease.JsonBody["data"].(common.MapType)["resourceData"]; got != nil {
		t.Fatalf("resourceData 应该是 null，实际 %v", got)
	}
}

func TestClearBannerRules(t *testing.T) {
	withFlags(t, false, true)
	sample := `{"code":200,"trp":{"rules":["BANNER_PC_V2::x"]},"banners":[{"adid":1},{"id":2}]}`
	netease := newTestNetease("/api/v2/banner/get", sample)
	if !processInterest(netease) {
		t.Fatal("横幅应该被清空")
	}
	if n := len(netease.JsonBody["banners"].(common.SliceType)); n != 0 {
		t.Fatalf("banners 应该清空，实际剩 %d", n)
	}
	if n := len(netease.JsonBody["trp"].(common.MapType)["rules"].(common.SliceType)); n != 0 {
		t.Fatalf("trp.rules 应该清空，实际剩 %d", n)
	}
}

func TestNoFlagsNoChange(t *testing.T) {
	withFlags(t, false, false)
	netease := newTestNetease("/api/music-vip-membership/client/vip/info", vipInfoClientSample)
	if processInterest(netease) {
		t.Fatal("开关关着时不该改任何东西")
	}
}
