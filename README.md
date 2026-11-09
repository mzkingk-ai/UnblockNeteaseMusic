# UnblockNeteaseMusic

解锁网易云音乐客户端变灰歌曲（Golang 单文件，无外部依赖）

> 这是 [`cnsilvan/UnblockNeteaseMusic`](https://github.com/cnsilvan/UnblockNeteaseMusic)
> 的 fork，主要用于在 Padavan / MT7621 路由器上跑，也可在 NAS、软路由、PC 上跑。
> 配套 LuCI 见 [luci-app-unblockneteasemusic](https://github.com/cnsilvan/luci-app-unblockneteasemusic)。

## 它是怎么工作的

程序在 `80`（HTTP）和 `443`（HTTPS）上冒充网易云音乐的接口域名，本地生成一份
`music.163.com` 的证书，把客户端的请求解出来，遇到没有版权的歌就用酷我、酷狗、咪咕、
pyncmd 等音源搜一个能播的版本，把播放地址换掉再返回。

所以客户端那边只有两件事要做：**把域名指到运行程序的机器**、**信任它签的证书**。

## 下载

到 [Releases](https://github.com/mzkingk-ai/UnblockNeteaseMusic/releases) 挑对应平台的压缩包，
每个包里是可执行文件 + `ca.crt` + `server.crt` + `server.key` + `createCertificate.sh`。

| 设备 | 文件 |
|---|---|
| **MT7621 / Padavan 等 MIPS 小端软浮点（推荐）** | `UnblockNeteaseMusic-linux-mipsle-softfloat-mt7621.zip` |
| 其它 MIPS 小端软浮点设备 | `UnblockNeteaseMusic-linux-mipsle-softfloat.zip` |
| 其它 MIPS 小端 | `UnblockNeteaseMusic-linux-mipsle.zip` |
| MIPS 大端 | `UnblockNeteaseMusic-linux-mips.zip` / `-softfloat` |
| ARM 路由器（armv7 及以上） | `UnblockNeteaseMusic-linux-arm7.zip`，更老的用 `arm6` / `arm5` |
| 树莓派 / 群晖 / ARM64 服务器 | `UnblockNeteaseMusic-linux-arm64.zip` |
| x86_64 PC / 软路由 / NAS | `UnblockNeteaseMusic-linux-amd64.zip` |
| Windows / macOS | `UnblockNeteaseMusic-windows-amd64.zip` / `darwin-arm64`、`darwin-amd64` |

MT7621 专用的那个包和通用 `mipsle-softfloat` 是同一份代码、同样跑得起来，
差别只是名字里点明了设备、去掉了 Go build id（对体积几乎没影响，差 200 字节左右），
以及构建时多一道「必须是 MIPS 小端 ELF」和「upx 压完能自解压」的检查。
拿不准路由器是哪个就先用带 `mt7621` 的；跑不起来再换 `mipsle`，
报 `exec format error` 说明是大端，换 `mips`。

```sh
unzip UnblockNeteaseMusic-linux-mipsle-softfloat-mt7621.zip -d /opt/unm
chmod +x /opt/unm/UnblockNeteaseMusic
```

## 服务端：启动

### 1. 证书（建议做一次）

包里自带一套证书，直接能用。但**自己生成的更安全**，而且包里那张的 CA 私钥没随包发出来，
将来想换域名就得整套重做：

```sh
cd /opt/unm
./createCertificate.sh
```

会覆盖 `ca.crt` / `server.crt` / `server.key`，另外留下一个 `ca.key`（**这是 CA 私钥，
别外传**）。客户端要装的是 `ca.crt`。

### 2. 启动

> `-m 1`（默认）会改写本机 `/etc/hosts`，加上 `80`/`443` 端口，所以要 root。

**最常见：路由器上跑，给局域网所有设备用**

```sh
/opt/unm/UnblockNeteaseMusic -m 2 -o pyncmd:kuwo -b -ba -bu -lv -sef
```

`-m 2` 不改 hosts（域名靠路由器的 dnsmasq 指过来，见下面「客户端」），
适合路由器 / NAS / 软路由这种一台机器伺候全网设备的场景。

**单机：程序装在 PC 上，只让自己这台机器用**

```sh
sudo /opt/unm/UnblockNeteaseMusic -m 1
```

`-m 1` 会把 `/etc/hosts` 里的网易域名指向 `127.0.0.1`，本机客户端不用再配 DNS，
只需导入 `ca.crt`。程序退出（`Ctrl+C`）时会自动还原 hosts。

**常用参数组合**

```sh
# 路由器：换源 + 强制最高音质 + 屏蔽广告/更新提示 + 解锁本地 VIP 和音效
/opt/unm/UnblockNeteaseMusic -m 2 -b -ba -bu -lv -sef

# 只想要基本解锁，日志写文件
/opt/unm/UnblockNeteaseMusic -m 2 -l /tmp/unm.log

# 排错：把每个请求和响应都打出来
/opt/unm/UnblockNeteaseMusic -m 2 -wl -l /tmp/unm.log
```

启动成功会打印版本、配置、各音源初始化结果，最后是 `Http Proxy:` 加本机 IP 列表——
那几行就是你要在客户端 DNS 里填的地址。

### 3. 常驻

**Padavan / OpenWrt（放在 `/etc/storage`，重启不丢）**

把可执行文件和证书放进 `/etc/storage/unm/`，在「自定义脚本 → 在路由器启动后执行」
（OpenWrt 是 `/etc/rc.local`）里加：

```sh
nohup /etc/storage/unm/UnblockNeteaseMusic -m 2 -b -ba -bu -lv -sef \
  -l /tmp/unm.log >/dev/null 2>&1 &
```

注意 Padavan / OpenWrt 的 LuCI 默认占着 `80` 端口。要么把 LuCI 改到别的端口，
要么给程序换成高位端口，再把 80/443 转过去：

```sh
# 程序用 8080/8443 跑，把局域网的 80/443 重定向过去
/opt/unm/UnblockNeteaseMusic -m 2 -p 8080 -sp 8443 -b
iptables -t nat -A PREROUTING -i br0 -p tcp --dport 80 -j REDIRECT --to-ports 8080
iptables -t nat -A PREROUTING -i br0 -p tcp --dport 443 -j REDIRECT --to-ports 8443
```

**systemd（NAS / 服务器）**

```ini
# /etc/systemd/system/unm.service
[Unit]
Description=UnblockNeteaseMusic
After=network-online.target

[Service]
WorkingDirectory=/opt/unm
ExecStart=/opt/unm/UnblockNeteaseMusic -m 2 -b -ba -bu -lv -sef -l /var/log/unm.log
Restart=always
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl enable --now unm
```

### 4. 参数

```sh
./UnblockNeteaseMusic -h
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-m` | `1` | 运行模式。`1` 自动改写本机 hosts（单机用，需 root）；`2` 不改 hosts（路由器/NAS 给全网用） |
| `-a` | `0.0.0.0` | 监听地址，只给本机用可以填 `127.0.0.1` |
| `-p` / `-sp` | `80` / `443` | HTTP / HTTPS 监听端口 |
| `-c` / `-k` | `./server.crt` / `./server.key` | 服务器证书和私钥 |
| `-o` | `pyncmd:kuwo` | 音源，冒号分隔可写多个，按顺序尝试（`pyncmd` `kuwo` `kugou` `migu`） |
| `-b` | 关 | 强制最高音质 |
| `-e` | 关 | 把返回里的播放地址也替换掉（部分老客户端需要） |
| `-ba` / `-bu` | 关 | 屏蔽广告请求 / 屏蔽版本更新提示 |
| `-lv` / `-sef` | 关 | 解锁本地 VIP / 解锁音效 |
| `-sl` | `0` | 搜索页展示其它平台结果的数量（0–3） |
| `-ms` | `0.75` | 匹配阈值 0–1，低于它宁可不解锁，避免匹配到错歌 |
| `-l` | 空 | 日志文件路径，不填只打印到终端 |
| `-wl` | 关 | 打印每个请求的 URL 和响应体，排错用 |
| `-proxy` | 自动探测 | `pyncmd` 音源走的本机代理，如 `http://127.0.0.1:7890`；`none` 强制直连 |
| `-mtls` | 关 | TLS 中间人监听地址，如 `0.0.0.0:5201`。只有客户端把本机当 HTTP/HTTPS 代理、走 `CONNECT` 隧道时才需要 |
| `-mk` | `./mitm-ca.key` | 中间人 CA 私钥，自动生成，重启复用 |
| `-insecure` | 关 | TLS 端口不加载证书、按明文服务（给那些直连 TLS 端口的客户端） |
| `-qc` | `./qq.cookie` | QQ 音乐 cookie 文件 |
| `-v` | — | 只打印版本信息后退出 |

## 客户端：怎么用

两件事：域名指向、信任证书。

### 1. 把域名指到服务端

在跑程序的机器上听 DNS 的设备，只要让它把 `music.163.com` 解析到服务端 IP 就行。
dnsmasq 一条规则覆盖所有子域（`interface3`、`interface9`、`interfacepc`、`apm` 等）：

```sh
# 路由器 / OpenWrt：写进 dnsmasq 配置后重启 dnsmasq
address=/music.163.com/192.168.2.1      # 换成跑 UnblockNeteaseMusic 的那台机器的 IP
```

如果是 OpenWrt 的 LuCI，在「网络 → DHCP/DNS → 自定义配置文件」里加同一行；
Padavan 在「DHCP 服务器 → 自定义配置」里加。

客户端侧的 DNS 指向路由器即可（多数人本来就是这么用的，不用改）。

**单机场景**不用配 DNS：`-m 1` 已经把 `/etc/hosts` 改好了。
Windows / macOS 装在本机也是同样道理，程序会自动改系统 hosts（需要管理员权限）。

### 2. 信任证书

程序用自己签的证书冒充 `music.163.com`，不装 CA 客户端会报证书错误或者干脆没声音。
用包里（或你自己 `createCertificate.sh` 生成的）`ca.crt`：

| 平台 | 做法 |
|---|---|
| **iOS** | 把 `ca.crt` 传到手机（AirDrop、邮件附件、或临时放到 HTTP 服务上用 Safari 打开）→ 设置 → 已下载描述文件 → 安装 → 再进 设置 → 通用 → 关于本机 → 证书信任设置，把 `UnblockNeteaseMusic Root CA` 打开 |
| **Android** | 设置 → 安全 → 加密与凭据 → 安装证书 → CA 证书，选 `ca.crt`。注意 Android 7 以后**应用默认不信任用户装的 CA**，若网易云仍不通，需要 root 后把证书装进系统区（`/system/etc/security/cacerts/`），或参考下面「常见问题」的替代方案 |
| **Windows** | 双击 `ca.crt` → 安装证书 → 本地计算机 → 把证书放入下列存储 → 受信任的根证书颁发机构 |
| **macOS** | 双击 `ca.crt` 加进「钥匙串访问」→ 系统 → 找到 `UnblockNeteaseMusic Root CA` → 显示简介 → 信任 → 始终信任 |
| **Linux** | `sudo cp ca.crt /usr/local/share/ca-certificates/unm.crt && sudo update-ca-certificates` |

装完重启一下网易云音乐，之前灰掉的歌应该能直接播。

### 3. 确认生效

```sh
# 服务端：请求能不能被改写
curl -k "https://music.163.com/api/search/get/web?csrf_token=&s=test&type=1&offset=0&total=true&limit=1"
```

浏览器打开 <https://music.163.com> 不报警告，说明证书已经装对了。
路由器上跑的话，手机连上 Wi-Fi 后访问任一句 `interface3.music.163.com` 的地址，
应该连到路由器而不是网易的服务器。

## 常见问题

**歌还是灰的 / 点了没反应**

按顺序看：① 客户端 DNS 是否真的指到服务端（`nslookup music.163.com`）；
② 证书是否装进「受信任的根证书」而不是「个人」；③ 加大日志 `-wl -l /tmp/unm.log`，
看请求有没有打到程序上、有没有出现 `unblock` 相关的行；④ 换个音源 `-o kuwo`
或 `-o pyncmd:kuwo:kugou` 再试。

**播放变成一小段试听**

说明请求走到了、但没解开。检查是否 `-e` 没开（老客户端需要）、
或者客户端走的是 `CONNECT` 隧道而 `-mtls` 没启用。

**报 `address already in use`**

`80`/`443` 被 LuCI、nginx、AdGuardHome 之类占了，按上面「常驻」里换端口 + iptables 重定向。

**MT7621 上启动就 SIGSEGV**

版本不对：用 release 里的 `linux-mipsle-softfloat`，不要自己用 go1.23+ 编（见下面硬约束）。

**iOS 新版本死活连不上**

新版客户端可能对接口域名做了额外校验。先把 `-wl` 打开，确认它请求的是哪个域名，
再把域名补进 `common/common.go` 里的 `ProxyDomain` / `HostDomain` 表。

## 本 fork 相对上游的改动

| 方面 | 说明 |
|---|---|
| module 路径 | `github.com/mzkingk-ai/UnblockNeteaseMusic`，所有包内引用同步改过，不再跨仓库引用上游路径 |
| TLS 中间人 | `proxy/mitm.go` 用 CA 现场签证书，解出来的 HTTP/1.1 复用同一套改写逻辑 |
| 握手重试 | MT7621 上 `crypto/tls` 偶发 `bad record MAC`，同一条 TCP 上重握最多 2 次（失败率 1/3 → 约 1/27） |
| 音源 | 新增 `pyncmd`；酷我改走 PC 接口解析（`provider/kuwo/pcparse.go`） |
| 路由与解锁链路 | 音质选档、翻唱阈值、证书失败不退出、eapi 容错、provider 初始化顺序等 |
| 多源稳定性 | 修掉并发写 map 崩溃；pyncmd 改为并发竞速，不再被全局 5 秒响应头超时误杀 |
| 自动发布 | 推送到 `master` 后，如果相比上一个 release 有新提交，自动构建 15 个平台产物并发布 |

### 路由器上的硬约束

1. **必须用 go1.22 编译。** go1.23+ 的 runtime 用自旋锁，在 MT7621 + uClibc 上让 futex
   返回 `-89`，产物一启动就 SIGSEGV。
2. **必须 `GOARCH=mipsle`。** MT7621 是 MIPS32r2 小端，`GOARCH=mips` 产出的大端程序上机
   直接 exec format error。
3. 交叉编译参数：`GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0`。

```sh
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
  go1.22 build -trimpath -ldflags "-s -w" -o UnblockNeteaseMusic .
```

## 测试

测试文件**不放在包目录**里，统一收在 [`_testdata/tests/`](_testdata/tests/)。
`_` 开头的目录会被 Go 工具链整个忽略，所以它们既不参与构建，也不进 `go test ./...`，
不会干扰把这个仓库直接拉去编译固件的构建链。

要跑测试：

```sh
./_testdata/tests/run.sh
```

脚本会把测试临时复制回各自的包目录，跑完自动删除。

## 自动发布

[`.github/workflows/auto_release.yml`](.github/workflows/auto_release.yml) 负责构建与发布：

| 触发 | 行为 |
|---|---|
| 推送到 `master` | 若相比上一个 release 有新提交，构建全部平台并发布新版本；没有新提交则直接跳过 |
| 手动运行（Actions 页面 *Run workflow* / `gh workflow run`） | 可选「强制发布」和「手动指定版本号」 |

版本号在「上一个 release 的 tag」上递增 patch（找不到 release 时依次回退到本地 tag、
上游 `cnsilvan/UnblockNeteaseMusic` 的最后一个 release、文件里的 `FALLBACK_BASE`）。
所以正常情况下第一次运行会产出 `0.2.14`。想用别的号，手动运行并填 `version` 即可。

手动运行：

```sh
gh workflow run auto_release.yml                       # 有新提交才发布
gh workflow run auto_release.yml -f force=true         # 强制发一版
gh workflow run auto_release.yml -f version=1.0.0      # 指定版本号
```

构建统一固定 go1.22（见上面的硬约束）。所有二进制在打包前用 `upx --lzma --best` 压缩
（路由器 flash 小，能省不少空间），macOS 的除外——upx 会破坏 Mach-O 签名。
MT7621 的那个包额外带两道检查：打包前先确认产物是 ELF32 小端、`e_machine` 是 MIPS，
压完再用 `upx -t` 解一遍验壳，任何一道不对就整个 job 失败，避免发出一个上机跑不起来的包。
Release 附带 `SHA256SUMS`，校验：`sha256sum -c SHA256SUMS --ignore-missing`。

## 致谢

- [cnsilvan/UnblockNeteaseMusic](https://github.com/cnsilvan/UnblockNeteaseMusic)：
  本项目的上游，Go 版本的绝大部分工作都是他做的。
- [nondanee/UnblockNeteaseMusic](https://github.com/nondanee/UnblockNeteaseMusic)：
  最早把「用其它平台音源替换播放地址」这个思路做出来的原版。
- [luci-app-unblockneteasemusic](https://github.com/cnsilvan/luci-app-unblockneteasemusic)：
  路由器上的 LuCI 界面。
- 酷我、酷狗、咪咕、pyncmd 等音源接口的探索者，以及上游所有提交过 issue 和 PR 的人。
- 本项目按 GPL-3.0 发布，仅供学习研究，请支持正版音乐。
