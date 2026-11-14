package processor

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

// 这一组是 JSON 取值的小工具。网易的响应字段时有时无、类型也不稳定，
// 直接写 data["id"].(json.Number) 这种单返回值断言，字段一缺就 panic，
// 而 panic 在代理层被 recover 兜住之后响应就写不回去了，客户端看到的是
// 「页面加载失败」——之前 front/vip/info 缺 now 字段就是这么炸的。
// 所以统一走这里，取值失败只返回零值 + false，绝不 panic。

// mapAt 取子对象，不是对象就当没有。
func mapAt(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]interface{})
	return v
}

// sliceAt 取数组，不是数组就当空。
func sliceAt(m map[string]interface{}, key string) []interface{} {
	if m == nil {
		return nil
	}
	v, _ := m[key].([]interface{})
	return v
}

// numAt 取整数。ParseJson 用的是 UseNumber，所以常规是 json.Number；
// 浮点、整型、纯数字字符串也一并认，省得各处自己处理。
func numAt(m map[string]interface{}, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case json.Number:
		i, err := v.Int64()
		return i, err == nil
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// strAt 取字符串。
func strAt(m map[string]interface{}, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[key].(string)
	return v, ok
}

// hasKey 判断键是否存在（值可以是 null）。
func hasKey(m map[string]interface{}, key string) bool {
	if m == nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// codeAt 读响应里的 code，缺省按 "200" 处理。
func codeAt(m map[string]interface{}) string {
	if n, ok := numAt(m, "code"); ok {
		return strconv.FormatInt(n, 10)
	}
	if s, ok := strAt(m, "code"); ok && s != "" {
		return s
	}
	return "200"
}

// firstTrackId 从 trackIds 参数里取第一首的 id。这个参数有两种形态：
// JSON 数组（字符串）和已经解析好的数组，新老客户端发的都不一样。
func firstTrackId(v interface{}) (string, bool) {
	switch ids := v.(type) {
	case string:
		var list []string
		if err := utils.ParseJsonV3([]byte(ids), &list); err == nil && len(list) > 0 {
			return list[0], true
		}
		var raw []interface{}
		if err := utils.ParseJsonV3([]byte(ids), &raw); err == nil && len(raw) > 0 {
			return fmt.Sprint(raw[0]), true
		}
		return "", false
	case []interface{}:
		if len(ids) == 0 {
			return "", false
		}
		if n, ok := ids[0].(json.Number); ok {
			return n.String(), true
		}
		return fmt.Sprint(ids[0]), true
	}
	return "", false
}

// idOf 取歌曲 id，兼容 json.Number 和字符串两种写法。
func idOf(m map[string]interface{}) (string, bool) {
	if s, ok := strAt(m, "id"); ok && s != "" {
		return s, true
	}
	if n, ok := numAt(m, "id"); ok {
		return strconv.FormatInt(n, 10), true
	}
	return "", false
}
