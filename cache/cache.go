package cache

import (
	"container/list"
	"fmt"
	"strconv"
	"sync"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
)

// 缓存在路由器上必须有上限：内存只有 128～256MB，sync.Map 只增不减，
// 开着几个月就是无限增长（每首解过的歌一条，含完整直链和 md5）。
//
// 这里用「sync.Map + 双向链表」做成并发安全的 LRU：
//   - 读命中时把节点挪到表头（需要写锁，用 sync.Mutex 保护链表）；
//   - 写入时超限就从表尾淘汰。
// 并发量只有一个家庭局域网，锁竞争可以忽略，换来的是明确的内存上界。
const (
	// maxSongs 是歌曲条数上限。按每条约 1KB 估，1500 条约 1.5MB。
	maxSongs = 1500
	// maxTagKeys 是平台 id 标签（GetPlatFormIdTag）的键数上限，
	// 这类键只会因为新平台出现而增长，正常几个就够。
	maxTagKeys = 64
)

type entry struct {
	key   string
	value interface{}
}

var (
	cache sync.Map
	mu    sync.Mutex
	// lru 前端是最近使用过的，后端是最久没用的
	lru   = list.New()
	count int
	// tagKeys 单独计数，平台标签不参与 LRU 淘汰（它们是常数级）
	tagKeys int
)

func lruGet(k string) (interface{}, bool) {
	v, ok := cache.Load(k)
	if !ok {
		return nil, false
	}
	mu.Lock()
	if e, ok2 := v.(*list.Element); ok2 {
		lru.MoveToFront(e)
	}
	mu.Unlock()
	return v, true
}

// lruPut 写入并按需淘汰。tag 为 true 表示平台标签类键，只计数不淘汰。
func lruPut(k string, value interface{}, tag bool) {
	mu.Lock()
	defer mu.Unlock()

	if old, ok := cache.Load(k); ok {
		// 已存在：原地更新，挪到表头
		if e, ok2 := old.(*list.Element); ok2 {
			e.Value.(*entry).value = value
			lru.MoveToFront(e)
			cache.Store(k, e)
			return
		}
	}

	e := lru.PushFront(&entry{key: k, value: value})
	cache.Store(k, e)
	count++

	for lru.Len() > maxSongs {
		be := lru.Back()
		if be == nil {
			break
		}
		lru.Remove(be)
		cache.Delete(be.Value.(*entry).key)
		count--
	}
}

func GetPlatFormIdTag(key common.PlatformIdTag) string {
	var id int64 = 100001
	k := "PlatFormIdTagFor" + string(key)
	if v, ok := lruGet(k); ok {
		if cur, ok2 := v.(int64); ok2 {
			id = cur + 1
		}
	}
	if tagKeys >= maxTagKeys {
		// 键数已经封顶：不再新增，直接复用当前值，避免被刷爆
		return strconv.FormatInt(id, 10)
	}
	lruPut(k, id, true)
	return strconv.FormatInt(id-1, 10)
}

func songKey(key common.SearchMusic) string {
	return fmt.Sprintf("%+v", key)
}

func PutSong(key common.SearchMusic, value *common.Song) {
	lruPut(songKey(key), value, false)
}

func GetSong(key common.SearchMusic) (*common.Song, bool) {
	if v, ok := lruGet(songKey(key)); ok {
		if song, ok2 := v.(*common.Song); ok2 {
			return song, true
		}
	}
	return nil, false
}

func Delete(key common.SearchMusic) {
	mu.Lock()
	defer mu.Unlock()
	k := songKey(key)
	if old, ok := cache.Load(k); ok {
		if e, ok2 := old.(*list.Element); ok2 {
			lru.Remove(e)
			count--
		}
		cache.Delete(k)
	}
}
