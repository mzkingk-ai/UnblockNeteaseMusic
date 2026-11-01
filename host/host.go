package host

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"github.com/cnsilvan/UnblockNeteaseMusic/common"
	"github.com/cnsilvan/UnblockNeteaseMusic/config"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func getWinSystemDir() string {
	dir := ""
	if runtime.GOOS == "windows" {
		dir = os.Getenv("windir")
	}

	return dir
}
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	//if os.IsNotExist(err) {
	//	return false, nil
	//}
	return false, err
}
func appendToFile(fileName string, content string) error {
	f, err := os.OpenFile(fileName, os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Println("appendToFile cacheFileList.yml file create failed. err: " + err.Error())
	} else {
		defer f.Close()
		//n, _ := f.Seek(0, io.SeekEnd)
		//_, err = f.WriteAt([]byte(content), n)
		_, err = f.WriteString(content)
		if err != nil {
			log.Println("appendToFile write file fail:", err)
			return err
		}
	}
	return err
}
func restoreHost(hostPath string) error {
	host, err := os.Create(hostPath)
	if err != nil {
		log.Println("open file fail:", err)
		return err
	}
	defer host.Close()
	gBackup, err := os.Open(hostPath + ".gBackup")
	if err != nil {
		log.Println("Open write file fail:", err)
		return err
	}
	defer gBackup.Close()
	br := bufio.NewReader(gBackup)
	for {
		line, _, err := br.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Println("read err:", err)
			return err
		}
		newLine := string(line)

		_, err = host.WriteString(newLine + "\n")
		if err != nil {
			log.Println("write to file fail:", err)
			return err
		}
	}
	return nil
}
func appendToHost(hostPath string) error {
	content := " \n# UnblockNetEaseMusic（Go）\n"
	for domain, _ := range common.HostDomain {
		content += common.ProxyIp + " " + domain + "\n"
	}
	return appendToFile(hostPath, content)
}
func backupHost(hostPath string) (bool, error) {
	containsProxyDomain := false
	host, err := os.Open(hostPath)
	if err != nil {
		log.Println("open file fail:", err)
		return containsProxyDomain, err
	}
	defer host.Close()
	gBackup, err := os.Create(hostPath + ".gBackup")
	if err != nil {
		log.Println("Open write file fail:", err)
		return containsProxyDomain, err
	}
	defer gBackup.Close()
	br := bufio.NewReader(host)
	for {
		line, _, err := br.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Println("read err:", err)
			return containsProxyDomain, err
		}
		newLine := string(line)
		if !containsProxyDomain {
			if strings.Contains(strings.ToUpper(newLine), strings.ToUpper("UnblockNetEaseMusic")) {
				containsProxyDomain = true
				log.Println("Found UnblockNetEaseMusic Line")
			}
			for domain, _ := range common.ProxyDomain {
				if strings.Contains(newLine, domain) {
					containsProxyDomain = true
					log.Println("Found ProxyDomain Line")
				}
			}
		}
		_, err = gBackup.WriteString(newLine + "\n")
		if err != nil {
			log.Println("write to file fail:", err)
			return containsProxyDomain, err
		}
	}
	return containsProxyDomain, nil
}

// Exclude UnblockNetEaseMusic related host
func excludeRelatedHost(hostPath string) error {
	host, err := os.Create(hostPath)
	if err != nil {
		log.Println("open file fail:", err)
		return err
	}
	defer host.Close()
	gBackup, err := os.Open(hostPath + ".gBackup")
	if err != nil {
		log.Println("Open write file fail:", err)
		return err
	}
	defer gBackup.Close()
	br := bufio.NewReader(gBackup)
	for {
		line, _, err := br.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Println("read err:", err)
			return err
		}
		newLine := string(line)
		needWrite := true
		for domain, _ := range common.ProxyDomain {
			if strings.Contains(newLine, domain) {
				needWrite = false
				break
			}
		}
		if needWrite && strings.Contains(strings.ToUpper(newLine), strings.ToUpper("UnblockNetEaseMusic")) {
			needWrite = false
		}
		if needWrite && len(strings.TrimSpace(newLine)) == 0 {
			needWrite = false
		}
		if needWrite {
			_, err = host.WriteString(newLine + "\n")
			if err != nil {
				log.Println("write to file fail:", err)
				return err
			}
		}
	}
	return nil
}
func resolveIp(domain string) (ip string, err error) {

	return "", nil
}

// publicDnsServer 是查询「网易真实地址」时用的外部 DNS。
// 不能用系统 DNS：转发模式下系统 DNS 就是本机 dnsmasq，它会把 music.163.com
// 直接答回 192.168.3.1，于是 HostDomain 被写成局域网地址，之后自己去请求
// 网易 API 就变成请求自己，服务端授权信息永远拿不到，任何歌都解锁不了。
var publicDnsServer = "223.5.5.5:53"

// resolveUpstreamIps 用外部 DNS 解析网易域名，结果写回 common.HostDomain。
//
// resolveIps() 走的是系统解析器（net.ResolveIPAddr），在 Padavan 上等价于
// 问本机 dnsmasq。转发模式下 dnsmasq 里正好有我们写进去的
// address=/music.163.com/<本机 IP>，于是拿到的是自己的地址。
// 这里必须绕开系统解析器，直接问公共 DNS。
//
// 解析失败时不改 HostDomain：内置 IP（59.111.181.35）仍然可用，
// 而写进一个局域网地址会让整条解锁链路彻底失效。
func resolveUpstreamIps() {
	for domain := range common.HostDomain {
		ip, err := queryPublicDns(domain, publicDnsServer)
		if err != nil || len(ip) == 0 {
			log.Printf("WARNING: 用 %s 解析 %s 失败(%v)，继续使用当前地址 %s",
				publicDnsServer, domain, err, common.HostDomain[domain])
			continue
		}
		if !isPublicIpv4(ip) {
			log.Printf("WARNING: %s 解析到非公网地址 %s（疑似被劫持），继续使用 %s",
				domain, ip, common.HostDomain[domain])
			continue
		}
		if common.HostDomain[domain] != ip {
			log.Printf("HostDomain 更新：%s %s -> %s", domain, common.HostDomain[domain], ip)
		}
		common.HostDomain[domain] = ip
	}
}

// isPublicIpv4 判断是否为可路由的公网 IPv4。
// 用来挡掉 127.0.0.1 / 192.168.x.x / 10.x.x.x 这类「答回自己」的结果。
func isPublicIpv4(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return false
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast())
}

// queryPublicDns 直接向 publicDnsServer 发一次 A 记录查询。
func queryPublicDns(domain, server string) (string, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.Dial("udp", server)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	id := uint16(time.Now().UnixNano() & 0xffff)
	msg := buildDnsQuery(id, domain)
	if _, err = conn.Write(msg); err != nil {
		return "", err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	return parseDnsAnswer(buf[:n], id)
}

// buildDnsQuery 拼一个标准的 A 记录查询报文（RD=1，单问题，class IN）。
func buildDnsQuery(id uint16, domain string) []byte {
	buf := make([]byte, 0, 512)
	buf = append(buf, byte(id>>8), byte(id))
	buf = append(buf, 0x01, 0x00) // flags: RD
	buf = append(buf, 0, 1)       // QDCOUNT
	buf = append(buf, 0, 0)       // ANCOUNT
	buf = append(buf, 0, 0)       // NSCOUNT
	buf = append(buf, 0, 0)       // ARCOUNT
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 {
			continue
		}
		buf = append(buf, byte(len(label)))
		buf = append(buf, label...)
	}
	buf = append(buf, 0)    // 名字结束
	buf = append(buf, 0, 1) // QTYPE=A
	buf = append(buf, 0, 1) // QCLASS=IN
	return buf
}

// skipDnsName 跳过（可能的压缩指针 +）标签序列，返回下一个字段的偏移。
func skipDnsName(msg []byte, off int) (int, bool) {
	for off < len(msg) {
		l := int(msg[off])
		if l == 0 {
			return off + 1, true
		}
		if l&0xc0 == 0xc0 { // 压缩指针，占 2 字节
			if off+2 > len(msg) {
				return 0, false
			}
			return off + 2, true
		}
		off += 1 + l
	}
	return 0, false
}

// parseDnsAnswer 从应答里取第一条 A 记录；没有 A 记录视为失败。
func parseDnsAnswer(msg []byte, id uint16) (string, error) {
	if len(msg) < 12 {
		return "", fmt.Errorf("dns 应答过短")
	}
	if binary.BigEndian.Uint16(msg[0:2]) != id {
		return "", fmt.Errorf("dns 事务号不匹配")
	}
	if rcode := msg[3] & 0x0f; rcode != 0 {
		return "", fmt.Errorf("dns rcode=%d", rcode)
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	off := 12
	for i := 0; i < qd; i++ {
		var ok bool
		if off, ok = skipDnsName(msg, off); !ok || off+4 > len(msg) {
			return "", fmt.Errorf("dns 问题段损坏")
		}
		off += 4
	}
	for i := 0; i < an; i++ {
		var ok bool
		if off, ok = skipDnsName(msg, off); !ok || off+10 > len(msg) {
			return "", fmt.Errorf("dns 应答段损坏")
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			return "", fmt.Errorf("dns 应答段越界")
		}
		if rtype == 1 && rdlen == 4 { // A
			return net.IP(msg[off : off+4]).String(), nil
		}
		off += rdlen
	}
	return "", fmt.Errorf("dns 无 A 记录")
}

func resolveIps() error {
	for domain, _ := range common.HostDomain {
		// 优先走公共 DNS。转发模式（mode=1）下系统解析器就是本机 dnsmasq，
		// 里面挂着 address=/music.163.com/<本机IP>，用它必然解析到自己，
		// 之后 HostDomain 被写成局域网地址，去请求网易 API 就等于请求自己，
		// 服务端授权信息永远拿不到，任何歌都解锁不了。
		ip := ""
		if pub, perr := queryPublicDns(domain, publicDnsServer); perr == nil && isPublicIpv4(pub) {
			ip = pub
		} else {
			if perr != nil {
				log.Printf("WARNING: 公共 DNS 解析 %s 失败(%v)，回退系统解析器", domain, perr)
			} else {
				log.Printf("WARNING: 公共 DNS 把 %s 解析到非公网地址 %s，回退系统解析器", domain, pub)
			}
			rAddr, err := net.ResolveIPAddr("ip", domain)
			if err != nil {
				log.Printf("Fail to resolve %s, %s\n", domain, err)
				return err
			}
			if len(rAddr.IP) == 0 {
				log.Printf("Fail to resolve %s,IP nil\n", domain)
				return fmt.Errorf("Fail to resolve  %s,Ip length==0 \n", domain)
			}
			ip = rAddr.IP.String()
		}
		if ip == "127.0.0.1" {
			// 上游在这里 panic，而且发生在主 goroutine 里，会直接把进程带走。
			// 触发条件：/etc/hosts 里已残留 music.163.com -> 127.0.0.1（例如上一轮
			// 转发模式没清理干净）。此时上游域名地址不可解析，换回内置 IP 继续跑，
			// 单机代理模式仍然可用；只有确实要改 hosts 时才需要真正解析成功。
			log.Printf("WARNING: %v 解析到 %v（/etc/hosts 可能残留了转发记录），改用内置地址 %s 继续",
				domain, ip, common.ProxyDomain[domain])
			if builtin, ok := common.ProxyDomain[domain]; ok && len(builtin) > 0 {
				common.HostDomain[domain] = builtin
				continue
			}
			return fmt.Errorf("%v ip:%v is error", domain, ip)
		}
		common.HostDomain[domain] = ip

	}
	return nil
}
func getHostsPath() (string, error) {
	hostsPath := "/etc/hosts"
	if runtime.GOOS == "windows" {
		hostsPath = getWinSystemDir()
		hostsPath = filepath.Join(hostsPath, "system32", "drivers", "etc", "hosts")
	} else {
		hostsPath = filepath.Join(hostsPath)
	}
	if exist, err := fileExists(hostsPath); !exist {
		log.Println("Not Found Host File：", hostsPath)
		return hostsPath, err
	}
	return hostsPath, nil
}
func RestoreHosts() error {
	if *config.Mode == 1 {
		hostsPath, err := getHostsPath()
		if err == nil {
			err := restoreHost(hostsPath)
			return err
		}
	}
	return nil
}
func InitHosts() error {
	log.Println("-------------------Init Host-------------------")
	if *config.Mode == 1 { //hosts mode
		hostsPath, err := getHostsPath()
		if err == nil {
			containsProxyDomain := false
			containsProxyDomain, err = backupHost(hostsPath)
			if err == nil {
				if containsProxyDomain {
					if err = excludeRelatedHost(hostsPath); err == nil {
						err = resolveIps()
						if err != nil {
							return err
						}
						log.Println("HostDomain:", common.HostDomain)
					}
				} else {
					err = resolveIps()
					if err != nil {
						return err
					}
					log.Println("HostDomain:", common.HostDomain)
				}
				if err = appendToHost(hostsPath); err == nil {

				}
			}
		}
		return err
	} else {
		// 纯代理模式下也要拿到「网易真实地址」，否则 serveProxy 会把请求
		// 发回 HostDomain 里那个值。转发模式（mode=1）下系统 DNS 已经
		// 被本机劫持，resolveIps() 必然解析到自己，这里统一走外部 DNS。
		resolveUpstreamIps()
		log.Println("HostDomain:", common.HostDomain)
		return nil
	}

}
