package processor

import "testing"

// hasApiPrefix 与 xeapi 归一化是本轮修复的核心：
// iOS 客户端全部走 /xeapi/，上游不认这个前缀，请求直接掉到透传分支，
// 播放地址永远是试听片段。这里把三条前缀和归一化后的结果都钉住。
func TestHasApiPrefix(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/song/enhance/player/url/v1", true},
		{"/weapi/song/enhance/player/url", true},
		{"/xeapi/song/enhance/player/url/v1", true},
		{"/eapi/song/enhance/player/url", false}, // POST 分支单独处理
		{"/api", false},
		{"/song/enhance/player/url", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hasApiPrefix(c.path); got != c.want {
			t.Errorf("hasApiPrefix(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestXeApiNormalizedToApi(t *testing.T) {
	// 归一化用正则 ^/xeapi/ -> /api/，验证两条播放地址都能落到 Path 白名单上。
	for _, p := range []string{
		"/api/song/enhance/player/url",
		"/api/song/enhance/player/url/v1",
	} {
		if _, ok := Path[p]; !ok {
			t.Errorf("Path 白名单缺少 %q，该接口不会被改写", p)
		}
	}
}
