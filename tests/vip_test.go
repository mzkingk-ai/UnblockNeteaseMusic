// Package tests 放跨包的黑盒测试：这里只通过 processor.RequestAfter 这个对外的
// 入口验证「会员信息改写 / 弹窗与广告清理」，不依赖 processor 的内部函数，
// 所以可以单独放在源码目录之外，不跟正常源码混在一起。
package tests

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/processor"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/processor/crypto"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

// 网易 eapi 的公开常量（processor 包里的 eApiKey），测试里复制一份即可。
const eapiKey = "e82ckenh8dichen8"

// 真机抓到的 /api/music-vip-membership/client/vip/info（PC 端，账号是过期黑胶 V3）。
const vipInfoClientSample = `{"message":"成功","code":200,"data":{"relationType":0,"relationOtherUserId":0,` +
	`"redVipLevel":3,"now":1700000000000,"redVipAnnualCount":-1,` +
	`"musicPackage":{"vipCode":220,"expireTime":1700000000000,"iconUrl":null,"dynamicIconUrl":null,"vipLevel":3,` +
	`"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false},` +
	`"associator":{"vipCode":100,"expireTime":1700000000000,"iconUrl":null,"dynamicIconUrl":null,"vipLevel":3,` +
	`"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false},` +
	`"oldCacheProtocol":false,"relationOtherUserRedVipExpireTime":0,"userId":0}}`

// 真机抓到的 /api/music-vip-membership/front/vip/info（iOS 端）。
// 关键区别：data 里没有 now 字段——早期实现直接用单返回值断言会 panic。
const vipInfoFrontSample = `{"code":200,"data":{` +
	`"associator":{"dynamicIconUrl":"","expireTime":1700000000000,"iconUrl":"https://example.com/asset.png",` +
	`"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false,"vipCode":100,"vipLevel":3},` +
	`"familyVip":{"dynamicIconUrl":null,"expireTime":0,"iconUrl":null,"isSign":false,"isSignDeduct":false,` +
	`"isSignIap":false,"isSignIapDeduct":false,"vipCode":600,"vipLevel":0},` +
	`"musicPackage":{"dynamicIconUrl":"","expireTime":1700000000000,"iconUrl":"https://example.com/asset.png",` +
	`"isSign":false,"isSignDeduct":false,"isSignIap":false,"isSignIapDeduct":false,"vipCode":220,"vipLevel":3},` +
	`"redVipAnnualCount":-1,"redVipLevel":3,"userId":0}}`

const deliveryPopupSample = `{"message":"成功","code":200,"data":{` +
	`"resourceData":{"title":"开通VIP畅听完整版","buttonText":"VIP仅¥0.22/天","positionName":"试听弹窗"},` +
	`"logContext":"{}"}}`

const bannerSample = `{"code":200,"trp":{"rules":["BANNER_PC_V2::x"]},"banners":[{"adid":1},{"id":2}]}`

// withFlags 临时改全局开关，测试结束恢复。
func withFlags(t *testing.T, localVip, blockAds bool) {
	t.Helper()
	oldVip, oldAds := *config.EnableLocalVip, *config.BlockAds
	*config.EnableLocalVip, *config.BlockAds = localVip, blockAds
	t.Cleanup(func() { *config.EnableLocalVip, *config.BlockAds = oldVip, oldAds })
}

// runRequestAfter 模拟 proxy 侧拿到一条加密响应后交给 RequestAfter 处理，
// 返回处理后的明文 JSON（已解回 ECB 密文的原文）。
func runRequestAfter(t *testing.T, path, plainBody string) []byte {
	t.Helper()
	encrypted := crypto.AesEncryptECB([]byte(plainBody), []byte(eapiKey))
	request, err := http.NewRequest("POST", "https://example.com/asset.png"+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	response := &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
		Body:       ioutil.NopCloser(bytes.NewReader(encrypted)),
	}
	netease := &processor.Netease{Path: path, Params: map[string]interface{}{}}
	processor.RequestAfter(request, response, netease)
	out, err := ioutil.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读回响应失败: %v", err)
	}
	if plain, ok := crypto.AesDecryptECB(out, []byte(eapiKey)); ok {
		return plain
	}
	return out
}

func decode(t *testing.T, body []byte) common.MapType {
	t.Helper()
	// 用项目自己的 ParseJson（decoder.UseNumber），否则数字会变成 float64，
	// 跟线上代码里的 json.Number 断言对不上。
	m := utils.ParseJson(body)
	if m == nil {
		t.Fatalf("响应不是合法 JSON 对象: %s", strings.TrimSpace(string(body)))
	}
	return m
}

func TestLocalVipOnClientVipInfo(t *testing.T) {
	withFlags(t, true, false)
	body := decode(t, runRequestAfter(t, "/api/music-vip-membership/client/vip/info", vipInfoClientSample))
	data := body["data"].(common.MapType)
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

// TestLocalVipOnFrontVipInfo 盯的就是当初那枚 panic：
// front/vip/info 的 data 里没有 now，早期实现 interface {} is nil, not json.Number。
func TestLocalVipOnFrontVipInfo(t *testing.T) {
	withFlags(t, true, false)
	body := decode(t, runRequestAfter(t, "/api/music-vip-membership/front/vip/info", vipInfoFrontSample))
	data := body["data"].(common.MapType)
	pkg := data["musicPackage"].(common.MapType)
	if got, _ := pkg["expireTime"].(json.Number).Int64(); got < 4_000_000_000_000 {
		t.Fatalf("expireTime 没被改写: %v", pkg["expireTime"])
	}
	if pkg["iconUrl"] == nil {
		t.Fatal("musicPackage.iconUrl 被抹掉了（旧实现整体替换对象会丢字段）")
	}
	// familyVip 没开过，不应该被顺手改成会员
	family := data["familyVip"].(common.MapType)
	if got, _ := family["expireTime"].(json.Number).Int64(); got != 0 {
		t.Fatalf("familyVip.expireTime 不该被改: %v", got)
	}
}

func TestBlockDeliveryPopup(t *testing.T) {
	withFlags(t, false, true)
	body := decode(t, runRequestAfter(t, "/api/delivery/deliver", deliveryPopupSample))
	if got := body["data"].(common.MapType)["resourceData"]; got != nil {
		t.Fatalf("resourceData 应该是 null，实际 %v", got)
	}
}

func TestBlockBannerRules(t *testing.T) {
	withFlags(t, false, true)
	body := decode(t, runRequestAfter(t, "/api/v2/banner/get", bannerSample))
	if n := len(body["banners"].(common.SliceType)); n != 0 {
		t.Fatalf("banners 应该清空，实际剩 %d", n)
	}
	if n := len(body["trp"].(common.MapType)["rules"].(common.SliceType)); n != 0 {
		t.Fatalf("trp.rules 应该清空，实际剩 %d", n)
	}
}

func TestNoFlagsKeepsData(t *testing.T) {
	withFlags(t, false, false)
	body := decode(t, runRequestAfter(t, "/api/music-vip-membership/client/vip/info", vipInfoClientSample))
	data := body["data"].(common.MapType)
	if got, _ := data["musicPackage"].(common.MapType)["expireTime"].(json.Number).Int64(); got != 1700000000000 {
		t.Fatalf("开关关着时不该改数据，实际 expireTime=%v", got)
	}
}
