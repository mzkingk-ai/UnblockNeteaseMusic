package processor

import (
	"log"
	"strings"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
)

// /api/batch 把好几个接口的响应打成一个包，键就是子接口的路径。
// 会员状态、广告位、弹窗都在里面各有一份，漏掉哪一个客户端都会照旧渲染，
// 所以这里按键分派，子响应交给各自的处理函数。
//
// 以前这里只认 /api/music-vip-membership/client/vip/info，新版客户端还会要
// front/vip/info，于是续费页签一直是旧的到期时间。现在只要子键带 vip 字样
// 就统一走 patchVipData。
func patchBatchResponse(netease *Netease) bool {
	if netease.JsonBody == nil {
		return false
	}
	modified := false
	for key, raw := range netease.JsonBody {
		sub, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		var subModified bool
		switch {
		case strings.Contains(key, "/usertool/sound/"):
			subModified = unblockSoundEffects(sub)
		case *config.EnableLocalVip && isVipPath(key):
			inner := &Netease{Path: key, JsonBody: sub}
			subModified = patchVipData(inner)
			sub = inner.JsonBody
		case *config.BlockAds && strings.Contains(key, "api/ad/"):
			// 子响应直接换成空对象，客户端拿不到素材就不会渲染广告位。
			sub = common.MapType{}
			log.Println("block Ad has been triggered(" + key + ").")
			subModified = true
		case *config.BlockAds && pathIn(key, bannerPaths):
			inner := &Netease{Path: key, JsonBody: sub}
			subModified = clearBanners(inner)
			sub = inner.JsonBody
		}
		if subModified {
			netease.JsonBody[key] = sub
			modified = true
		}
	}
	return modified
}
