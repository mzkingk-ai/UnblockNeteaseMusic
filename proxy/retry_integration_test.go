package proxy

import "testing"

// 用一个会「假失败一次」的 crypto/tls 替代品不现实，这里改为验证核心不变量：
// 重放模式下，交给下一次 tls.Server 的字节必须与上一次完全一致。
func TestReplayFeedsIdenticalBytesToRetry(t *testing.T) {
	// 造一段类似 ClientHello 的字节
	hello := []byte{0x16, 0x03, 0x01, 0x00, 0x2f, 0x01, 0x00, 0x00, 0x2b, 0x03, 0x03}
	f := &fakeConn{chunks: [][]byte{hello[:4], hello[4:8], hello[8:]}}
	rc := newTestReplay(f)
	buf := make([]byte, 3)

	readAll := func() []byte {
		got := []byte{}
		for len(got) < len(hello) {
			n, err := rc.Read(buf)
			if err != nil {
				break
			}
			got = append(got, buf[:n]...)
		}
		return got
	}
	first := readAll()
	if string(first) != string(hello) {
		t.Fatalf("首次读取损坏：%x", first)
	}
	rc.startReplay()
	second := readAll()
	if string(second) != string(hello) {
		t.Fatalf("重放字节不一致：%x vs %x", second, hello)
	}
}
