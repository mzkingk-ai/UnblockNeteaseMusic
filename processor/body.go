package processor

import (
	"bytes"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/processor/crypto"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
)

// bodyCodec 记住一个响应体是「怎么包出来的」，回写时按同样的方式包回去。
//
// 网易的响应最多叠三层：
//  1. HTTP 的 Content-Encoding: gzip（客户端显式带 Accept-Encoding: gzip 时
//     Go 不会自动解，得我们自己解）；
//  2. 接口自己压的内层 gzip；
//  3. 老接口的 AES-ECB 密文。
//
// 少解一层，JSON 就解不出来（JsonBody 变 nil，换源逻辑全线跳过）；回写时少包
// 一层，客户端会因为报文结构变了把它当成坏包。实测 iPhone 的 player/url：
// 739B gzip -> 704B AES-ECB -> 689B gzip -> 1104B JSON。
type bodyCodec struct {
	aesKey    string
	encrypted bool
	innerGzip bool
}

// decodeBody 把原始响应体解开，返回明文 JSON 字节（可能仍然不是 JSON，
// 调用方用 utils.ParseJson 判断）。
func decodeBody(raw []byte, contentEncoding, aesKey string) ([]byte, bodyCodec) {
	codec := bodyCodec{aesKey: aesKey}
	dec := raw
	// 1. 外层 Content-Encoding
	if isGzipEncoding(contentEncoding) {
		if unzipped, err := utils.UnGzip(dec); err == nil {
			dec = unzipped
		}
	}
	// 2. 密文前后都可能出现一层 gzip，先剥掉再判断是不是密文
	if unzipped, ok := gunzipIfNeeded(dec); ok {
		dec = unzipped
	}
	// 3. AES-ECB 密文。注意必须先确认「不是明文 JSON」再解密：
	//    AesDecryptECB 只要长度是 16 的倍数就会返回成功，明文 JSON 恰好撞上
	//    16 的倍数时会被解成乱码，所以这里用「能不能解析成 JSON」来判定，
	//    和旧逻辑保持一致。
	if utils.ParseJson(dec) != nil {
		return dec, codec
	}
	plain, encrypted := crypto.AesDecryptECB(dec, []byte(aesKey))
	if !encrypted {
		return dec, codec
	}
	codec.encrypted = true
	if unzipped, ok := gunzipIfNeeded(plain); ok {
		plain = unzipped
		codec.innerGzip = true
	}
	return plain, codec
}

// encode 把改好的 JSON 按原来的层次重新包回去。
func (c bodyCodec) encode(plain []byte) ([]byte, error) {
	out := plain
	if c.innerGzip {
		gz, err := utils.Gzip(out)
		if err != nil {
			return nil, err
		}
		out = gz
	}
	if c.encrypted {
		out = crypto.AesEncryptECB(out, []byte(c.aesKey))
	}
	return out, nil
}

// gunzipIfNeeded 只有确实是 gzip 流时才解，避免把普通 JSON 交给 gzip reader。
func gunzipIfNeeded(b []byte) ([]byte, bool) {
	if !isGzipBytes(b) {
		return b, false
	}
	unzipped, err := utils.UnGzip(b)
	if err != nil {
		return b, false
	}
	return unzipped, true
}

func isGzipBytes(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

func isGzipEncoding(contentEncoding string) bool {
	return len(contentEncoding) > 0 &&
		(bytes.Contains([]byte(contentEncoding), []byte("gzip")) ||
			bytes.Contains([]byte(contentEncoding), []byte("deflate")))
}

// headOf 取前 16 字节写日志，用来区分「空 body」和「不是密文的 bin」。
func headOf(b []byte) []byte {
	if len(b) > 16 {
		return b[:16]
	}
	return b
}
