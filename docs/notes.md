# UnblockNeteaseMusic 踩坑记录

这份文档只记录 **UnblockNeteaseMusic 自身**的问题：它的代码、协议、TLS、证书、
DNS 与请求改写、音源实现。每一条都附现象原文和根因，多数带具体数字。

接进 **Padavan** 固件时的集成细节（构建脚本、页面模板、刷机、CI）不在这里，
那份在 padavan-4.4 仓库的 `trunk/user/unblockneteasemusic/PADAVAN.md`。
另有一份 [troubleshooting.md](troubleshooting.md)，是单次故障（新版 PC 端
「连上但不换源」）的完整排查记录。

---

## 0. 先看结论

如果只想记住几条：

1. **只协商 `http/1.1` 是不够的**。新版客户端 ClientHello 只带 `h2`，ALPN 无交集时
   Go 会静默断开，日志里只有一串 CONNECT、没有任何 `Transport:`。
2. **`-c` 指向的不是 ECDSA CA 时，MITM 会在内存里另生成一套 CA**，客户端装的
   `ca.crt` 永远对不上。
3. **证书重签是破坏性的**。CA 一换，所有客户端都得重装。
4. **接管一条客户端请求要四处同时对**：`ProxyDomain` 表里有这个域名、程序自己
   解析网易地址不能问本机、TLS 端口与 MITM 端口不冲突、客户端信任这张 CA。
   任一环没对上，表现都是「歌还是试听」。
5. **失败大多是静默的**（见最后一节）。

---

## 1. 证书与 TLS（坑最多的地方）

这一节的东西**全都不依赖固件**：只要你在自建代理，就会撞上。

### 1.1 客户端 ALPN 只带 `h2`，只声明 `http/1.1` 会被静默断开

**现象**：客户端能连上、封面歌单都正常，唯独该换源的歌放不了。日志里只有
一串每秒重连的 `CONNECT`，**没有任何 `Transport:` 行**：

```
Local Received request CONNECT interfacepc.music.163.com:443 192.168.1.225:49320
```

**根因**：`proxy/mitm.go` 的 `handshakeWithRetry` 只声明 `NextProtos: []string{"http/1.1"}`，
而新版客户端 ClientHello 的 ALPN **只有 `h2`**（抓包 94 次 `h2`、0 次 `http/1.1`）。
没有交集时 Go 发 `no_application_protocol`（alert 120）后断开。

**最阴的一点**：`tls.Server.Handshake()` 在这种情况下**不返回 error**，
于是 `handshakeWithRetry` 误判成功，接着等一个永远不来的请求。日志里连
「握手失败」都看不到。

**正解**：同时声明两者：

```go
NextProtos: []string{"h2", "http/1.1"},
```

不需要引入 `golang.org/x/net/http2`——`http.Server` 未设 `TLSConfig` 时会自动
注册 `TLSNextProto["h2"]`，`conn.serve` 见到 `NegotiatedProtocol == "h2"` 就交给
标准库的 h2 栈（`shouldConfigureHTTP2ForServe()` → `setupHTTP2_Serve()`）。

验证：`openssl s_client -alpn h2` 应回 `ALPN protocol: h2`；修复前是
`tlsv1 alert no application protocol:SSL alert number 120`。

### 1.2 `-c` 指向的不是「ECDSA 且 IsCA」的证书时，MITM 会在内存里另生成一套 CA

**现象**：启动日志里出现
`提示：xxx/server.crt 不是 CA 证书，改用在内存里生成的 CA`，
客户端 `openssl s_client` 报 `Verify return code: 21 (unable to verify the first certificate)`。

**根因**：`ensureMitmCA()` 的复用条件是「**ECDSA 且 `leaf.IsCA`**」。而 `server.crt`
的 Basic Constraints 是 `CA:FALSE`，`server.key` 是 RSA 2048 —— 都复用不了。

于是一边是磁盘上的 `ca.crt`（客户端装的是这个），一边是内存里新生成的
`mitm-ca.crt`，**两套指纹完全不同**，客户端永远对不上。

**更正一个常见误判**：并不是「每次重启都换一张新 CA」。只要 `mitm-ca.key` 落盘、
Subject 固定，重启重建出来的 CA 是**逐字节相同**的（实测公钥 md5
`548163ba7d46b70afb7c02534ab1c059` 前后一致），客户端装一次就够。

**正解**：要么给一套真正的 ECDSA + `CA:TRUE` 证书，要么只发 `mitm-ca.crt`；
关键是**别让用户去装一张程序根本不会用的 `ca.crt`**。

### 1.3 MITM 端口别和 `-sp` 撞车

`-sp` 已经在 `0.0.0.0:$sslport` 上监 TLS，MITM 再绑 `127.0.0.1:$sslport` 会撞：

```
bind: address already in use
```

而且下游 `startTlsServer` 的 fatal 会**把整个进程带走**。

**正解**：`mitm_port=$((sslport + 1))`，绑 `127.0.0.1` 不对外暴露。

### 1.4 `-mtls` 的参数是监听地址，不是证书路径

```
-mtls 127.0.0.1:5201        # ✅
-mtls /path/to/cert.crt     # ❌ listen tcp: address ...: missing port in address
```

`-h` 原文：`mitm listen address such as : "127.0.0.1:5201" (empty disables)`。
私钥由另一个参数 `-mk`（默认 `./mitm-ca.key`）指定。

### 1.5 没有 `-mtls` 时，属于 `ProxyDomain` 的 CONNECT 会被送进本机回环

`serveTunnel` 里这段判断是硬编码的：

```go
if _, ok := common.ProxyDomain[strings.TrimSuffix(hostStr, ":443")]; ok {
    proxyConnectLocalhost(resp, request)
    return
}
```

没启用 MITM 时它退化成拨 `localhost:443`：

```
dial tcp [::1]:443: connect: connection refused
提示：xxx 走本机回环但未启用 -mtls，将原样透传（换源不生效）
```

**排查提示**：日志前缀能直接区分分支——`Local Received request ...` 是回环分支，
`Received request ...`（不带 Local）才是正常 CONNECT 隧道。

### 1.6 重签是破坏性的：CA 一换，所有客户端都得重装

正确的判定顺序应该是四档，而不是「能用就用、不行就整套重签」：


| 情况 | 应该怎么做 |
|---|---|
| 证书可用且与 CA 匹配 | 一个字节不动 |
| 只是 server 快到期（30 天内） | **只重签 server**，CA 不变 → 客户端无需重装 |
| CA 也不可用 | 整套挪成备份 → 重签 → 成功才删备份，失败自动还原 |
| 文件在但配对不上 | 视为不可用，同上 |

判定必须检查四件事：文件齐、能解析、私钥与证书配对、签发链对得上。只查前三件
会出现「CA 换了、服务器证书还是旧的」这种**客户端必然报错**的半残状态。
到期检查留 30 天余量（`openssl x509 -checkend 2592000`），避免正好卡在下次
重启的边界上失效。

### 1.7 CA 缺 SubjectKeyIdentifier → 客户端报 error 19 / 20


**现象**：

```
Verify return code: 19 (self-signed certificate in certificate chain)
error 20 at 0 depth lookup: unable to get local issuer certificate
```

**根因**：`crypto/x509` 默认模板只给叶子证书带 AuthorityKeyIdentifier，CA 侧
没有 SubjectKeyIdentifier 时，OpenSSL / 部分客户端找不到签发者。

**解法**：CA 必须显式写入 SKI（RFC 5280 方法一 = 公钥的 SHA-1）：

```go
func skiOf(pub *ecdsa.PublicKey) []byte {
	enc, _ := x509.MarshalPKIXPublicKey(pub)
	h := sha1.Sum(enc)
	return h[:]
}
```

脚本侧对应：写独立的 `[v3_ca]` 段，加 `basicConstraints = critical,CA:TRUE` 和
`subjectKeyIdentifier = hash`。注意 OpenSSL 3.x 上直接引用 `v3_ca` 段可能报
`Error checking x509 extension section v3_ca`，要确保配置里确实定义了这一段。

### 1.8 证书目录 vs MITM CA 目录是两套

业务证书跟随 `-c`/`-k`，而 MITM CA（`mitm-ca.crt`/`mitm-ca.key`）跟着 `-mk` 与
程序工作目录。这两个不是同一个变量——排查「客户端为什么说证书不匹配」时很容易
找错地方。

### 1.9 vfat 不支持 rename 覆盖

证书目录放在 vfat/exFAT 存储上时，重签会报：

```
mv: can't rename '/mnt/usb/.../server.crt.new': File exists
```

这类文件系统没有原子 rename。**正解**：先写临时文件再 `cat >` 覆盖。

### 1.10 csr 与 key 不配对

手工重签时如果用了旧 csr，`openssl` 校验会报 `MISMATCH`。要重新生成 csr：

```sh
openssl req -new -key server.key -out server.csr
```

## 2. DNS 与请求改写

这一节全是**上游代码本身的坑**，和用什么系统劫持 DNS 无关。

### 2.1 `interfacepc.music.163.com` 一定要在表里

`common/common.go` 的 `ProxyDomain` / `HostDomain` 是同一张表：

```
music.163.com
interface.music.163.com
interface3.music.163.com
interfacepc.music.163.com
apm.music.163.com
apm3.music.163.com
```

新版 PC 客户端走 `interfacepc`，漏了它就等于电脑端完全没被接管——播放地址请求
全部直连网易，只能拿到试听片段。注释里也标了 iOS 走 `interface3`。

**注意**：这张表在 `serveTunnel` 里是**硬编码分支**（见 1.5），表里有什么域名，
那些域名的 CONNECT 就一定会被送进回环分支，不存在「透传」这个选项。

### 2.2 程序解析网易地址不能问本机 dnsmasq

转发模式（`-m 1`）下，宿主把网易域名劫持到本机。如果程序用**系统解析器**去解析
这些域名（在路由器上就是问本机 dnsmasq），拿到的 `ProxyDomain` 全都会被改写成
本机 IP，于是「请求网易 API」变成「请求自己」，服务端授权信息拿不到，
**任何 VIP 歌都解锁不了**。

实测报错：`timeout awaiting response headers`。

**正解**：`resolveIps()` 优先走 `queryPublicDns(223.5.5.5)`，仅在失败或返回非公网地址时
才回退系统解析器。

### 2.3 `-m 1` 会自己改 `/etc/hosts`，和系统的劫持规则互相覆盖

上游 `host.InitHosts()` 在 `mode == 1` 时会往 `/etc/hosts` 里写 8 个域名 → `127.0.0.1`
（并备份原文件、退出时还原）。这和平台侧自己的劫持机制会**互相覆盖**，谁后写谁赢，
排查时完全看不出是谁在起作用。

**正解**：二选一。要么永远不让上游看到 `-m 1`（用宿主自己的劫持机制，比如 dnsmasq 的
`address=` 规则），要么彻底把劫持权交给上游。别两边一起管。

### 2.4 停止服务时必须清理劫持规则，否则表现像断网

服务停了但域名还指向本机 → 客户端连不上 → 用户以为是 WiFi 断了。

更隐蔽的一种：`/etc/hosts` 里残留 `music.163.com → 127.0.0.1` 时，程序解析自己
会 panic，日志里打满 `dial tcp [::1]:443: connect: connection refused`。

### 2.5 改配置后用重载，不要重启整个 DHCP 服务

重启 DHCP 服务会顺带把租约一起重置，整片网络断一下。改完 hosts / 劫持规则需要的是
重载（dnsmasq 上就是 SIGHUP），不是重启。


## 3. 音源与换源逻辑

### 3.1 并发写 map → `fatal error`，`recover` 抓不住

**现象**：改多音源后每次播放必崩：

```
fatal error: concurrent map read and map write
```

**根因**：`provider/provider.go` 的 `buildSearchSong` 里有一行
`key["musicQuality"] = music.Quality`——**写进去的值从来没被读过**，但让
`resolveSong` 里每个音源的 goroutine 并发写同一个 map。

Go 的 `fatal error` **`recover` 抓不住**，进程当场退出。

单音源时不暴露，改多源后每次播放必崩。

**解法**：删掉那行。

### 3.2 超时定时器必须建在循环外

写成 `select { case <-time.After(...) }` 时，**每轮循环都重新计时**——收到一条
结果就把 deadline 往后推。

实测：本意 12 秒的窗口被拖到 **14.9 秒**；改用循环外 `time.NewTimer` 后精确
**12.006 秒**。

### 3.3 全局 5 秒响应头超时会误杀慢接口

`network/network.go` 的 `Transport` 写死 `ResponseHeaderTimeout: 5s`，而 pyncmd
这个第三方 API 要 **4.5~9.4 秒**才回响应头——每次都被截断重试。

日志表现：`http2: timeout awaiting response headers`。

**解法**：给 `ClientRequest` 加 `ResponseHeaderTimeout` 字段单独放宽（改成 20s），
`maxAttempts` 从 3 降到 2。

### 3.4 pyncmd 直连会偶发挂到 17 秒

实测：直连 1.2s / 3.3s / 2.9s 之后会**挂起**，也出现过 12.4s、17.9s
（`pyncmd consumed: 17.907148332s`）。

走本机代理 `127.0.0.1:7890` → **0.71s / 0.52s / 1.11s** 全部 200。

**解法**：启动时探测 `7890/7891/1080/8118/10808/20171`（TCP dial 300ms 超时），
探到就走代理（单次 6s），探不到走直连（单次 20s）。

**注意**：不要用 `HTTP_PROXY` 环境变量——会影响 kuwo/kugou 这些本来就快的音源。
用单独的 `-proxy` 参数：留空=自动，`none`=强制直连。

### 3.5 `ForceAttemptHTTP2` 千万别关

关掉之后每次请求报：

```
net/http: HTTP/1.x transport connection broken: malformed HTTP response "\x00\x00\x12\x04\x00\x00..."
```

`\x00\x00\x12\x04` 是 h2 帧头。代理在隧道里直接发 h2 帧，Go 却按 HTTP/1.x
解析明文响应——必然失败。

> 这条和 `troubleshooting.md` 里的 ALPN 坑是同一个家族的问题：**HTTP/2 在隧道里
> 很容易被当成 HTTP/1.1 处理**。

### 3.6 `ClientRequest` 的两个字段要合并成一次 `Transport.Clone()`

加 `HTTPProxy` 和 `ResponseHeaderTimeout` 时，如果分成两次写各自的 `Transport.Clone()`，
后写会覆盖前面的**整个** Transport 配置。要合并成一次赋值。

### 3.7 音源分隔符必须是冒号

上游用 `strings.Split(*Source, ":")` 拆分音源。写竖线或逗号会被当成一个整体名字：

```
providers=["pyncmd|kuwo"]   # 一个源都没注册上
```

而且老配置里存的是竖线，启动脚本要**先归一（`tr` 换成冒号）再判断**，顺序反了就会
误以为没写 pyncmd。

### 3.8 匹配阈值 0.75 是刻意的，不是 bug

`CalScore` = `歌名分*0.55 + 歌手分*0.35 + 首位加分*0.1`，另加歌名必须 ≥ 0.7。

实测《晴天》：

- 搜 `晴天` → 第 1 条就是周杰伦原唱
- 搜 `晴天 周杰伦`（**程序实际用的**）→ 全是翻唱：`(KTV版伴奏)`、`(原唱:周杰伦)激情吃力版`、
  `（吉他弹唱）Cover 周杰伦`

所以「有原唱却播翻唱」是**酷我搜索行为**，不是解析 bug。翻唱得分约 0.64，
被 0.75 正确拦下。

**取舍是有意的**：宁可放不出来，也不要放错歌。

### 3.9 音质被锁死在 128k

上游 `unifiedMusicQuality` 只认字符串 `br`，而网易客户端发的是**数字**——类型断言
失败后 `MusicQuality` 一直停在默认的 Lossless。

后果是反的：**手机要 128k，也照样回一份 37 MB 的 flac**。

另一个 bug：上游写成 `if level {} else if br {}`，这两个字段**恒同时出现**，
所以 br 分支永远进不去。

**解法**：两者独立判断、br 优先。

### 3.10 普通 `/api/` 请求的 query 从未被解析

上游只处理 eapi 和 linux/forward，普通 `/api/` GET 的 query 从未解析。
注意 `netease.Path` 来自 `request.URL.Path`（**不含 query**），
必须从 `request.URL.RawQuery` 取。

### 3.11 酷我音质降级：请求 320k 拿回 128k

原来无条件按 `format:mp3 & bitrate:320` 请求，但有些歌最高只有 192k 或 flac，
结果拿到 M500（128k）。

**解法**：保留搜索结果里的 `MINFO`，只请求**真实存在的最高档位**。

### 3.12 原唱被前 5 条截断

酷我 mobi 接口每页条数少、字段不全。改成 PC 接口（固定 30 条、字段更全），
扫描上限从 `min(len/2+1, 5)` 放宽到 `kuwoMaxScan = 8`。

PC 接口返回的是**单引号伪 JSON**，得手写解析（`pcparse.go`），按 `rid` 去重。

### 3.13 QQ 音源默认别开

QQ 的取址接口对**未开通绿钻的账号一律返回 `104003` / purl 为空**。硬塞进音源列表
只会每次多走一次无效请求。

另外曾经做过「在设置页编辑 QQ cookie」的功能，后来整体 revert 了——cookie 方案在
未登录账号上不解决问题，维护成本大于收益。

---

## 4. 速查表

| 类别 | 坑 | 现象 | 正解 |
|---|---|---|---|
| TLS | ALPN 只声明 `http/1.1` | 客户端每 5 秒重连，没有 `Transport:` | `NextProtos: []string{"h2", "http/1.1"}` |
| TLS | `-c` 不是 ECDSA+CA:TRUE | 客户端装的 `ca.crt` 永远对不上 | 给真 CA，或只发 `mitm-ca.crt` |
| TLS | MITM 撞 `-sp` | `bind: address already in use`，整个进程被带走 | `sslport + 1` |
| TLS | `-mtls` 传证书路径 | `missing port in address` | 传监听地址 `127.0.0.1:5201` |
| TLS | 没开 MITM | `dial tcp [::1]:443: connection refused` | 看日志前缀有没有 `Local` |
| TLS | CA 缺 SKI | 客户端 error 19 / 20 | 显式写 `subjectKeyIdentifier` |
| TLS | 重签没备份 | 所有客户端要重装 | 先备份再动手 |
| DNS | 表里漏 `interfacepc` | PC 端完全不生效 | 补进 `ProxyDomain` |
| DNS | 程序问本机 dnsmasq | `timeout awaiting response headers` | 用公共 DNS 解析 |
| DNS | `-m 1` 自己改 hosts | 和系统劫持互相覆盖 | 只用一种机制 |
| DNS | 停服不清理 | 表现像断网 | 停止时清规则 |
| 证书 | iOS 少了信任开关 | `unknown certificate` | 设置里手动打开 |
| 证书 | Linux 不是 `.crt` | `update-ca-certificates` 不认 | 后缀必须 `.crt` |
| 证书 | vfat 上 rename | `File exists` | 写 `/tmp` 再 `cat >` |
| 音源 | 并发写 map | `fatal error: concurrent map read and map write` | 删掉多余的那行写入 |
| 音源 | 循环内 `time.After` | 超时被无限推后（12s → 14.9s） | 循环外 `NewTimer` |
| 音源 | 全局 5s 响应头超时 | `timeout awaiting response headers` | 给慢接口单独放宽 |
| 音源 | pyncmd 直连 | 偶发挂到 17s | 走本机代理 |
| 音源 | 关 `ForceAttemptHTTP2` | `malformed HTTP response "\x00\x00\x12\x04..."` | 别关 |
| 音源 | 两次 `Transport.Clone()` | 后写的覆盖前面的 | 合并成一次赋值 |
| 音源 | 竖线分隔音源 | 一个源都没注册上 | 用冒号 |
| 音源 | 阈值 0.75 | 翻唱被拒 | 刻意设计，不是 bug |
| 音源 | `br` 是数字不是字符串 | 128k 请求回 37MB flac | 两种类型都认 |
| 音源 | `else if` 判断 level/br | br 分支永远进不去 | 独立判断、br 优先 |
| 音源 | 普通 `/api/` 的 query | 参数恒空 | 从 `URL.RawQuery` 取 |
| 音源 | 酷我档位 | 要 320k 拿回 128k | 用 `MINFO` 里真实存在的档位 |
| 音源 | QQ 音源默认开 | 每次多一次 `104003` | 默认关闭 |
| 进程 | `-c` 证书缺失 | 日志戛然而止、进程消失 | 不 panic + `-insecure` 降级 |
| 进程 | `flag.Bool` 写 `=1` | `flag.Parse` 报错，进程退出 | 只能「传=开」 |
| 进程 | `flag.Parse` 失败 | `os.Exit(2)`，看不到原因 | `flag.ContinueOnError` |
| 进程 | `pidof` 按名字找 | 名字超 15 字节就找不到 | 遍历 `/proc` 匹配命令行 |

---

## 5. 仍未解决的

| 问题 | 状态 |
|---|---|
| `handshakeWithRetry` 重试 1/2、2/2 连续两次 `bad record MAC` | 存在。同一条连接上连续两次同样错误，说明该次不是独立抖动。客户端会自动重连，暂接受 |
| iOS 走 `xeapi` 加密接口 | 未做。只认老的明文 eapi 路径，iOS 新版可能整条链路拦不到 |
| 安卓证书安装 | 没有真机记录，只有页面帮助文本 |
| RSA vs ECDSA 签名耗时 | 只有源码注释（「RSA-2048 几百毫秒 / P-256 几毫秒」），没有毫秒级实测 |
| 新版客户端是否还有别的加密路径 | 只在 PC 版验证过 `interfacepc`；移动端、TV 端没测 |

---

## 6. 一条贯穿始终的教训

这些坑里有一半的共同点是：**失败是静默的**。

- ALPN 无交集 → `Handshake()` 不返回 error，日志里只有一串 CONNECT
- 证书不是 CA → 悄悄换成内存 CA，用户装的那张从此永远用不上
- `flag.Parse` 失败 → 进程消失，外部只看得到「已退出」
- 超时定时器建在循环里 → 12 秒的窗口变成 15 秒，没有任何报错
- 并发写 map → `fatal error`，`recover` 根本抓不住

所以这个项目的改动思路基本是同一个：**能报错就别静默**。
`ContinueOnError`、MITM 启动时的警告、证书四档自检、代理探测结果写进日志——
都是为了把「看起来正常但就是不换源」变成一句能 grep 到的话。
