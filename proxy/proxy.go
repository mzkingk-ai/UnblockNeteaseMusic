package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/network"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/processor"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/version"
)

type HttpHandler struct{}

var localhost = map[string]int{}

// localhostWarn 保证「本机回环但未启用 MITM」每个目标只提示一次，
// 否则客户端每 5 秒重连一次就刷一行日志。
var localhostWarn sync.Map

func InitProxy() {
	log.Println("-------------------Init Proxy-------------------")
	// address := "0.0.0.0:"
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		panic(err)
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				localhost[ipnet.IP.String()] = 1
			}
			if ipnet.IP.To16() != nil {
				localhost[ipnet.IP.To16().String()] = 1
			}
		}
	}
	var localhostKey []string
	for k, _ := range localhost {
		localhostKey = append(localhostKey, k)
	}
	log.Println("Http Proxy:")
	log.Println(strings.Join(localhostKey, " , "))
	// MITM 监听：客户端把网易域名解析回本机时，CONNECT 落进 5200，
	// 由这里解密后交给 serveProxy 改写，换源才真正生效。
	if len(*config.MITMAddr) > 0 {
		startMitmServer(*config.MITMAddr)
	}
	// 证书文件缺失/不可读时先退化为「只提供 HTTP、不开 TLS 端口」，
	// 而不是让 startTlsServer 直接 os.Exit。理由：TLS 端口只在「客户端把
	// HTTPS 代理指向本机」时才用到，播放器/网页端绝大多数走 HTTP；为了一个
	// 可选的端口把整个服务打死，用户看到的现象是「页面点启动没反应」。
	if err := checkCertFiles(*config.CertFile, *config.KeyFile); err != nil {
		if *config.Insecure {
			log.Printf("提示：%v，按 -insecure 以明文在此端口提供服务", err)
			go startTlsServer(fmt.Sprintf("%s:%d", *config.Addr, *config.TLSPort), *config.CertFile, *config.KeyFile, &HttpHandler{})
		} else {
			log.Printf("警告：%v，TLS 端口 %d 不启动（HTTP 端口 %d 正常工作）", err, *config.TLSPort, *config.Port)
			log.Printf("提示：如需 TLS，请检查证书路径或改用 -insecure 只跑 HTTP")
		}
	} else {
		go startTlsServer(fmt.Sprintf("%s:%d", *config.Addr, *config.TLSPort), *config.CertFile, *config.KeyFile, &HttpHandler{})
	}
	// HTTP 端口必须最后启动：它是「服务是否活着」的判断依据。
	// HTTPS 起不来时 startTlsServer 会直接退出进程，若 HTTP 已经先监听，
	// 探活脚本会误判成功（端口在、进程已死），现象变成「能用一会儿就断」。
	startServer(fmt.Sprintf("%s:%d", *config.Addr, *config.Port), &HttpHandler{})
}
func (h *HttpHandler) ServeHTTP(resp http.ResponseWriter, request *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recover panic : "+"\n"+string(debug.Stack()), r)
		}
	}()
	requestURI := request.RequestURI
	// 上游把整段路由写成一个 if / else if / else 链：只有走到最后一个 else 才是
	// 「普通请求转发」。但最后一个 else 被挂在了 else-if 分支里面，而 else-if 的条件
	// （isEmbeddedSourceUrl）在外层 if 为假时永远进不去——真正的普通 API 请求
	// （/eapi/...、/api/...）因此直接掉到函数末尾，返回 200 空响应。
	// 实测：上游 curl /api/song/enhance/player/url 永远是 Content-Length: 0，
	// 日志里连 Transport 都没有。这里改成顺序判定，三种形态互斥后各自处理。
	switch {
	case strings.Contains(requestURI, "/unblockmusic/"):
		h.serveSourceUrl(resp, request)
	case isEmbeddedSourceUrl(requestURI):
		if !forwardSourceUrl(resp, request, requestURI) {
			h.serveProxy(resp, request)
		}
	case request.Method == http.MethodConnect:
		h.serveTunnel(resp, request)
	default:
		h.serveProxy(resp, request)
	}
}

// serveSourceUrl 处理经典的 /unblockmusic/<真实音源地址> 形态。
func (h *HttpHandler) serveSourceUrl(resp http.ResponseWriter, request *http.Request) {
	requestURI := request.RequestURI
	i := strings.Index(requestURI, "/unblockmusic/")
	realMusicUrl := requestURI[i+len("/unblockmusic/"):]
	realURI, err := url.Parse(realMusicUrl)
	if err != nil {
		log.Println("url.Parse error:", err)
		return
	}
	response, err := network.Request(&network.ClientRequest{
		Method:               request.Method,
		RemoteUrl:            realMusicUrl,
		Host:                 realURI.Host,
		Header:               request.Header,
		Body:                 request.Body,
		Cookies:              request.Cookies(),
		ForbiddenEncodeQuery: true,
		Proxy:                false,
	})
	if err != nil {
		log.Println("network.Request error:", err)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		resp.Header()[name] = values
	}
	//fix ios 7.0.20
	resp.Header().Del("Keep-Alive")
	if response.StatusCode == 200 && (len(request.Header.Get("range")) > 0 || len(response.Header.Get("content-range")) > 0) {
		response.StatusCode = 206
	}
	resp.WriteHeader(response.StatusCode)
	if _, err = io.Copy(resp, response.Body); err != nil {
		return
	}
}

// serveTunnel 处理 CONNECT：目标是本机时回环到自己的端口，否则直接打洞。
func (h *HttpHandler) serveTunnel(resp http.ResponseWriter, request *http.Request) {
	hostStr := request.Host
	if len(request.URL.Host) > 0 {
		hostStr = request.URL.Host
	}
	if _, ok := common.ProxyDomain[strings.TrimSuffix(hostStr, ":443")]; ok {
		proxyConnectLocalhost(resp, request)
		return
	}
	for k := range localhost {
		if strings.Contains(hostStr, k) {
			proxyConnectLocalhost(resp, request)
			return
		}
	}
	proxyConnect(resp, request)
}

// serveProxy 是普通请求的主路径：网易云 API 走 processor 改写，其它域名直接透传。
// passthroughHosts 记录已经提示过的「未登记域名」，让上面的日志每个域名只出现一次。
var passthroughHosts sync.Map

func (h *HttpHandler) serveProxy(resp http.ResponseWriter, request *http.Request) {
	requestURI := request.RequestURI
	hostStr := request.URL.Host
	if len(hostStr) == 0 {
		hostStr = request.Host
	}
	if len(request.URL.Port()) > 0 && strings.Contains(hostStr, ":"+request.URL.Port()) {
		hostStr = strings.Replace(hostStr, ":"+request.URL.Port(), "", 1)
	}
	path := request.URL.Path
	rawQuery := request.URL.RawQuery
	uriBytes := []byte(path)
	left := uriBytes[:(len(uriBytes) / 2)]
	right := uriBytes[len(uriBytes)/2:]

	scheme := "http://"
	if request.TLS != nil || request.URL.Port() == "443" {
		scheme = "https://"
	}
	if len(request.URL.Scheme) > 0 {
		scheme = request.URL.Scheme + "://"
	}
	infinite := false
	for k := range localhost {
		if strings.Contains(hostStr, k) {
			infinite = true
			break
		}
	}
	if infinite || strings.Contains(hostStr, "localhost") ||
		strings.Contains(hostStr, "127.0.0.1") ||
		strings.Contains(hostStr, "0.0.0.0") ||
		(len(path) > 1 && strings.Count(path, "/") > 1 && bytes.EqualFold(left, right)) {
		//cause infinite loop
		requestURI = scheme + request.Host
		pathA := ""
		if bytes.EqualFold(left, right) {
			pathA = string(left)
		} else {
			pathA = string(uriBytes)
		}
		requestURI += pathA
		log.Printf("Abandon:%s\n", requestURI)
		for strings.HasPrefix(pathA, "/") {
			pathA = strings.TrimPrefix(pathA, "/")
		}
		requestURI = scheme + pathA
		fixedUrl, err := url.Parse(requestURI)
		if err != nil {
			log.Printf("Try Fix :%s,error:%s\n", requestURI, err)
			resp.WriteHeader(200)
			resp.Write([]byte(version.AppVersion()))
			return
		} else if fixedUrl.Host == "" || fixedUrl.Host == "favicon.ico" {
			resp.WriteHeader(200)
			resp.Write([]byte(version.AppVersion()))
			return
		}
		log.Printf("Try Fix:%s\n", requestURI)
		request.URL = fixedUrl
		hostStr = fixedUrl.Host
		path = fixedUrl.Path
		rawQuery = fixedUrl.RawQuery
		if len(request.URL.Port()) > 0 && strings.Contains(hostStr, ":"+request.URL.Port()) {
			hostStr = strings.Replace(hostStr, ":"+request.URL.Port(), "", 1)
		}
	}
	request.Host = hostStr
	if proxyDomain, ok := common.ProxyDomain[hostStr]; ok && !strings.Contains(path, "stream") {
		if hostIp, ok := common.HostDomain[hostStr]; ok {
			proxyDomain = hostIp
		} else {
			proxyDomain = hostStr
		}
		if len(request.URL.Port()) > 0 {
			proxyDomain = proxyDomain + ":" + request.URL.Port()
		}
		// Host 头**必须保持原域名**，不能改成 IP。
		// 实测（2026-11-01，目标 220.197.30.67）：
		//   Host: interfacepc.music.163.com → {"code":-110,...,"url":null} 正常业务响应
		//   Host: 220.197.30.67             → 404 Not Found（9 字节）
		// 证书校验交给 network 那边显式指定 SNI 解决，与此无关。
		urlString := scheme + proxyDomain + path
		if len(rawQuery) > 0 {
			urlString = urlString + "?" + rawQuery
		}
		log.Printf("Transport:%s(%s)(%s)\n", urlString, request.Host, request.Method)
		netease := processor.RequestBefore(request)
		if netease == nil {
			log.Println("Request Blocked:", urlString)
			return
		}
		response, err := processor.Request(request, urlString)
		if err != nil {
			log.Println("Request error:", urlString)
			return
		}
		defer response.Body.Close()
		processor.RequestAfter(request, response, netease)
		for name, values := range response.Header {
			resp.Header()[name] = values
		}
		resp.WriteHeader(response.StatusCode)
		if _, err = io.Copy(resp, response.Body); err != nil {
			log.Println("io.Copy error:", err)
			return
		}
		return
	}
	// 走到这里说明上面没命中：请求会被原样透传，任何改写都不会发生。
	// 网易接口域名不在 ProxyDomain 里时，这就是「播放地址永远是试听片段」
	// 的直接原因（interfacepc.music.163.com 漏登记就是这么丢的）。
	// 只按域名提示一次，避免每个请求刷一行。
	if strings.Contains(hostStr, "music.163.com") {
		if _, seen := passthroughHosts.LoadOrStore(hostStr, true); !seen {
			log.Printf("Passthrough 未登记的网易接口域名：%s（该域名的请求不会走换源）", hostStr)
		}
	}
	if proxyDomain, ok := common.HostDomain[hostStr]; ok {
		if len(request.URL.Port()) > 0 {
			proxyDomain = proxyDomain + ":" + request.URL.Port()
		}
		requestURI = scheme + proxyDomain + path
	} else {
		if len(request.URL.Port()) > 0 {
			hostStr = hostStr + ":" + request.URL.Port()
		}
		requestURI = scheme + hostStr + path
	}
	if len(rawQuery) > 0 {
		requestURI = requestURI + "?" + rawQuery
	}
	for hostDomain := range common.HostDomain {
		if strings.Contains(request.Referer(), hostDomain) {
			request.Header.Set("referer", request.Host)
			break
		}
	}
	log.Printf("Direct:%s(%s)(%s)\n", requestURI, request.Host, request.Method)
	response, err := network.Request(&network.ClientRequest{
		Method:    request.Method,
		RemoteUrl: requestURI,
		Host:      request.Host,
		Header:    request.Header,
		Body:      request.Body,
		Cookies:   request.Cookies(),
		Proxy:     true,
	})
	if err != nil {
		log.Println("network.Request error:", err)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		resp.Header()[name] = values
	}
	resp.WriteHeader(response.StatusCode)
	if _, err = io.Copy(resp, response.Body); err != nil {
		log.Println("io.Copy error:", err)
		return
	}
}
func proxyConnectLocalhost(rw http.ResponseWriter, req *http.Request) {
	log.Printf("Local Received request %s %s %s\n",
		req.Method,
		req.Host,
		req.RemoteAddr,
	)
	hij, ok := rw.(http.Hijacker)
	if !ok {
		log.Println("HTTP Server does not support hijacking")
	}
	client, _, err := hij.Hijack()
	if err != nil {
		log.Println(err)
		return
	}
	// 客户端把网易域名解析回本机时，隧道落在这里。配了 -mtls 就整条交给
	// MITM：解密后复用 serveProxy 改写响应，这才是换源真正生效的位置。
	// 原先这里是 tls.Dial 回环自己，既不解密也不改写，且 mips 上 Go TLS
	// 会报 packet length too long，客户端表现为「连上了又被掐断」。
	if len(*config.MITMAddr) > 0 {
		if _, err := client.Write([]byte("HTTP/1.0 200 Connection Established\r\n\r\n")); err != nil {
			client.Close()
			return
		}
		go func() {
			defer client.Close()
			upstream, err := net.DialTimeout("tcp", *config.MITMAddr, 10*time.Second)
			if err != nil {
				log.Println("MITM 回环拨号失败：", err)
				return
			}
			defer upstream.Close()
			// 双向搬运：handleMitmInbound 负责嗅探与握手，无需知道目标
			done := make(chan struct{}, 1)
			go func() { io.Copy(upstream, client); done <- struct{}{} }()
			io.Copy(client, upstream)
			<-done
		}()
		return
	}
	localUrl := "localhost:"
	var server net.Conn
	port := req.URL.Port()
	// 没配 MITM 时退化为原样直通。同一目标只提示一次：客户端每 5 秒重连
	// 一次，逐条打印会把日志冲成只剩这几行。
	if _, seen := localhostWarn.LoadOrStore(req.Host, true); !seen {
		log.Printf("提示：%s 走本机回环但未启用 -mtls，将原样透传（换源不生效）", req.Host)
	}
	if port == "" {
		port = "443"
	}
	localUrl = localUrl + port
	server, err = net.DialTimeout("tcp", localUrl, 15*time.Second)
	if err != nil {
		log.Println(err)
		return
	}
	client.Write([]byte("HTTP/1.0 200 Connection Established\r\n\r\n"))
	go io.Copy(server, client)
	io.Copy(client, server)
	defer client.Close()
	defer server.Close()
}
func proxyConnect(rw http.ResponseWriter, req *http.Request) {
	log.Printf("Received request %s %s %s\n",
		req.Method,
		req.Host,
		req.RemoteAddr,
	)
	if req.Method != "CONNECT" {
		rw.WriteHeader(http.StatusMethodNotAllowed)
		rw.Write([]byte("This is a http tunnel proxy, only CONNECT method is allowed."))
		return
	}
	host := req.URL.Host
	hij, ok := rw.(http.Hijacker)
	if !ok {
		log.Println("HTTP Server does not support hijacking")
	}
	client, _, err := hij.Hijack()
	if err != nil {
		log.Println(err)
		return
	}
	server, err := net.DialTimeout("tcp", host, 15*time.Second)
	if err != nil {
		log.Println(err)
		return
	}
	client.Write([]byte("HTTP/1.0 200 Connection Established\r\n\r\n"))
	go io.Copy(server, client)
	io.Copy(client, server)
	defer client.Close()
	defer server.Close()
}

// isEmbeddedSourceUrl 判断 RequestURI 里是否内嵌了一个完整的音源直链。
// 需要同时满足：有 scheme、有 host，且路径里出现过 /unblockmusic/<scheme>://。
// 歌曲自身的 path（如 /song/media/outer/url/xxx.mp3）不会命中。
func embeddedSourceUrl(requestURI string) (string, bool) {
	idx := strings.Index(requestURI, "/unblockmusic/")
	if idx < 0 {
		return "", false
	}
	rest := requestURI[idx+len("/unblockmusic/"):]
	for len(rest) > 1 && rest[0] == '/' {
		rest = rest[1:]
	}
	if !strings.HasPrefix(rest, "http://") && !strings.HasPrefix(rest, "https://") {
		return "", false
	}
	u, err := url.Parse(rest)
	if err != nil || len(u.Host) == 0 {
		return "", false
	}
	return rest, true
}

func isEmbeddedSourceUrl(requestURI string) bool {
	_, ok := embeddedSourceUrl(requestURI)
	return ok
}

// forwardSourceUrl 把请求原样转发到内嵌的音源直链。
func forwardSourceUrl(resp http.ResponseWriter, request *http.Request, requestURI string) bool {
	realMusicUrl, ok := embeddedSourceUrl(requestURI)
	if !ok {
		return false
	}
	realURI, err := url.Parse(realMusicUrl)
	if err != nil {
		log.Println("url.Parse error:", err)
		return false
	}
	continueFrom := request.Header.Get("Range")
	header := request.Header.Clone()
	response, err := network.Request(&network.ClientRequest{
		Method:               request.Method,
		RemoteUrl:            realMusicUrl,
		Host:                 realURI.Host,
		Header:               header,
		Body:                 request.Body,
		ForbiddenEncodeQuery: true,
		Proxy:                false,
	})
	if err != nil {
		log.Println("forward source url error:", err)
		resp.WriteHeader(http.StatusBadGateway)
		return true
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		resp.Header()[name] = values
	}
	resp.Header().Del("Keep-Alive")
	if response.StatusCode == 200 && len(continueFrom) > 0 {
		response.StatusCode = 206
	}
	resp.WriteHeader(response.StatusCode)
	if _, err := io.Copy(resp, response.Body); err != nil {
		log.Println("io.Copy error:", err)
	}
	return true
}

// fatal 取代上游的 panic：panic 只发生在子 goroutine 里，main 的 recover 够不着，
// 进程会带着空日志栈直接死掉，前端页面无从判断原因。
// 这里打印明确的中文原因，并主动 os.Exit(1)，让 rc/init 脚本的探活立刻判定失败。
// checkCertFiles 在启动前检查证书/私钥是否可用，让「证书缺失」从进程崩溃
// 降级为一个可记录、可提示的启动告警。
func checkCertFiles(certFile, keyFile string) error {
	if len(certFile) == 0 || len(keyFile) == 0 {
		return fmt.Errorf("未指定证书或私钥路径")
	}
	if _, err := os.Stat(certFile); err != nil {
		return fmt.Errorf("证书文件不可用 %s: %v", certFile, err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		return fmt.Errorf("私钥文件不可用 %s: %v", keyFile, err)
	}
	return nil
}

func fatal(what string, err error) {
	msg := fmt.Sprintf("启动失败：%s：%v", what, err)
	log.Println(msg)
	// 只有真的缺文件才提示，避免端口占用等无关错误把用户往证书上带
	if !*config.Insecure {
		if _, statErr := os.Stat(*config.CertFile); os.IsNotExist(statErr) {
			log.Println("提示：证书文件不存在 → " + *config.CertFile)
		}
		if _, statErr := os.Stat(*config.KeyFile); os.IsNotExist(statErr) {
			log.Println("提示：私钥文件不存在 → " + *config.KeyFile)
		}
	}
	fmt.Fprintln(os.Stderr, msg)
	// 先落盘再退出，避免 stdout 重定向到日志文件时缓冲未刷
	time.Sleep(200 * time.Millisecond)
	os.Exit(1)
}

func startTlsServer(addr, certFile, keyFile string, handler http.Handler) {
	log.Printf("starting TLS Server  %s\n", addr)
	s := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 10 * time.Second,
		// 不能设 WriteTimeout：TLS 端口是给播放器串流用的，一个完整音频
		// 走完响应头之后还要持续几十秒到几分钟，10 秒硬超时会把长曲直接
		// 掐断（现象是「播一会儿就断」）。连接空闲由 ReadTimeout 兜住。
		WriteTimeout:   0,
		MaxHeaderBytes: 1 << 20,
	}
	if *config.Insecure {
		// 无证书模式：直接按明文在 TLS 端口上提供服务，供「客户端把 HTTPS
		// 代理指向本端口」这种用法的兜底，避免因证书缺失导致整个进程不可用。
		log.Println("WARNING: TLS 端口以无证书模式运行（-insecure），不支持 TLS 握手")
		if err := s.ListenAndServe(); err != nil {
			fatal(fmt.Sprintf("监听 HTTPS 端口 %s（无证书模式）", addr), err)
		}
		return
	}
	if err := s.ListenAndServeTLS(certFile, keyFile); err != nil {
		fatal(fmt.Sprintf("监听 HTTPS 端口 %s（证书 %s / 私钥 %s）", addr, certFile, keyFile), err)
	}
}
func startServer(addr string, handler http.Handler) {
	log.Printf("starting Server  %s\n", addr)
	s := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 10 * time.Second,
		// 不能设 WriteTimeout：TLS 端口是给播放器串流用的，一个完整音频
		// 走完响应头之后还要持续几十秒到几分钟，10 秒硬超时会把长曲直接
		// 掐断（现象是「播一会儿就断」）。连接空闲由 ReadTimeout 兜住。
		WriteTimeout:   0,
		MaxHeaderBytes: 1 << 20,
	}
	if err := s.ListenAndServe(); err != nil {
		fatal(fmt.Sprintf("监听 HTTP 端口 %s", addr), err)
	}
}
func printMemStats() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	log.Printf("Alloc = %v TotalAlloc = %v Sys = %v NumGC = %v HeapInuse= %v \n", m.Alloc/1024, m.TotalAlloc/1024, m.Sys/1024, m.NumGC, m.HeapInuse/1024)
}
