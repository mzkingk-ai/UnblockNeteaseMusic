package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/provider/base"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/provider/kuwo"
)

func scan(kw string) {
	u := fmt.Sprintf("http://search.kuwo.cn/r.s?&all=%s&ft=music&itemset=web_2013&client=kt&pn=0&rn=30&rformat=json&encoding=utf8", url.QueryEscape(kw))
	list, err := kuwo.FetchPcAbsList(u, make(http.Header, 2))
	fmt.Printf("\n=== 搜索 %q err=%v items=%d\n", kw, err, len(list))
	ss := base.PreSearchSong(common.SearchSong{Keyword: "孤勇者 陈奕迅", Name: "孤勇者", ArtistsName: "陈奕迅", ArtistList: []string{"陈奕迅"}})
	for i, it := range list {
		if i > 30 {
			break
		}
		name, _ := it["SONGNAME"].(string)
		art, _ := it["ARTIST"].(string)
		sc, ok := base.CalScore(ss, name, art, i, len(list))
		flag := ""
		if ok && sc >= 0.75 {
			flag = " <<<< 通过"
		}
		fmt.Printf("  [%2d] %-24q %-14q score=%.2f ok=%v%s\n", i, name, art, sc, ok, flag)
	}
}

func main() {
	scan("孤勇者")
	scan("孤勇者 陈奕迅")
}
