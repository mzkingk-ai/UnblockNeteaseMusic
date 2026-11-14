// Package tests 放跨包的黑盒测试：只通过 processor.RequestAfter 这个对外入口
// 验证行为，不碰 processor 的内部函数，所以能独立放在源码目录之外。
package tests

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/processor"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/processor/crypto"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

// 网易 eapi 的公开常量（processor 包里的 eApiKey）。
const eapiKey = "e82ckenh8dichen8"

// 100 年后的毫秒时间戳，改写后的 expireTime 至少要比这个大才算「推到远期」。
const farFuture = 4_000_000_000_000

func withFlags(t *testing.T, localVip, blockAds bool) {
	t.Helper()
	oldVip, oldAds := *config.EnableLocalVip, *config.BlockAds
	*config.EnableLocalVip, *config.BlockAds = localVip, blockAds
	t.Cleanup(func() { *config.EnableLocalVip, *config.BlockAds = oldVip, oldAds })
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := ioutil.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 testdata 失败: %v", err)
	}
	return body
}

// runRequestAfter 模拟 proxy 拿到一条 ECB 加密响应后交给 RequestAfter，
// 再把处理完的响应解回明文。
func runRequestAfter(t *testing.T, path string, plainBody []byte) common.MapType {
	t.Helper()
	encrypted := crypto.AesEncryptECB(plainBody, []byte(eapiKey))
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
	plain, ok := crypto.AesDecryptECB(out, []byte(eapiKey))
	if !ok {
		plain = out
	}
	// 用项目自己的 ParseJson（UseNumber），否则数字是 float64，跟线上断言对不上。
	body := utils.ParseJson(plain)
	if body == nil {
		t.Fatalf("响应不是合法 JSON 对象: %s", string(plain))
	}
	return body
}

func num(t *testing.T, m common.MapType, key string) int64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("字段 %s 不存在", key)
	}
	switch n := v.(type) {
	case interface{ Int64() (int64, error) }:
		i, err := n.Int64()
		if err != nil {
			t.Fatalf("字段 %s 无法转成整数: %v", key, err)
		}
		return i
	}
	t.Fatalf("字段 %s 不是数字: %#v", key, m[key])
	return 0
}

func mapOf(t *testing.T, m common.MapType, key string) common.MapType {
	t.Helper()
	v, ok := m[key].(common.MapType)
	if !ok {
		t.Fatalf("字段 %s 不是对象: %#v", key, m[key])
	}
	return v
}

// TestVipInfoClient 覆盖 PC 的 vip/info（data 直接挂 musicPackage）。
func TestVipInfoClient(t *testing.T) {
	withFlags(t, true, false)
	body := runRequestAfter(t, "/api/music-vip-membership/client/vip/info",
		readTestdata(t, "music-vip-membership_client_vip_info.json"))
	data := mapOf(t, body, "data")
	for _, key := range []string{"musicPackage", "associator"} {
		pkg := mapOf(t, data, key)
		if got := num(t, pkg, "expireTime"); got < farFuture {
			t.Fatalf("%s.expireTime 没有推到远期: %d", key, got)
		}
		if got := num(t, pkg, "vipLevel"); got != 7 {
			t.Fatalf("%s.vipLevel 不是 7: %d", key, got)
		}
	}
	if got := num(t, data, "redVipLevel"); got != 7 {
		t.Fatalf("redVipLevel 不是 7: %d", got)
	}
}

// TestVipInfoFrontWithoutNow 盯的就是当初那枚 panic：
// front/vip/info 的 data 里没有 now，早期实现 interface {} is nil, not json.Number。
func TestVipInfoFrontWithoutNow(t *testing.T) {
	withFlags(t, true, false)
	body := runRequestAfter(t, "/api/music-vip-membership/front/vip/info",
		readTestdata(t, "music-vip-membership_front_vip_info.json"))
	data := mapOf(t, body, "data")
	pkg := mapOf(t, data, "musicPackage")
	if got := num(t, pkg, "expireTime"); got < farFuture {
		t.Fatalf("musicPackage.expireTime 没被改写: %d", got)
	}
	if pkg["iconUrl"] == nil {
		t.Fatal("musicPackage.iconUrl 被抹掉了（旧实现整体替换对象会丢字段）")
	}
	// familyVip 从没开通过（expireTime=0），不能被顺手改成会员
	if got := num(t, mapOf(t, data, "familyVip"), "expireTime"); got != 0 {
		t.Fatalf("familyVip.expireTime 不该被改: %d", got)
	}
}

// TestVipFloatData 覆盖续费页签：vipInfo 挂在 data.vipInfo 里。
func TestVipFloatData(t *testing.T) {
	withFlags(t, true, false)
	body := runRequestAfter(t, "/api/vip-center-bff/float/data",
		readTestdata(t, "vip-center-bff_float_data.json"))
	vipInfo := mapOf(t, mapOf(t, body, "data"), "vipInfo")
	for _, key := range []string{"musicPackage", "associator"} {
		if got := num(t, mapOf(t, vipInfo, key), "expireTime"); got < farFuture {
			t.Fatalf("%s.expireTime 没有推到远期: %d", key, got)
		}
	}
}

// TestVipQualityAuthInfo 覆盖音质/权益卡页面。
func TestVipQualityAuthInfo(t *testing.T) {
	withFlags(t, true, false)
	body := runRequestAfter(t, "/api/vip-center-bff/quality/auth/info",
		readTestdata(t, "vip-center-bff_quality_auth_info.json"))
	vipInfo := mapOf(t, mapOf(t, body, "data"), "vipInfo")
	if got := num(t, mapOf(t, vipInfo, "musicPackage"), "expireTime"); got < farFuture {
		t.Fatalf("musicPackage.expireTime 没有推到远期: %d", got)
	}
}

// TestVipLevelCard 覆盖「黑胶·叁」等级卡：userLevel.vipType 为负代表已过期。
func TestVipLevelCard(t *testing.T) {
	withFlags(t, true, false)
	body := runRequestAfter(t, "/api/vipnewcenter/app/level/growhpoint/basic",
		readTestdata(t, "vipnewcenter_app_level_growhpoint_basic.json"))
	userLevel := mapOf(t, mapOf(t, body, "data"), "userLevel")
	if got := num(t, userLevel, "expireTime"); got < farFuture {
		t.Fatalf("userLevel.expireTime 没有推到远期: %d", got)
	}
	if got := num(t, userLevel, "vipType"); got < 0 {
		t.Fatalf("userLevel.vipType 仍是过期标记: %d", got)
	}
}

// TestVipRightsCanUse 覆盖权益清单（每条权益的 canUse）。
func TestVipRightsCanUse(t *testing.T) {
	withFlags(t, true, false)
	body := runRequestAfter(t, "/api/vipauth/app/auth/query",
		readTestdata(t, "vipauth_app_auth_query.json"))
	list, ok := body["data"].(common.SliceType)
	if !ok || len(list) == 0 {
		t.Fatalf("data 不是非空数组: %#v", body["data"])
	}
	item := list[0].(common.MapType)
	if canUse, _ := item["canUse"].(bool); !canUse {
		t.Fatal("canUse 仍是 false")
	}
	if reason, _ := item["canNotUseReasonCode"].(interface{ String() string }); reason != nil && reason.String() != "0" {
		t.Fatalf("canNotUseReasonCode 没清零: %s", reason.String())
	}
}

func TestBlockDeliveryPopup(t *testing.T) {
	withFlags(t, false, true)
	sample := []byte(`{"message":"成功","code":200,"data":{"resourceData":{"title":"开通VIP畅听完整版",` +
		`"buttonText":"VIP仅¥0.22/天","positionName":"试听弹窗"},"logContext":"{}"}}`)
	body := runRequestAfter(t, "/api/delivery/deliver", sample)
	if got := mapOf(t, body, "data")["resourceData"]; got != nil {
		t.Fatalf("resourceData 应该是 null，实际 %v", got)
	}
}

func TestBlockBannerRules(t *testing.T) {
	withFlags(t, false, true)
	sample := []byte(`{"code":200,"trp":{"rules":["BANNER_PC_V2::x"]},"banners":[{"adid":1},{"id":2}]}`)
	body := runRequestAfter(t, "/api/v2/banner/get", sample)
	if n := len(body["banners"].(common.SliceType)); n != 0 {
		t.Fatalf("banners 应该清空，实际剩 %d", n)
	}
	if n := len(mapOf(t, body, "trp")["rules"].(common.SliceType)); n != 0 {
		t.Fatalf("trp.rules 应该清空，实际剩 %d", n)
	}
}

func TestNoFlagsKeepsData(t *testing.T) {
	withFlags(t, false, false)
	raw := readTestdata(t, "music-vip-membership_client_vip_info.json")
	body := runRequestAfter(t, "/api/music-vip-membership/client/vip/info", raw)
	data := mapOf(t, body, "data")
	if got := num(t, mapOf(t, data, "musicPackage"), "expireTime"); got != 1700000000000 {
		t.Fatalf("开关关着时不该改数据，实际 expireTime=%d", got)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
