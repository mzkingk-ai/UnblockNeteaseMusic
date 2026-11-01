package network

import (
	"bytes"
	"crypto/tls"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

var (
	httpClient   *http.Client
	directClient *http.Client
)

func init() {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          300,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		MaxConnsPerHost:       100,
	}
	directClient = &http.Client{
		Transport: tr.Clone(),
	}
	// 重要：InsecureSkipVerify 必须挂在**独立的一份** Transport 上。
	// 原先直接写在 tr 上再传进 httpClient，而 directClient 是 tr.Clone() ——
	// Clone 会复制当时的 TLSClientConfig，顺序先后决定谁跳校验，极易看漏。
	// 独立出来之后语义明确：
	//   directClient —— 直连音源/第三方 API，自己控制目标，不校验证书可接受；
	//   httpClient   —— 转发客户端发往网易的请求，**不**跳过校验。
	// 不跳过是必须的：serveProxy 把 Host 改写成真实 IP 后 TLS 的 SNI 也变成 IP，
	// 而网易证书没有 IP SAN，跳过校验时又报
	//   x509: cannot validate certificate for 220.197.30.67
	// 两条路都跑不通。正确做法见 Request：转发 IP 目标时显式指定 ServerName。
	httpClient = &http.Client{
		Transport: tr.Clone(),
	}

}

type ClientRequest struct {
	Method               string
	RemoteUrl            string
	Host                 string
	ForbiddenEncodeQuery bool
	Header               http.Header
	Body                 io.Reader
	Cookies              []*http.Cookie
	Proxy                bool
	ConnectTimeout       time.Duration
	// ResponseHeaderTimeout 覆盖全局的 5s 响应头超时。
	// 全局值是按 kuwo/kugou 这类快音源定的；pyncmd（第三方 API）实测
	// 要 4.5~9.4s 才回响应头，5s 会被反复截断并触发重试，结果在 6s 的
	// 并发窗口里一次都拿不到。0 表示沿用全局默认。
	ResponseHeaderTimeout time.Duration
	// HTTPProxy 显式指定本次请求走的代理，形如 "http://127.0.0.1:7890"。
	// 空串表示沿用默认（http.ProxyFromEnvironment，即看 HTTP_PROXY/HTTPS_PROXY）。
	// 用于「直连失败后改走本地代理再试一次」：不改环境变量（那会影响所有请求），
	// 只对这一条请求生效。
	HTTPProxy string
}

func Request(clientRequest *ClientRequest) (*http.Response, error) {
	//log.Println("remoteUrl:" + clientRequest.RemoteUrl)
	method := clientRequest.Method
	remoteUrl := clientRequest.RemoteUrl
	host := clientRequest.Host
	header := clientRequest.Header
	body := clientRequest.Body
	proxy := clientRequest.Proxy
	cookies := clientRequest.Cookies
	connectTimeout := clientRequest.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	var resp *http.Response
	request, err := http.NewRequest(method, remoteUrl, body)
	if err != nil {
		log.Printf("NewRequest fail:%v\n", err)
		return resp, nil
	}
	if !clientRequest.ForbiddenEncodeQuery {
		request.URL.RawQuery = strings.Replace(request.URL.Query().Encode(), "+", "%20", -1)
	}
	if len(host) > 0 {
		request.Host = host
		request.Header.Set("host", host)
	}
	if len(request.URL.Scheme) == 0 {
		if request.TLS != nil {
			request.URL.Scheme = "https"
		} else {
			request.URL.Scheme = "http"
		}
	}

	if proxy { //keep headers&cookies for Direct
		if header != nil {
			request.Header = header
		}
		for _, value := range cookies {
			request.AddCookie(value)
		}
	}
	accept := "application/json, text/plain, */*"
	acceptEncoding := "gzip, deflate"
	acceptLanguage := "zh-CN,zh;q=0.9"
	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/94.0.4606.61 Safari/537.36"

	if header != nil {
		accept = header.Get("accept")
		if len(accept) == 0 {
			accept = "application/json, text/plain, */*"
		}
		acceptEncoding = header.Get("accept-encoding")
		if len(acceptEncoding) == 0 {
			acceptEncoding = "gzip, deflate"
		}
		acceptLanguage = header.Get("accept-language")
		if len(acceptLanguage) == 0 {
			acceptLanguage = "zh-CN,zh;q=0.9"
		}
		userAgent = header.Get("user-agent")
		if len(userAgent) == 0 {
			userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/94.0.4606.61 Safari/537.36"
		}
		Range := header.Get("range")
		if len(Range) > 0 {
			request.Header.Set("range", Range)
		}
	}

	acceptEncodingArr := strings.Split(acceptEncoding, ",")
	var newAcceptEncodingArr []string
	for _, e := range acceptEncodingArr {
		if strings.TrimSpace(e) != "br" {
			newAcceptEncodingArr = append(newAcceptEncodingArr, strings.TrimSpace(e))
		}
	}

	request.Header.Set("accept", accept)
	request.Header.Set("accept-encoding", strings.Join(newAcceptEncodingArr, ", "))
	request.Header.Set("accept-language", acceptLanguage)
	request.Header.Set("user-agent", userAgent)
	client := directClient
	// 判断走哪个 client 看的是「目标地址」而不是 Host 头：serveProxy 把
	// 网易域名换成了真实 IP 转发，Host 头这时是 IP，用原逻辑
	// common.HostDomain[request.Host] 永远查不到，直接落到 directClient。
	targetHost := request.URL.Hostname()
	_, isNetease := common.HostDomain[targetHost]
	if !isNetease {
		// 目标已换成 IP 时按 IP 反查回域名，再判一次
		if name := common.HostNameOf(targetHost); len(name) > 0 {
			targetHost = name
			isNetease = true
		} else if _, ok := common.HostDomain[request.Host]; ok {
			targetHost = request.Host
			isNetease = true
		}
	}
	if isNetease {
		client = httpClient
		// 目标是 IP 时 TLS 的 SNI 会跟着变成那个 IP，而网易证书只有 DNS SAN，
		// 必须显式把 ServerName 指回原域名，否则报
		//   x509: cannot validate certificate for 220.197.30.67
		// 注意 Host 头不受影响，仍是调用方设定的值。
		if tr, ok := client.Transport.(*http.Transport); ok && net.ParseIP(targetHost) == nil {
			t2 := tr.Clone()
			tn := t2.TLSClientConfig
			if tn == nil {
				tn = &tls.Config{}
			} else {
				tn = tn.Clone()
			}
			if net.ParseIP(request.URL.Hostname()) != nil {
				tn.ServerName = targetHost
			}
			t2.TLSClientConfig = tn
			client = &http.Client{Transport: t2}
		}
	}

	// 需要更长的响应头超时（pyncmd 这类慢接口）或指定代理时，克隆一份 client
	// 换掉 Transport，不改全局配置 —— kuwo/kugou 等快音源仍按原超时/直连，
	// 避免被慢接口或代理拖累。
	// 两个覆盖项合并成一次 Clone：分两次写会互相覆盖（第二次拿的还是原 Transport）。
	if clientRequest.ResponseHeaderTimeout > 0 || clientRequest.HTTPProxy != "" {
		if tr, ok := client.Transport.(*http.Transport); ok {
			t2 := tr.Clone()
			if clientRequest.ResponseHeaderTimeout > 0 {
				t2.ResponseHeaderTimeout = clientRequest.ResponseHeaderTimeout
			}
			if clientRequest.HTTPProxy != "" {
				if pu, err := url.Parse(clientRequest.HTTPProxy); err == nil {
					t2.Proxy = http.ProxyURL(pu)
					// ForceAttemptHTTP2 必须保持 true（默认值），别关。
					// 实测关掉后代理在 CONNECT 隧道里直接发 h2 帧（\x00\x00\x12\x04...），
					// 而 Go 按 HTTP/1.x 解析明文响应，报
					//   malformed HTTP response "\x00\x00\x12\x04..."
					// 每次请求都失败。保持 true 时 Go 会走 h2 客户端，正常。
				} else {
					log.Println("invalid HTTPProxy:", clientRequest.HTTPProxy, err)
				}
			}
			client = &http.Client{Transport: t2}
		}
	}
	// }
	resp, err = client.Do(request)
	if err != nil {
		//log.Println(request.Method, request.URL.String(), host)
		log.Printf("http.Client.Do fail:%v\n", err)
		return resp, err
	}

	return resp, err

}
func StealResponseBody(resp *http.Response) (io.Reader, error) {
	encode := resp.Header.Get("Content-Encoding")
	enableGzip := false
	if len(encode) > 0 && (strings.Contains(encode, "gzip") || strings.Contains(encode, "deflate")) {
		enableGzip = true
	}
	if enableGzip {
		resp.Header.Del("Content-Encoding")
		body, err := utils.UnGzipV2(resp.Body)
		if err != nil {
			log.Println("read  body fail")
			return resp.Body, err
		}
		return body, err
	}
	return resp.Body, nil

}
func GetResponseBody(resp *http.Response, keepBody bool) ([]byte, error) {
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("read body fail")
		return body, err
	}
	resp.Body.Close()
	if keepBody {
		bodyHold := ioutil.NopCloser(bytes.NewBuffer(body))
		resp.Body = bodyHold
	}
	encode := resp.Header.Get("Content-Encoding")
	enableGzip := false
	if len(encode) > 0 && (strings.Contains(encode, "gzip") || strings.Contains(encode, "deflate")) {
		enableGzip = true
	}
	if enableGzip {
		if !keepBody {
			resp.Header.Del("Content-Encoding")
		}
		body, err = utils.UnGzip(body)
		if err != nil {
			log.Println("read  body fail")
			return body, err
		}
	}
	return body, err
}
