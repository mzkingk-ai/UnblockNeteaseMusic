package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"time"
)

// 用指定 SNI 连 IP:443，打印对端证书的 CN/DNS 名，用来判断某个 IP 是不是网易家的。
func main() {
	ip := os.Args[1]
	sni := "interface3.music.163.com"
	if len(os.Args) > 2 {
		sni = os.Args[2]
	}
	conn, err := net.DialTimeout("tcp", ip+":443", 6*time.Second)
	if err != nil {
		fmt.Printf("%s dial: %v\n", ip, err)
		return
	}
	defer conn.Close()
	c := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err := c.Handshake(); err != nil {
		fmt.Printf("%s tls: %v\n", ip, err)
		return
	}
	st := c.ConnectionState()
	fmt.Printf("%s  ver=%x  alpn=%q\n", ip, st.Version, st.NegotiatedProtocol)
	for _, cert := range st.PeerCertificates {
		fmt.Printf("   CN=%q  O=%q  DNS=%v  NotAfter=%s\n", cert.Subject.CommonName, cert.Subject.Organization, cert.DNSNames, cert.NotAfter.Format("2006-01-02"))
	}
}
