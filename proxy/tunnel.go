package proxy

import (
	"errors"
	"io"
	"net"
	"net/http"
)

// connectEstablished 是 CONNECT 成功后回给客户端的固定应答。
const connectEstablished = "HTTP/1.0 200 Connection Established\r\n\r\n"

var errNoHijack = errors.New("http server does not support hijacking")

// hijackTunnel 接管这条 CONNECT 并回 200，返回带缓冲的客户端连接。
//
// 返回的 src 同时包含连接和 bufio 缓冲区：Chrome/Electron 内核会在 CONNECT
// 之后立刻把 ClientHello 一起塞进来，不等我们的响应，这部分字节已经躺在
// buf.Reader 里了。只搬 net.Conn 会把它们丢掉，于是服务端等 ClientHello、
// 客户端等响应，双方一起超时，日志里只有一长串 CONNECT 却没有 Transport。
func hijackTunnel(rw http.ResponseWriter) (net.Conn, *bufferedConn, error) {
	hij, ok := rw.(http.Hijacker)
	if !ok {
		return nil, nil, errNoHijack
	}
	client, buf, err := hij.Hijack()
	if err != nil {
		return nil, nil, err
	}
	if _, err := buf.WriteString(connectEstablished); err == nil {
		_ = buf.Flush()
	}
	return client, &bufferedConn{Conn: client, r: buf.Reader}, nil
}

// pipeBoth 双向搬运直到任一侧关闭，返回前把两边都关掉。
func pipeBoth(client net.Conn, src io.Reader, upstream net.Conn) {
	defer client.Close()
	defer upstream.Close()
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, src)
		done <- struct{}{}
	}()
	_, _ = io.Copy(client, upstream)
	<-done
}
