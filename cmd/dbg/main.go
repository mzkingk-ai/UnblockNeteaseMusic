package main

import (
	"fmt"
	"os"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/provider"
)

func main() {
	srcs := []string{"kuwo", "migu", "kugou", "qq", "pyncmd"}
	if len(os.Args) > 1 {
		srcs = os.Args[1:]
	}
	id := "1901371647"
	name, artist := "孤勇者", "陈奕迅"
	common.Source = srcs
	provider.Init()
	for _, src := range srcs {
		p := provider.GetProvider(src)
		if src == "pyncmd" {
			s := p.GetSongUrl(common.SearchMusic{Id: id, Quality: common.Lossless}, &common.Song{Id: id})
			fmt.Printf("== pyncmd == url=%q br=%d size=%d\n", s.Url, s.Br, s.Size)
			continue
		}
		songs := p.SearchSong(common.SearchSong{Keyword: name + " " + artist, Name: name, ArtistsName: artist, ArtistList: []string{artist}, Quality: common.Lossless})
		fmt.Printf("== %s: %d 条 ==\n", src, len(songs))
		best := 0
		for i, s := range songs {
			if i > 4 {
				break
			}
			fmt.Printf("  [%d] %s - %s score=%.2f id=%s\n", i, s.Name, s.Artist, s.MatchScore, s.Id)
		}
		if len(songs) > 0 {
			best = len(songs) - 1
			u := p.GetSongUrl(common.SearchMusic{Id: songs[best].Id, Quality: common.Lossless}, songs[best])
			fmt.Printf("  best[%d] url=%q br=%d size=%d\n", best, u.Url, u.Br, u.Size)
		}
	}
}
