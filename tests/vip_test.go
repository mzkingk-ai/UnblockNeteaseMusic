// Package tests 放跨包的黑盒测试：只通过 processor.RequestAfter 这个对外入口
// 验证行为，不碰 processor 的内部函数，所以能独立放在源码目录之外。
package tests

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
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
	// testdata 里的到期时间在导出时统一换成了固定的过去时间点（脱敏），
	// 这里断言的就是「原样不动」。
	if got := num(t, mapOf(t, data, "musicPackage"), "expireTime"); got != 1700000000000 {
		t.Fatalf("开关关着时不该改数据，实际 expireTime=%d", got)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// TestDoubleGzipRoundTrip 覆盖「外层 gzip + AES-ECB + 内层 gzip」这条实测报文路径：
// iPhone 的 player/url 就是 739B gzip -> 704B ECB -> 689B gzip -> 1104B JSON。
// 解的时候少解一层，JsonBody 就是 nil，换源逻辑全线跳过；回写时少包一层，
// 客户端会因为报文结构变了当成坏包。这里断言改写后的报文字节结构与原来一致。
func TestDoubleGzipRoundTrip(t *testing.T) {
	withFlags(t, true, false)
	jsonBody := []byte(`{"code":200,"data":{"now":1700000000000,"redVipLevel":3,"musicPackage":{"vipCode":220,"expireTime":1700000000000}}}`)

	inner, err := utils.Gzip(jsonBody)
	if err != nil {
		t.Fatalf("gzip inner: %v", err)
	}
	encrypted := crypto.AesEncryptECB(inner, []byte("e82ckenh8dichen8"))
	outer, err := utils.Gzip(encrypted)
	if err != nil {
		t.Fatalf("gzip outer: %v", err)
	}

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
		Body:       ioutil.NopCloser(bytes.NewBuffer(outer)),
	}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/eapi/music-vip-membership/client/vip/info", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/music-vip-membership/client/vip/info"})

	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("content-encoding 应该被删掉，实际 %q", ce)
	}
	out, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// 回写顺序和客户端平时收到的一致：先压内层 gzip，再整体 AES-ECB 加密。
	dec, ok := crypto.AesDecryptECB(out, []byte("e82ckenh8dichen8"))
	if !ok {
		t.Fatalf("回写内容不是 AES-ECB 密文")
	}
	plain, err := utils.UnGzip(dec)
	if err != nil {
		t.Fatalf("回写内容里没有内层 gzip: %v", err)
	}
	body := utils.ParseJson(plain)
	if body == nil {
		t.Fatalf("回写内容解不出 JSON")
	}
	if got := num(t, mapOf(t, body, "data"), "redVipLevel"); got != 7 {
		t.Fatalf("redVipLevel 没改写，got %d", got)
	}
}

// TestBatchResponse 覆盖 /api/batch 这种「一个响应里打包多个子接口」的情况：
// 续费页签读的是包里的 front/vip/info，广告位在包里另有子响应。
func TestBatchResponse(t *testing.T) {
	withFlags(t, true, true)
	// 子响应都是明文 JSON，整体也不加密——这正是新版客户端的 batch 形态。
	body := []byte(`{"code":200,` +
		`"/api/music-vip-membership/front/vip/info":{"code":200,"data":{"redVipLevel":3,"musicPackage":{"vipCode":220,"expireTime":1700000000000}}},` +
		`"/api/ad/loading/get":{"ads":[{"ad_id":"x"}]}}`)

	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/eapi/batch", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/batch"})

	out, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := utils.ParseJson(out)
	if got == nil {
		t.Fatalf("batch 响应被改坏，解不出 JSON: %x", out)
	}
	vip := mapOf(t, got, "/api/music-vip-membership/front/vip/info")
	expire := num(t, mapOf(t, mapOf(t, vip, "data"), "musicPackage"), "expireTime")
	if expire < farFuture {
		t.Fatalf("批量子响应里的到期时间没被推远，got %d", expire)
	}
	if ad := mapOf(t, got, "/api/ad/loading/get"); len(ad) != 0 {
		t.Fatalf("批次里的广告子响应没被清空: %v", ad)
	}
}

// TestPlainBodyNotDecrypted 是给 AesDecryptECB 的回归测试：它只要长度是 16 的
// 倍数就返回成功，明文 JSON 恰好撞上 16 的倍数时会被解成乱码。所以解码前必须
// 先判断「本来就是 JSON」，不能无脑解密。
func TestPlainBodyNotDecrypted(t *testing.T) {
	withFlags(t, true, true)
	body := []byte(`{"code":200,"data":{"a":"xxxx"}}`) // 32 字节，16 的倍数
	if len(body)%16 != 0 {
		t.Fatalf("测试前提不成立：body 长度 %d 不是 16 的倍数", len(body))
	}
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/api/song/detail", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/song/detail"})

	out, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("明文响应被改动了:\n want %s\n got  %s", body, out)
	}
}

// TestClearAdsKeepsArrayShape 覆盖开屏广告：/api/ad/loading/get 的 ads 是数组，
// 清空时必须还它一个空数组。换成空对象的话，按强类型解析的客户端会把整包
// 当成坏数据，退回本地缓存的旧素材，开屏广告就还会弹。
func TestClearAdsKeepsArrayShape(t *testing.T) {
	withFlags(t, false, true)
	body := []byte(`{"code":200,"ads":[{"ad_id":"x","imgs":["u"]}],"other":1}`)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/eapi/ad/loading/get", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/ad/loading/get"})

	out, _ := ioutil.ReadAll(resp.Body)
	got := utils.ParseJson(out)
	if got == nil {
		t.Fatalf("响应被改坏: %s", out)
	}
	if _, ok := got["ads"].([]interface{}); !ok {
		t.Fatalf("ads 应该还是数组，实际 %T: %v", got["ads"], got["ads"])
	}
	if len(got["ads"].([]interface{})) != 0 {
		t.Fatalf("ads 没清空: %v", got["ads"])
	}
}

// TestUserInfoVipPatch 覆盖 /api/v1/user_info：「我的」页和会员入口都读它，
// 未开通账号返回 viptype=0、expiretime=0、backupExpireTime=上一次到期的
// 时间戳，客户端用 backupExpireTime 和现在相减算出「已失去黑胶VIP特权 N 天」。
func TestUserInfoVipPatch(t *testing.T) {
	withFlags(t, true, false)
	// 取自真机 dump，值已脱敏。
	body := []byte(`{"level":8,"viptype":0,"expiretime":0,"backupExpireTime":1700000000000,` +
		`"storeTitle":"商城","userPoint":{"balance":0,"userId":0},"code":200}`)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/eapi/v1/user_info", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/v1/user_info"})

	out, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := utils.ParseJson(out)
	if got == nil {
		t.Fatalf("响应被改坏: %s", out)
	}
	if vt := num(t, got, "viptype"); vt != 11 {
		t.Fatalf("viptype 应该被改成黑胶VIP(11)，实际 %d", vt)
	}
	for _, key := range []string{"expiretime", "backupExpireTime"} {
		if v := num(t, got, key); v < farFuture {
			t.Fatalf("%s 没被推到 100 年后，got %d", key, v)
		}
	}
	// 其它字段必须原样保留，客户端整包解析，少一个都可能判坏。
	if got["storeTitle"] != "商城" {
		t.Fatalf("storeTitle 被改动了: %v", got["storeTitle"])
	}
}

// TestUserInfoRealMemberUntouched：真会员（viptype 已经是正数、到期时间在
// 100 年内）只需要推时间，类型值不能被覆盖成别的。
func TestUserInfoRealMemberUntouched(t *testing.T) {
	withFlags(t, true, false)
	body := []byte(`{"viptype":11,"expiretime":1700000000000,"backupExpireTime":1700000000000,"code":200}`)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/api/v1/user_info", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/v1/user_info"})

	out, _ := ioutil.ReadAll(resp.Body)
	got := utils.ParseJson(out)
	if num(t, got, "viptype") != 11 {
		t.Fatalf("viptype 被改坏了: %v", got["viptype"])
	}
	if num(t, got, "expiretime") < farFuture {
		t.Fatalf("到期时间没被推远")
	}
}

// TestUserInfoIOSPath：iOS 用的是 /user/info（不是 /user_info），冷启动还有
// /page/cold/start/user/info/get，三种写法都要命中。
func TestUserInfoIOSPath(t *testing.T) {
	withFlags(t, true, false)
	for _, path := range []string{"/api/v1/user/info", "/api/page/cold/start/user/info/get"} {
		body := []byte(`{"viptype":0,"expiretime":0,"backupExpireTime":1700000000000,"code":200}`)
		resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
		req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/xeapi/v1/user/info", nil)
		processor.RequestAfter(req, resp, &processor.Netease{Path: path})

		out, _ := ioutil.ReadAll(resp.Body)
		got := utils.ParseJson(out)
		if got == nil {
			t.Fatalf("%s: 响应被改坏: %s", path, out)
		}
		if num(t, got, "viptype") != 11 {
			t.Fatalf("%s: viptype 没被改动: %v", path, got["viptype"])
		}
		if num(t, got, "backupExpireTime") < farFuture {
			t.Fatalf("%s: backupExpireTime 没被推远", path)
		}
	}
}

// TestUserInfoNested 冷启动接口把用户信息包在 data 里，字段不在顶层。
func TestUserInfoNested(t *testing.T) {
	withFlags(t, true, false)
	body := []byte(`{"code":200,"data":{"userType":null,"viptype":0,"expiretime":0,` +
		`"backupExpireTime":1700000000000,"canRcmdStartPlay":0}}`)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: ioutil.NopCloser(bytes.NewBuffer(body))}
	req := httptest.NewRequest(http.MethodPost, "https://interface3.music.163.com/xeapi/page/cold/start/user/info/get", nil)
	processor.RequestAfter(req, resp, &processor.Netease{Path: "/api/page/cold/start/user/info/get"})

	out, _ := ioutil.ReadAll(resp.Body)
	got := utils.ParseJson(out)
	data := mapOf(t, got, "data")
	if num(t, data, "viptype") != 11 {
		t.Fatalf("嵌套的 viptype 没被改动: %v", data["viptype"])
	}
	if num(t, data, "backupExpireTime") < farFuture {
		t.Fatalf("嵌套的 backupExpireTime 没被推远")
	}
	if _, ok := data["canRcmdStartPlay"]; !ok {
		t.Fatalf("同层其它字段被弄丢了: %v", data)
	}
}
