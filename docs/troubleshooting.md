# 问题复盘：新版网易云 PC 端「连着但不换源」是怎么修好的

这份笔记记录 2026-11-10 / 11 排查「网易云 PC 版放歌一直是试听、灰歌不恢复」的完整过程：
踩了哪些坑、哪些观察是假象、最后两个根因分别是什么。留着给下次遇到同类问题的人（也包括
未来的自己）少走弯路。

结论先行：

> **换源不生效有两个根因，必须同时修：**
> 1. MITM 只协商 `http/1.1`，而新版客户端 ClientHello 里**只有 `h2`** → ALPN 无交集 →
>    Go 发 `no_application_protocol` 警报 → 客户端一个字节的请求都没发。
> 2. 修了 ALPN 之后还有一道：客户端**不信任** `-mtls` 现自签的 CA →
>    握手报 `tls: unknown certificate` → 同样不发请求。

---

## 0. 现场环境

| 项 | 值 |
|---|---|
| 路由器 | D-Team Newifi D2（MT7621 / ramips），ImmortalWrt 6.18.55 |
| 解锁程序 | 本仓库 `UnblockNeteaseMusic`，跑在 `-p 5210 -sp 5211 -mtls 127.0.0.1:5221` |
| 分流 | ShellCrash（Mihomo Meta，`redir_mod=Mix`），nftables 实现 |
| 劫持 | `inet unm` 表，`unm_prerouting` `ip daddr @unm4 tcp dport 443 redirect to :5211` |
| 客户端 | `open-orpheus`（网易云音乐 Linux 版，Electron/Chromium 内核） |
| 客户端代理 | 系统代理 → `http://192.168.1.1:5210`（本程序的 HTTP 口） |

链路：客户端 → `5210` CONNECT → 目标是网易域名 → `serveTunnel` 判进
`proxyConnectLocalhost` → 配了 `-mtls` 就把隧道交给 MITM → 解密 → `serveProxy` 改写 → 换源。

---

## 1. 症状

- 客户端能连上，歌单、封面、评论都正常，**唯独该换源的歌放不了**（试听或直接失败）。
- 服务端日志里客户端业务流**只有**：

  ```
  Local Received request CONNECT interfacepc.music.163.com:443 192.168.1.225:33754
  Local Received request CONNECT interfacepc.music.163.com:443 192.168.1.225:33766
  ...  每 5 秒一条，永远重复
  ```

  其余全是 `Direct:http://clientlog.music.163.com/api/feedback/weblog/sys(POST)` 这种噪音。
- **没有任何 `Transport:` / `Passthrough:` / eapi / 音源相关日志**。

「每 5 秒重连一次」是关键指纹：客户端重试池的默认节奏。它说明客户端认为
「连上了，但没有可用的连接」。

---

## 2. 走过的弯路（这些都是假象或半对的结论）

### 2.1 `curl --resolve` 测出来的「生效」是假的

最早用

```sh
curl -sk --resolve music.163.com:5211:192.168.1.1 "https://music.163.com/api/song/enhance/player/url?..."
```

拿到带 `url` 的响应，于是判定「插件正常」。**这个测法没有走真实链路**：

- 真实路径是内核 nft 把「发往 unm4 集合里 IP 的 443」`redirect` 到 `:5211`，
  客户端看到的目标始终是 `music.163.com:443`。
- `--resolve` 直接把 Host 写成 `music.163.com:5211`，程序看到带端口的 Host，
  会当成普通代理请求去连上游，不走劫持分支。

**教训：验证劫持必须用真实客户端路径，或者用 `curl -x <程序端口>` 走 CONNECT 隧道。**
后者才是客户端走的那条路。

### 2.2 「WAN 出口 443 全断」——BusyBox 工具误导

一度以为路由器连不上网易的 443，依据是 `nc -w3 <ip> 443` 全部 `rc=1`，连 `1.1.1.1:443`
也一样。实际是：

- 路由器上的 `nc` 是 BusyBox 版，**不支持 `-w`**，参数被当成别的含义；
- 加 `</dev/null` 后即使连接成功，BusyBox nc 也会返回非零。

`wget` 的 `rc=8` 同理——那是「服务器返回了错误响应」，**不是连不上**。

**教训：判断连通性别只看退出码。** 用 `wget -S -O /dev/null` 看有没有 `HTTP/` 响应行，
或者干脆抓包看有没有 SYN-ACK。

### 2.3 以为是 AAAA / IPv6 的锅

日志里确实出现过

```
dial tcp [2408:8770:0:44:8000:0:d00:24]:5211: i/o timeout
```

于是关了 AAAA（`dhcp.@dnsmasq[0].filter_aaaa=1`）、删了 `unm6` 集合。
IPv6 报错确实消失了，**但换源问题一点没解决**——它只是 cron 里 `sync_set.sh`
同步域名时的一个附带故障。

**教训：把「同时存在的噪音」和「根因」分开。** 修掉噪音≠修好问题，
别因为日志干净了就以为修好了。

### 2.4 `-mtls` 的参数是监听地址，不是证书路径

第一次尝试只在配置里写了 `-mtls $CERT_DIR/mitm-ca.crt`，日志报

```
MITM 监听 ... 失败（listen tcp: address ...: missing port in address）
```

看 `-h` 才发现原文是 `mitm listen address such as : "127.0.0.1:5201"`。
正确写法：

```sh
-mtls 127.0.0.1:5221 -mk $CERT_DIR/mitm-ca.key
```

### 2.5 改远程脚本时把它改坏了两次

- 路由器上没有 `diff`，sed 的引号嵌套又极易出错，一次把 awk 脚本改成了
  `awk x27!/^#/{printf "%s, ",}x27`（`$1` 被 shell 先吃掉了）。
- 另一次用 awk 往 init 脚本里插行，插成了 `[ -s ... ] [ -s ... ] && ...` 的重复片段。

**教训：远程改脚本一律「下载原件 → 本机用脚本打补丁 → `sh -n` 校验 → scp 回去」。**
本机 `python3` 不可用时用 node。

### 2.6 ShellCrash 的 `ip_filter` / `mac` 文件不支持注释

曾经想用注释写清楚往 `configs/ip_filter` 里加了什么，结果 ShellCrash 把 `#` 原样拼进
`ip saddr { #, ... }`，nft 直接 `syntax error`，**代理起不来**。

**教训：这两个文件只写裸值，一个字符别多。**

还有一点：`ip_filter` 是 `ip saddr`（**源地址**），想按目标地址放行要另加
`ip daddr @set return`，不能直接往里写目标 IP 就完事。

---

## 3. 定位过程：怎么一步步逼出根因

### 3.1 先证明「程序本身没坏」

```sh
curl -x http://192.168.1.1:5210 "https://interfacepc.music.163.com/eapi/song/enhance/player/url" -X POST ...
```

日志立刻出现

```
Transport:https://220.197.30.67/eapi/song/enhance/player/url(interfacepc.music.163.com)(POST)
```

**这条 `Transport:` 是分水岭**：它证明 MITM → `serveProxy` → 换源这条链路是通的。
既然 curl 能通、客户端不通，问题就被限定在「客户端到 MITM 之间」。

### 3.2 抓包看隧道里到底有没有请求

```sh
tcpdump -i br-lan -n -s 0 -A "host 192.168.1.225 and port 5210"
```

20 秒 2577 个包，17 次 `CONNECT interfacepc.music.163.com:443`，
**隧道里一个 h2/h1 请求字节都没有**。同时把 ClientHello 里的 ALPN 抠出来：

```
94 h2
```

只有 `h2`，**完全没有 `http/1.1`**。到这里答案就基本揭晓了。

### 3.3 用 openssl 直接复现

```sh
openssl s_client -proxy 192.168.1.1:5210 -connect interfacepc.music.163.com:443 \
  -servername interfacepc.music.163.com -CAfile mitm-ca.crt -alpn "h2"
```

- 改之前：`tlsv1 alert no application protocol:SSL alert number 120`、`No ALPN negotiated`
- `-alpn "http/1.1"`：`ALPN protocol: http/1.1`，正常

---

## 4. 根因一：MITM 只协商 `http/1.1`

`proxy/mitm.go` 的 `handshakeWithRetry` 原来是：

```go
cfg := &tls.Config{
	Certificates: []tls.Certificate{*cert},
	NextProtos:   []string{"http/1.1"},
}
```

客户端只提供 `h2` → 两边没有交集 → Go 的 `tls.Server` 发
`no_application_protocol(120)` 警报后断开。

**为什么日志里看不到失败**：实测 ALPN 不匹配时 `tls.Server.Handshake()` 不返回 error，
Go 只发 `no_application_protocol` 警报就关掉连接。所以 `handshakeWithRetry` 认为握手成功，
静默往下走，`serveMitmHTTP` 等一个永远不来的请求，客户端 5 秒后重连。
**这是最难查的一层：所有「握手失败」关键字都匹配不上。**

### 修法：同时声明 `h2`

```go
NextProtos: []string{"h2", "http/1.1"},
```

不用引入 `golang.org/x/net/http2`。理由在标准库里：

- `net/http/server.go:3195-3202` `shouldConfigureHTTP2ForServe()`：`srv.TLSConfig == nil`
  时返回 true；
- `Serve()` 里据此调用 `onceSetNextProtoDefaults()`，注册 `srv.TLSNextProto["h2"]`；
- `net/http/server.go:1945-1954` `conn.serve()`：若 `c.rwc.(*tls.Conn)` 且
  `NegotiatedProtocol` 通过 `validNextProto`（`:1816`，只排除 `""`/`http/1.1`/`http/1.0`），
  就调用 `c.server.TLSNextProto[proto]` 交给标准库 h2 协议栈。

而 `serveMitmHTTP` 里的 `http.Server{Handler: &HttpHandler{}}` **没有设 `TLSConfig`**，
所以上面这条自动路径是生效的，握手仍然全部由 `handshakeWithRetry` 完成。

---

## 5. 根因二：客户端不信任现自签的 MITM CA

修完 ALPN，`openssl -alpn "h2"` 已经能协商出 `h2` 且 `Verify return code: 0 (ok)`
（因为指定了 `-CAfile`），但客户端仍然重连，只是从「每 5 秒」变成「握手后立刻断」。

关键对比：

```
本机信任库里的：C=CN, CN=UnblockNeteaseMusic Root CA, O=UnblockNeteaseMusic
  sha256 D8:2A:56:1A:D7:97:3C:26:71:98:3D:53:F4:B2:10:FF:CC:30:3C:FC:1D:F6:EE:5B:0A:8A:71:40:E2:38:BC:C2

路由器 -mtls 生成的：O=UnblockNeteaseMusic, CN=UnblockNeteaseMusic CA
  sha256 91:EA:B7:88:EF:81:98:D7:AC:BE:90:0A:C5:F4:D7:C2:8E:62:C7:D7:66:B1:87:73:DB:A2:43:2D:8D:29:59:C8
```

**两张 CA 不是同一张。** 客户端只信任前者（旧 CA），而 `-mtls` 现签的是后者。

`curl` 不加 `-k` 时 `exit=60`（证书校验失败），与客户端行为一致。

### 为什么不能「用旧 CA 继续签」

`ensureMitmCA()` 的逻辑（`proxy/mitm.go:259`，`IsCA` 判断在 `:266`）：

1. `-c`/`-k` 若**是 ECDSA 且 `IsCA`**，复用它；
2. 否则调 `genSelfSignedCA()` 现生成 ECDSA P-256 CA，私钥落盘到 `-mk`。

旧 CA 的证书还在（`/usr/share/unblockneteasemusic-go/ca.crt`，指纹同上），
但**私钥 `ca.key` 已经不在发布包里**——release zip 只有
`UnblockNeteaseMusic`、`ca.crt`、`server.crt`、`server.key`、`createCertificate.sh`。
`server.crt` 是 RSA 且 `IsCA=false`，也当不了签发者。

所以只能二选一：**把新 CA 装进客户端信任库**，或者重签一套证书并同步三端。
本次选前者。

### 修法

```sh
scp root@192.168.1.1:/usr/share/unblockneteasemusic-go/mitm-ca.crt .
sudo cp mitm-ca.crt /usr/local/share/ca-certificates/unblockneteasemusic-mitm-ca.crt
sudo update-ca-certificates
```

> `update-ca-certificates` 末尾若报
> `dpkg-trigger: 无法打开或创建触发器锁文件 ...: 只读文件系统`
> 以及 `jks-keystore exited with code 2`，那是 Java 信任库的钩子，**与 TLS 信任无关**，
> 看到 `验证成功` 即可。装完 `/etc/ssl/certs/` 下会出现对应的 `.pem` 符号链接。

Electron 客户端要**重启**才会重新读信任库。

**部署机上的长期做法**：把 `mitm-ca.crt` 装进每台客户端的系统信任库 / 描述文件，
或者用 `createCertificate.sh` 那套自己签发、自己分发（记得把 `ca.key` 留在手里，
别再一次打包丢了）。

---

## 6. 修复后的验证链

按顺序打，每一环都有明确证据：

| # | 验什么 | 命令 / 现象 |
|---|---|---|
| 1 | ALPN 协商 | `openssl s_client ... -alpn "h2"` → `ALPN protocol: h2` |
| 2 | 证书信任 | `curl -x :5210` **不加 `-k`** → `HTTP 200` |
| 3 | 进换源分支 | 日志 `Transport:https://220.197.30.67/eapi/...` |
| 4 | 重连循环消失 | 25 秒内新增 `CONNECT interfacepc` = 0；`ss` 到 `5210` 的 ESTAB 从 0 升到 48 |
| 5 | 换源成功 | `resolved by pyncmd: {"Id":"1965671755",...,"MatchScore":1,"Source":"pyncmd"}` |
| 6 | 客户端拉流 | 紧跟 `CONNECT m701.music.126.net:443` |
| 7 | 真的在播 | 客户端日志 `"native播放state",1`；`pactl list short sink-inputs` 有活动流 |

---

## 7. 工具箱：以后怎么快速定位

### 7.1 日志里哪些前缀对应哪条分支

| 日志前缀 | 走的哪条路 | 含义 |
|---|---|---|
| `Received request CONNECT ...` | `serveTunnel` → `proxyConnect` | 普通隧道，直接打洞 |
| `Local Received request CONNECT ...` | `serveTunnel` → `proxyConnectLocalhost` | 目标是网易域名，进本机回环分支 |
| `提示：%s 走本机回环但未启用 -mtls` | 同上，且 `MITMAddr` 为空 | **换源一定不生效** |
| `starting MITM Server ...` | `startMitmServer` | 启动时就绪 |
| `警告：MITM 证书不可用` | `startMitmServer` | 证书坏了，隧道只会透传 |
| `mitm 握手 %s 失败（已重试 ...）` | `handshakeWithRetry` | TLS 层失败，**注意 ALPN 失败不会出现在这里** |
| `Transport:https://<ip>/<path>(<host>)(POST)` | `serveProxy` | **真正进了换源逻辑** |
| `Direct:http://...` | `serveProxy` | 直连/透传，没改 |
| `resolved by <source>: {...}` | provider | 换源成功 |
| `reject low score ... < 0.75` | provider | 匹配度过低被拒，属正常拦截 |

**判断口诀**：只有 `Local Received` + 没有 `Transport:` ⇒ 问题在 MITM 之前
（ALPN / 证书 / 客户端根本不发请求）；有 `Transport:` 但没 `resolved by` ⇒ 问题在音源。

### 7.2 一条命令判断「隧道里有没有请求」

```sh
tcpdump -i br-lan -n -s 0 -A "host <客户端IP> and port 5210" 2>/dev/null \
  | grep -aoE "(GET|POST|CONNECT) [^ ]*|h2|http/1\.[01]" | sort | uniq -c
```

ALPN 只出现 `h2` 是**正常**的（修好之后也这样），别被误导；
要看的是有没有 `POST /eapi/...` 这类应用层请求。

先 `apk add tcpdump`，完事 `apk del tcpdump`。

### 7.3 复现客户端行为的最小命令

```sh
# 走客户端同一条 CONNECT 路径（必须加 -x，不加 --resolve）
curl -sx http://192.168.1.1:5210 --max-time 25 \
  "https://interfacepc.music.163.com/eapi/song/enhance/player/url" -X POST \
  -H "User-Agent: NeteaseMusicDesktop/9.1.0" -o /dev/null -w "%{http_code} %{http_version}\n"

# 只看 TLS / ALPN / 证书链
openssl s_client -proxy 192.168.1.1:5210 -connect interfacepc.music.163.com:443 \
  -servername interfacepc.music.163.com -CAfile mitm-ca.crt -alpn "h2" </dev/null 2>&1 \
  | grep -E "ALPN|Verify return|alert"
```

---

## 8. 坑位速查表

| 坑 | 表现 | 正解 |
|---|---|---|
| ALPN 只声明 `http/1.1` | 每 5 秒重连，日志只有 `Local Received CONNECT`，**无任何握手错误** | `NextProtos: []string{"h2", "http/1.1"}` |
| MITM CA 没装进客户端 | 握手后立刻断，`curl` 不加 `-k` 报 `exit=60` | 把 `mitm-ca.crt` 装进系统信任库，重启客户端 |
| 发布包丢 `ca.key` | 无法用旧 CA 重签 | 自签 CA 后把 `ca.key` 留在自己的仓库外 |
| `-mtls` 传证书路径 | `listen tcp: address ...: missing port` | 传**监听地址** `127.0.0.1:5221`，私钥用 `-mk` |
| `curl --resolve` 验证劫持 | 假成功 | 用 `-x <端口>` 走 CONNECT |
| BusyBox `nc -w` / `wget rc=8` | 误判「网断了」 | 看 `wget -S` 的响应行或抓包 |
| 只修 IPv6 `unm6` | 日志干净了但问题还在 | IPv6 是噪音，别当根因 |
| `configs/ip_filter` 写注释 | nft `syntax error`，代理起不来 | 只写裸值 |
| `ip_filter` 当成目标地址过滤 | 写了没效果 | 它是 `ip saddr`；目标地址要另加 `ip daddr @set return` |
| 远程 sed/awk 改脚本 | 文件被改坏 | 下载 → 本机打补丁 → `sh -n` → scp |
| `update-ca-certificates` 报 jks 错 | 看起来像失败 | 与 TLS 无关，看「验证成功」 |
| 修完不重启客户端 | 改了信任库但仍旧失败 | Electron 只在启动时读信任库 |

---

## 9. 顺带记下的其它行为

- `serveTunnel`（`proxy/proxy.go:148`，判定在 `:153`）对 `common.ProxyDomain` 里的域名**无条件**
  走 `proxyConnectLocalhost`，跟它解析到哪个 IP 无关。所以 hosts 指向不指向本机都会
  进回环分支——**这决定了 `-mtls` 在这类部署里是必需的，不是可选优化**。
- 未启用 `-mtls` 时，回环分支会去拨 `localhost:443`（或 Host 里的端口），
  MT7621 上表现为 `dial tcp [::1]:443: connect: connection refused`。
- `genSelfSignedCA()` 用 ECDSA P-256 而不是 RSA：单核 MIPS 上 RSA-2048 每连接签名
  要几百毫秒，一首歌十几次握手会明显拖慢。
- `skiOf()` 必须显式写 SubjectKeyIdentifier，否则 OpenSSL 1.1.1 按 AKI 找不到签发者，
  报 `error 20 unable to get local issuer certificate`，客户端看起来像断网。
- `certForHost()` 按 host 缓存证书，同一域名只签一次。
- `handshakeWithRetry` 在 MT7621 上兜 `bad record MAC`，同一条 TCP 重握最多 2 次
  （`mitmMaxRetry = 2`）。它**只处理 `Handshake()` 返回 error 的情况**，
  ALPN 不匹配不在其中。

---

## 10. 仍未收尾的小问题

- 日志里偶见 `mitm 握手 music.163.com 失败（已重试 2 次）：remote error: tls: unknown certificate`。
  业务主通道是 `interfacepc.music.163.com`，不受影响；但如果之后遇到某些页面异常，
  可以顺着这条查（可能是客户端对该域名用了不同的证书校验策略）。
- 本次修复后的二进制是本地交叉编译直接上机的（没走 sstrip / upx），
  CI 出包后固件里随包的内核才是标准产物。
