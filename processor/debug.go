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
