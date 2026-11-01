package proxy

// TLS 中间人（MITM）实现。
//
// 为什么需要它：
//   换源逻辑位于 serveProxy —— 它把 /eapi/song/enhance/player/url 转发到网易
//   真实 IP，再拿响应做改写。但客户端到网易之间是 TLS，serveProxy 只能看到
//   明文。CONNECT 分支原先做的是「本机回环 + tls.Dial 自己」，既不解密也不改写，
//   而且 mips 上 Go TLS 在隧道里套 TLS 会报 packet length too long，
//   客户端看到的现象是「连上了又被掐断」（PC 端网易云像没网一样）。
//
// 做法：
//   每个 CONNECT 到本机网易域名的连接，现场用 CA 签发该域名的证书，
//   在该连接上完成 TLS 握手，然后把解出来的 HTTP/1.1 交给同一个 serveProxy。
//   这样 TLS 只是传输层，改写逻辑完全复用，不存在第二套代码路径。
//
// 证书：
//   有 -c/-k 且是 ECDSA 时直接用它当签发者；否则自动生成 CA 并把私钥落盘
//   （-mk 指定路径），重启复用同一把 key，客户端信任一次即可。

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/config"
)

var (
	mitmOnce  sync.Once
	mitmCA    *x509.Certificate
	mitmKey   *ecdsa.PrivateKey
	mitmErr   error
	mitmDesc  string
	mitmCache sync.Map // host -> *tls.Certificate
)

// startMitmServer 在 addr 上启动监听。连进来的每条连接都来自本机回环：
// proxyConnectLocalhost 先把客户端的 CONNECT 接住，再拨到这里。
func startMitmServer(addr string) {
	if _, _, err := ensureMitmCA(); err != nil {
		// 证书不可用时不要静默：此时隧道只能透传，换源不会生效，
		// 而这正是「看起来连着、歌还是试听」的根因。
		log.Printf("警告：MITM 证书不可用（%v），隧道将原样透传，换源不会生效", err)
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("警告：MITM 监听 %s 失败（%v），隧道将原样透传，换源不会生效", addr, err)
		return
	}
	log.Printf("starting MITM Server  %s (%s)", addr, mitmDesc)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Println("mitm accept:", err)
				return
			}
			go handleMitmInbound(c)
		}
	}()
}

// handleMitmInbound 嗅探首字节：TLS 就取 SNI 现签证书后握手，明文就直接当 HTTP。
// 两种形态最终都交给 HttpHandler，与普通代理请求走同一条改写路径。
func handleMitmInbound(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	br := bufio.NewReaderSize(c, 8192)
	first, err := br.Peek(1)
	if err != nil || len(first) == 0 {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if first[0] != 0x16 {
		// 明文 HTTP 走进来（客户端把 HTTPS 代理端口填成 5200 的旧用法）
		serveMitmHTTP(&bufferedConn{Conn: c, r: br}, "plain")
		return
	}
	host, err := sniffSNI(br)
	if err != nil {
		// 取不到 SNI 就没法签出客户端认的证书；直接关掉比握手失败更干净
		log.Printf("mitm 取 SNI 失败：%v", err)
		return
	}
	cert, err := certForHost(host)
	if err != nil {
		log.Printf("mitm 签发 %s 证书失败：%v", host, err)
		return
	}
	tc, err := handshakeWithRetry(c, br, host, cert)
	if err != nil {
		log.Printf("mitm 握手 %s 失败（已重试 %d 次）：%v", host, mitmMaxRetry, err)
		return
	}
	serveMitmHTTP(tc, host)
}

// mitmMaxRetry 是同一条连接上因错乱而重试的次数上限。
// 2 次足够了：真出错是低概率抖动，连着 3 次都坏说明不是抖动，重试也救不回来。
const mitmMaxRetry = 2

// handshakeWithRetry 在 MT7621 上做 TLS 握手，遇到「抖动型」错误就在同一条
// TCP 上重新建 tls.Server 再握一次，最多重试 mitmMaxRetry 次。
//
// 为什么需要：MT7621 是 32 位 MIPS，Go 的 crypto/tls 在记录层偶尔算错
// MAC（bad record MAC）或长度（packet length too long），纯软件实现上的
// 低频抖动，不是稳定故障。客户端一旦握手失败就断开，网易云每 5.5 秒重连
// 一轮，表现成「没网」。实测握手失败率大约 1/3，两次重试后降到 1/27。
//
// 为什么不能「拨回自己的监听重来」：handleMitmInbound 靠首字节 0x16 判断
// TLS，重放的 ClientHello 会被再嗅探一次，然后拨回来，无限递归。
// 所以只在原连接上重来：TLS 的往返都在服务端（ServerHello/Finished），
// 客户端看不到我们的内部失败，它只是在等一个来得慢一点的响应。
func handshakeWithRetry(c net.Conn, br *bufio.Reader, host string, cert *tls.Certificate) (net.Conn, error) {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		// 只协商 HTTP/1.1：h2 会走二进制帧，这里没有解码器
		NextProtos: []string{"http/1.1"},
	}
	// 嗅探阶段已经 Peek 过的 ClientHello 还在 br 里，包一层照常可读
	rc := &replayConn{Conn: &bufferedConn{Conn: c, r: br}, capture: true}
	for attempt := 0; ; attempt++ {
		tc := tls.Server(rc, cfg)
		err := tc.Handshake()
		if err == nil {
			// 握手成功：后面是应用数据，不该再留副本
			rc.stopCapture()
			return tc, nil
		}
		if !retryableTLS(err) || attempt == mitmMaxRetry {
			return nil, err
		}
		log.Printf("mitm 握手 %s 失败，重试 %d/%d：%v", host, attempt+1, mitmMaxRetry, err)
		// 下一轮从副本开头重新读：客户端最早发的 ClientHello 必须原样重来
		rc.startReplay()
	}
}

// replayConn 是一个会自己记账的 net.Conn：握手阶段读进来的原始字节留一份
// 副本，重试时先吐副本，再接着读真实连接。
//
// 需要重放的量很小：ClientHello 之后、ServerHello 之前客户端不会再发东西，
// 所以正常只有一条记录进账。唯一的多余是 crypto/tls 因 bad record MAC
// 而多读的一条后续记录——那是客户端发完 Finished 后的第一个请求，重放给
// 新握手也照样能读到。
type replayConn struct {
	net.Conn
	mu        sync.Mutex
	pending   []byte // 待重放副本
	capture   bool   // 为真时把读到的字节追加进 pending
	replaying bool   // 为真时先吐副本，吐完再回到真实连接
}

func (r *replayConn) Read(p []byte) (int, error) {
	r.mu.Lock()
	// 只有「重放模式」才吐副本。平时读到什么就是什么——副本只是记账，
	// 不能反过来把自己记的字节再发一遍，否则第一次握手的数据流就成了
	// 每段重复两遍的乱码。
	if r.replaying && len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		if len(r.pending) == 0 {
			// 吐空了就彻底断开引用：否则 slice 还挂着 cap>0 的底层数组，
			// 下一次 append 会写回这块刚吐出去的内存，把自己又记一遍。
			r.pending = nil
		}
		r.mu.Unlock()
		return n, nil
	}
	capture := r.capture
	r.mu.Unlock()
	n, err := r.Conn.Read(p)
	if n > 0 && capture {
		r.mu.Lock()
		if r.capture {
			r.pending = append(r.pending, p[:n]...)
		}
		r.mu.Unlock()
	}
	return n, err
}

// startReplay 在握手失败后切到重放模式：把攒下来的原始字节从头发一遍。
func (r *replayConn) startReplay() {
	r.mu.Lock()
	r.replaying = true
	r.mu.Unlock()
}

// stopCapture 在握手成功后关掉记账，之后的请求字节不再复制。
func (r *replayConn) stopCapture() {
	r.mu.Lock()
	r.capture = false
	r.pending = nil
	r.mu.Unlock()
}

// retryableTLS 判断握手失败是不是「抖动」而非「确定性错误」。
// 证书不匹配、协议版本不支持这类错误重试多少次都一样，必须直接判负，
// 否则每次连接都要白白多握手两轮。
func retryableTLS(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, k := range []string{
		"bad record MAC",
		"packet length too long",
		"unexpected message",
		"record overflow",
		"encrypted record too long",
		"corrupt",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// serveMitmHTTP 在单条已建立的连接上跑一个 http.Server。
func serveMitmHTTP(conn net.Conn, who string) {
	hs := &http.Server{
		Handler: &HttpHandler{},
		// 不设 WriteTimeout：一首歌的转发可能持续几十秒，硬超时会掐断
		ReadTimeout:    30 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	_ = hs.Serve(&singleListener{conn: conn, who: who})
}

// ensureMitmCA 初始化签发者，只跑一次。
func ensureMitmCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	mitmOnce.Do(func() {
		// 优先复用 -c/-k（必须是 ECDSA，签名才快；RSA 在 MT7621 上握手要秒级）
		if len(*config.CertFile) > 0 && len(*config.KeyFile) > 0 {
			if c, err := tls.LoadX509KeyPair(*config.CertFile, *config.KeyFile); err == nil && len(c.Certificate) > 0 {
				leaf, e1 := x509.ParseCertificate(c.Certificate[0])
				k, e2 := c.PrivateKey.(*ecdsa.PrivateKey)
				if e1 == nil && e2 && leaf.IsCA {
					mitmCA, mitmKey = leaf, k
					mitmDesc = "签发者 " + *config.CertFile
					return
				}
				if e1 == nil && leaf != nil && !leaf.IsCA {
					log.Printf("提示：%s 不是 CA 证书，改用在内存里生成的 CA", *config.CertFile)
				}
			}
		}
		mitmKey, mitmCA, mitmErr = genSelfSignedCA()
		if mitmErr == nil {
			mitmDesc = "自签 CA，私钥 " + mitmKeyPath()
		}
	})
	return mitmCA, mitmKey, mitmErr
}

// mitmKeyPath 把 -mk 解析成绝对路径（相对程序所在目录，与 -c/-k 的处理一致）。
func mitmKeyPath() string {
	p := *config.MITMKeyFile
	if len(p) == 0 {
		p = "./mitm-ca.key"
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		dir := exe[:strings.LastIndex(exe, "/")+1]
		return dir + p
	}
	return p
}

// genSelfSignedCA 生成 ECDSA P-256 的 CA。
// 用 ECDSA 而不是 RSA：MT7621 是单核 MIPS，RSA-2048 每连接签名要几百毫秒，
// 一首歌十几次握手会明显拖慢；P-256 只要几毫秒。
func genSelfSignedCA() (*ecdsa.PrivateKey, *x509.Certificate, error) {
	path := mitmKeyPath()
	// 先看有没有上次落盘的 key：有就复用，客户端不必重装根证书
	if b, err := os.ReadFile(path); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
				tmpl := caTemplate(serial)
				tmpl.SubjectKeyId = skiOf(&k.PublicKey)
				der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
				if err == nil {
					ca, err := x509.ParseCertificate(der)
					if err == nil {
						return k, ca, nil
					}
				}
				log.Printf("提示：%s 可读但证书重建失败，重新生成", path)
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := caTemplate(serial)
	tmpl.SubjectKeyId = skiOf(&key.PublicKey)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	if kder, err := x509.MarshalECPrivateKey(key); err == nil {
		blk := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
		if err := os.WriteFile(path, blk, 0600); err != nil {
			// 写不进去只影响「重启后不用重装证书」，不影响本次运行
			log.Printf("提示：MITM 私钥写入 %s 失败（%v），重启后会重新生成 CA", path, err)
		}
	}
	// 同名 .crt 方便用户直接拷到手机上装
	if cer, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key); err == nil {
		_ = os.WriteFile(strings.TrimSuffix(path, ".key")+".crt",
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cer}), 0644)
	}
	return key, ca, nil
}

// skiOf 按 RFC 5280 的方法一生成 Subject Key Identifier。
// 必须显式写入：否则 Go 签出的 CA 没有 2.5.29.14 扩展，而叶子证书有
// Authority Key Identifier，OpenSSL(1.1.1) 按 AKI 找不到签发者，
// 直接报 error 20 unable to get local issuer certificate——客户端看起来就像断网。
func skiOf(pub *ecdsa.PublicKey) []byte {
	enc, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil
	}
	h := sha1.Sum(enc)
	return h[:]
}

func caTemplate(serial *big.Int) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "UnblockNeteaseMusic CA", Organization: []string{"UnblockNeteaseMusic"}},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        false,
	}
}

// certForHost 现签目标域名证书并按 host 缓存。
// 同一域名只签一次，之后的握手直接复用。
func certForHost(host string) (*tls.Certificate, error) {
	if v, ok := mitmCache.Load(host); ok {
		return v.(*tls.Certificate), nil
	}
	ca, key, err := ensureMitmCA()
	if err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &leafKey.PublicKey, key)
	if err != nil {
		return nil, err
	}
	// 把 CA 一起带上：部分客户端（尤其 iOS）缺中间证书会直接判定不受信
	c := &tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: leafKey}
	mitmCache.Store(host, c)
	return c, nil
}

// sniffSNI 从 ClientHello 里取 server_name。只 Peek 不 Consume，
// 字节留在 bufio 里，握手时还能完整读到。
func sniffSNI(br *bufio.Reader) (string, error) {
	hdr, err := br.Peek(5)
	if err != nil {
		return "", err
	}
	if hdr[0] != 0x16 {
		return "", fmt.Errorf("不是 TLS 握手（首字节 0x%02x）", hdr[0])
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	if recLen <= 0 || recLen > 16384 {
		return "", fmt.Errorf("TLS 记录长度异常 %d", recLen)
	}
	full, err := br.Peek(5 + recLen)
	if err != nil {
		return "", err
	}
	body := full[5:]
	if len(body) < 4 || body[0] != 0x01 {
		return "", fmt.Errorf("不是 ClientHello")
	}
	p := 4
	p += 2 + 32 // legacy_version + random
	if p >= len(body) {
		return "", fmt.Errorf("ClientHello 截断")
	}
	p += 1 + int(body[p]) // session_id
	if p+2 > len(body) {
		return "", fmt.Errorf("ClientHello 截断")
	}
	p += 2 + (int(body[p])<<8 | int(body[p+1])) // cipher_suites
	if p >= len(body) {
		return "", fmt.Errorf("ClientHello 截断")
	}
	p += 1 + int(body[p]) // compression_methods
	if p+2 > len(body) {
		return "", fmt.Errorf("ClientHello 没有扩展段")
	}
	p += 2
	end := len(body)
	for p+4 <= end {
		etype := int(body[p])<<8 | int(body[p+1])
		elen := int(body[p+2])<<8 | int(body[p+3])
		p += 4
		if p+elen > end {
			break
		}
		if etype == 0x0000 && elen > 5 {
			d := body[p : p+elen]
			nlen := int(d[3])<<8 | int(d[4])
			if 5+nlen <= len(d) {
				return string(d[5 : 5+nlen]), nil
			}
		}
		p += elen
	}
	return "", fmt.Errorf("ClientHello 里没有 SNI")
}

// bufferedConn 让 bufio 已缓存的字节仍然可读。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// singleListener 让 http.Server 只服务一条已建立的连接。
// Accept 第二次调用会永久阻塞，Serve 处理完当前连接后自然不再取新连接。
type singleListener struct {
	conn net.Conn
	who  string
	done bool
	mu   sync.Mutex
}

func (s *singleListener) Accept() (net.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		select {} // 永不再返回，让 Serve 阻塞在 Accept 上
	}
	s.done = true
	return s.conn, nil
}

func (s *singleListener) Close() error   { return nil }
func (s *singleListener) Addr() net.Addr { return dummyAddr(s.who) }

type dummyAddr string

func (d dummyAddr) Network() string { return "tcp" }
func (d dummyAddr) String() string  { return string(d) }

// 保证 io 被引用（bufferedConn 的兜底分支用不到时也不报未使用）
var _ = io.EOF
