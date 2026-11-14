package processor

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
)

// 这一组只在排障时用：把客户端真实收到的响应结构落盘，拷回本机离线分析。
// 平时（没开 -wl）一个字节都不碰。

// 单份落盘上限，-wla 会把所有响应都读一遍，别让媒体/大 JSON 撑爆 /tmp。
const dumpBodyLimit = 512 * 1024

// isInterestPath 判断这个响应值不值得落盘（会员 / 广告 / 投放 / 升级）。
func isInterestPath(path string) bool {
	for _, kw := range []string{"/vip", "/ad/", "deliver", "/banner", "upgrade", "vipnewcenter", "/act/", "/linkshow"} {
		if strings.Contains(path, kw) {
			return true
		}
	}
	return false
}

// peekInterestBody 读一份响应体、尽力解成 JSON 后落盘，再把 Body 原样还原。
// 只用于观察真实结构，不参与改写，失败也不影响转发。
func peekInterestBody(path string, response *http.Response) {
	if response.StatusCode != 200 || response.Body == nil {
		return
	}
	body, err := ioutil.ReadAll(response.Body)
	response.Body.Close()
	response.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	if err != nil || len(body) == 0 || len(body) > dumpBodyLimit {
		return
	}
	plain, _ := decodeBody(body, response.Header.Get("Content-Encoding"), eApiKey)
	dumpInterest(*config.LogFile, path, plain)
}

// dumpRaw 把解不开的原始响应体落一份，方便拷回本机继续分析（只留最近 3 份）。
func dumpRaw(logFile string, body []byte) {
	if logFile == "" {
		return
	}
	dump := fmt.Sprintf("%s.raw.%d.bin", logFile, time.Now().Unix())
	if err := ioutil.WriteFile(dump, body, 0o644); err != nil {
		return
	}
	old, _ := filepath.Glob(logFile + ".raw.*.bin")
	for _, f := range old[:max(0, len(old)-3)] {
		_ = os.Remove(f)
	}
}

// dumpInterest 把解密后的 JSON 写到 <log>.json.<ms>.<path>.txt。
func dumpInterest(logFile, path string, body []byte) {
	if logFile == "" {
		return
	}
	name := strings.NewReplacer("/", "_", "?", "_", "&", "_", "=", "_").Replace(strings.TrimPrefix(path, "/"))
	if len(name) > 60 {
		name = name[:60]
	}
	dump := fmt.Sprintf("%s.json.%d.%s.txt", logFile, time.Now().UnixNano()/1e6, name)
	if err := ioutil.WriteFile(dump, body, 0o644); err != nil {
		log.Printf("dump interest fail(%s): %v\n", path, err)
		return
	}
	if len(body) > 0 {
		log.Printf("dump interest(%s) %d bytes -> %s\n", path, len(body), filepath.Base(dump))
	}
	// 同一个接口只在最近 3 份里保留，避免热门接口把冷门接口挤掉；总量再设个上限兜底。
	if old, _ := filepath.Glob(logFile + ".json.*." + name + ".txt"); len(old) > 3 {
		for _, f := range old[:len(old)-3] {
			_ = os.Remove(f)
		}
	}
	if all, _ := filepath.Glob(logFile + ".json.*.txt"); len(all) > 300 {
		for _, f := range all[:len(all)-300] {
			_ = os.Remove(f)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// contentProbePaths 是「列表类」接口。它们不在改写白名单里，但客户端拿到空
// 数据时会静默重试，光看日志分不清是上游空、我们改坏了还是根本没收到。
var contentProbePaths = []string{
	"playlist/square",
	"playlist/highquality",
	"playlist/catalogue",
	"page/cold",
}

var contentProbeCount sync.Map // 路径 -> *int32

// probeContent 把这类接口的状态码、长度和报文头部记一笔，每条路径最多记 3 次。
func probeContent(path string, response *http.Response) {
	hit := false
	for _, p := range contentProbePaths {
		if strings.Contains(path, p) {
			hit = true
			break
		}
	}
	if !hit {
		return
	}
	counter, _ := contentProbeCount.LoadOrStore(path, new(int32))
	if n := atomic.AddInt32(counter.(*int32), 1); n > 3 {
		return
	}
	body, err := ioutil.ReadAll(response.Body)
	response.Body.Close()
	if err == nil {
		response.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	}
	log.Printf("probe %s status=%d ce=%q cl=%d bodylen=%d head=%s\n",
		path, response.StatusCode, response.Header.Get("Content-Encoding"),
		response.ContentLength, len(body), headText(body, 96))
}

// headText 返回报文头部可打印的前 n 个字节。
func headText(body []byte, n int) string {
	if len(body) > n {
		body = body[:n]
	}
	out := make([]byte, 0, len(body))
	for _, b := range body {
		if b >= 0x20 && b < 0x7f {
			out = append(out, b)
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}
