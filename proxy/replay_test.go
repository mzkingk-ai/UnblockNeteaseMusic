package proxy

import (
	"bufio"
	"errors"
	"net"
	"testing"
)

// fakeConn 依次吐出预置的字节块，全部吐完后返回 EOF。
type fakeConn struct {
	net.Conn
	chunks [][]byte
}

func (f *fakeConn) Read(p []byte) (int, error) {
	if len(f.chunks) == 0 {
		return 0, errors.New("EOF")
	}
	n := copy(p, f.chunks[0])
	if n == len(f.chunks[0]) {
		f.chunks = f.chunks[1:]
	} else {
		f.chunks[0] = f.chunks[0][n:]
	}
	return n, nil
}
func (f *fakeConn) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeConn) Close() error                { return nil }

func newTestReplay(f *fakeConn) *replayConn {
	return &replayConn{Conn: &bufferedConn{Conn: f, r: bufio.NewReaderSize(f, 16)}, capture: true}
}

// 平时读到什么就是什么；握手失败进入重放模式后，才把攒的字节从头再发一遍。
func TestReplayConnReplaysOnDemand(t *testing.T) {
	const stream = "helloworld!abcdef"
	f := &fakeConn{chunks: [][]byte{[]byte("hello"), []byte("world!"), []byte("abcdef")}}
	rc := newTestReplay(f)
	buf := make([]byte, 4)
	read := func(t *testing.T, n int) string {
		got := make([]byte, 0, n)
		for len(got) < n {
			k, err := rc.Read(buf)
			if err != nil {
				break
			}
			got = append(got, buf[:k]...)
		}
		return string(got)
	}
	// 第一次握手：读到的必须是干净的一条流，不能每段重复
	if got := read(t, len(stream)); got != stream {
		t.Fatalf("首次读取 = %q，期望 %q", got, stream)
	}
	// 握手失败 → 切重放模式 → 同一批字节原样再来一遍
	rc.startReplay()
	if got := read(t, len(stream)); got != stream {
		t.Fatalf("重放 = %q，期望 %q", got, stream)
	}
	// 副本吐完后回到真实连接（这里已 EOF）
	if k, err := rc.Read(buf); err == nil || k != 0 {
		t.Fatalf("副本耗尽后应读到 EOF，实际 n=%d err=%v", k, err)
	}
}

func TestReplayConnStopCapture(t *testing.T) {
	f := &fakeConn{chunks: [][]byte{[]byte("GET / HTTP/1.1\r\n"), []byte("Host: x\r\n\r\n")}}
	rc := newTestReplay(f)
	rc.stopCapture()
	buf := make([]byte, 8)
	if n, _ := rc.Read(buf); string(buf[:n]) != "GET / HT" {
		t.Fatalf("读到 %q", buf[:n])
	}
	_, _ = rc.Read(buf)
	rc.mu.Lock()
	pending := len(rc.pending)
	rc.mu.Unlock()
	if pending != 0 {
		t.Fatalf("stopCapture 之后仍累积了 %d 字节", pending)
	}
}

func TestRetryableTLS(t *testing.T) {
	for _, s := range []string{
		"local error: tls: bad record MAC",
		"tls: packet length too long",
		"tls: unexpected message",
		"tls: record overflow",
	} {
		if !retryableTLS(errors.New(s)) {
			t.Errorf("%q 应当可重试", s)
		}
	}
	for _, s := range []string{
		"remote error: tls: bad certificate",
		"x509: certificate signed by unknown authority",
		"EOF",
	} {
		if retryableTLS(errors.New(s)) {
			t.Errorf("%q 不应重试", s)
		}
	}
}
