// Package learn 自动发现「该被劫持但没在集合里」的网易接口 IP。
//
// 为什么需要它：
//
//	iPhone/新版客户端解析 interface*.music.163.com 时常常不走系统 DNS（客户端
//	自带 HTTPDNS），拿到的节点 IP 和路由器自己的解析结果不一样。实测同一时刻
//	路由器解析出 220.197.30.67（在 nft 集合里），而 iPhone 连的是
//	220.197.30.75 / 123.138.126.190 —— 都不在集合里，于是它的 player/url 请求
//	根本没进代理，表现为「PC 上能解锁、iPhone 上还是试听」。
//
// 做法（不猜 IP、不扫网段）：
//  1. 读 /proc/net/nf_conntrack，收集局域网客户端发往 443 的公网目标；
//  2. 对不在集合里的目标做一次 TLS 探测，SNI 固定 interface3.music.163.com；
//  3. 只有对端证书确实是 *.music.163.com 才加进 nft 集合。
//
// 第 2 步是「分类器」：同样一个客户端，还会连音乐 CDN（*.music.126.net），
// 用网易接口的 SNI 去握那只手会直接 unrecognized name 失败，所以既不会误伤，
// 也不会把无关域名加进来。
package learn

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	table     = "inet unm"
	set4      = "unm4"
	probeSNI  = "interface3.music.163.com"
	maxPerRun = 4
)

type Learner struct {
	mu    sync.Mutex
	known map[string]struct{}
}

// Start 在后台起学习循环。interval<=0 时用 15s。
// nft 集合不存在（dont_hijack / use_hosts 模式）时直接返回，不产生任何副作用。
func Start(interval time.Duration) {
	l := &Learner{known: loadSet()}
	if l.known == nil {
		log.Println("learn: nft set " + table + " " + set4 + " not found, auto-learn disabled")
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	log.Printf("learn: auto-learn enabled, interval=%s, set has %d ips\n", interval, len(l.known))
	go l.loop(interval)
}

func (l *Learner) loop(interval time.Duration) {
	for {
		time.Sleep(interval)
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Println("learn panic:", r)
				}
			}()
			l.once()
		}()
	}
}

func (l *Learner) once() {
	cands := l.candidates()
	if len(cands) == 0 {
		return
	}
	learned := 0
	for _, ip := range cands {
		if !isNeteaseHost(ip) {
			continue
		}
		if l.add(ip) {
			learned++
			log.Println("learn: hijacked new netease ip " + ip)
		}
	}
	if learned > 0 {
		log.Printf("learn: added %d ip(s), total known=%d\n", learned, len(l.known))
	}
}

// candidates 从 conntrack 里挑出「局域网 -> 公网:443」里还没在集合里的目标。
func (l *Learner) candidates() []string {
	data, err := os.ReadFile("/proc/net/nf_conntrack")
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]struct{}, 16)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "dport=443") || !strings.Contains(line, "ESTABLISHED") {
			continue
		}
		src, dst := firstPair(line)
		if len(dst) == 0 || len(src) == 0 {
			continue
		}
		if !isPrivate(src) || isPrivate(dst) {
			continue
		}
		if strings.Contains(dst, ":") { // 先只做 IPv4，IPv6 集合另说
			continue
		}
		l.mu.Lock()
		_, ok := l.known[dst]
		l.mu.Unlock()
		if ok {
			continue
		}
		if _, dup := seen[dst]; dup {
			continue
		}
		seen[dst] = struct{}{}
		out = append(out, dst)
		if len(out) >= maxPerRun {
			break
		}
	}
	return out
}

// firstPair 取 conntrack 行里第一组 src=/dst=（原始方向，第二个是回包方向）。
func firstPair(line string) (src, dst string) {
	fields := strings.Fields(line)
	for _, f := range fields {
		switch {
		case strings.HasPrefix(f, "src=") && src == "":
			src = f[4:]
		case strings.HasPrefix(f, "dst=") && dst == "":
			dst = f[4:]
		}
		if src != "" && dst != "" {
			break
		}
	}
	return
}

// isNeteaseHost 用固定 SNI 握手一次，看对端证书是不是 *.music.163.com。
func isNeteaseHost(ip string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "443"), 4*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	c := tls.Client(conn, &tls.Config{ServerName: probeSNI, InsecureSkipVerify: true})
	if err := c.Handshake(); err != nil {
		return false
	}
	cs := c.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return false
	}
	cert := cs.PeerCertificates[0]
	for _, name := range cert.DNSNames {
		if name == "*.music.163.com" || name == "music.163.com" {
			return true
		}
	}
	cn := cert.Subject.CommonName
	return cn == "*.music.163.com" || cn == "music.163.com"
}

func (l *Learner) add(ip string) bool {
	l.mu.Lock()
	if _, ok := l.known[ip]; ok {
		l.mu.Unlock()
		return false
	}
	l.known[ip] = struct{}{}
	l.mu.Unlock()
	// nft 是原子操作，重复添加不会报错，但加上面那层判断可以少一次 fork。
	cmd := exec.Command("nft", "add", "element", "inet", "unm", set4, "{", ip, "}")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("learn: nft add %s fail: %v %s\n", ip, err, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

func loadSet() map[string]struct{} {
	out, err := exec.Command("nft", "list", "set", "inet", "unm", set4).CombinedOutput()
	if err != nil {
		return nil
	}
	known := make(map[string]struct{}, 16)
	for _, tok := range strings.FieldsFunc(string(out), func(r rune) bool {
		return r == ' ' || r == ',' || r == '{' || r == '}' || r == '\n' || r == '\t'
	}) {
		if net.ParseIP(tok) != nil && !strings.Contains(tok, ":") {
			known[tok] = struct{}{}
		}
	}
	return known
}

func isPrivate(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	if p.IsLoopback() || p.IsLinkLocalUnicast() || p.IsUnspecified() {
		return true
	}
	if p4 := p.To4(); p4 != nil {
		switch {
		case p4[0] == 10:
			return true
		case p4[0] == 172 && p4[1] >= 16 && p4[1] <= 31:
			return true
		case p4[0] == 192 && p4[1] == 168:
			return true
		case p4[0] == 100 && p4[1] >= 64 && p4[1] <= 127:
			return true
		case p4[0] == 169 && p4[1] == 254:
			return true
		}
	}
	return false
}

var _ = fmt.Sprintf
