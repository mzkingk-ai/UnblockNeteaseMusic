# Padavan / MT7621 固件集成踩坑史

这份文档记录 2026-10-30 ~ 11-02 把 UnblockNeteaseMusic 集成进 Padavan 固件
（`padavan-4.4` fork，设备 Newifi D2 / MT7621）时踩过的坑。

和 [`troubleshooting.md`](troubleshooting.md) 的区别：
那份是**一次具体故障**的复盘（新版 PC 端连着不换源），这份是**整条集成链**上
反复踩到的问题——编译器、字节序、证书、DNS 劫持、Web 页面、音源、固件打包。

按环节分组，每条都附**现象原文**和**为什么**。有些坑看起来离谱（比如
`event_mask` 左移 64 位的未定义行为），但都是真机上撞出来的。

---

## 0. 先看结论

如果你只想记住三件事：

1. **编译器必须是 go1.22**。go1.23+ 的 runtime 在 MT7621 上启动即 SIGSEGV，报错
   长得完全不像编译器版本问题。
2. **证书生成是破坏性操作**。重签 CA 会让所有客户端已装的证书失效，而且用户
   往往看不出是证书问题——只看到「歌放不了」。所以换证书前一定要先备份、能还原。
3. **劫持链路有四处都要对**：DNS 解析到本机 → 隧道进 MITM → MITM 能解密 →
   解密后能被改写。少任何一环，表现都是「连上了但没效果」。

---

## 1. 编译与打包

### 1.1 必须用 go1.22，go1.23+ 启动即 SIGSEGV

**现象**：

```
futexwakeup addr=0x343e32c returned -89
SIGSEGV: segmentation violation
PC=0x77c00 m=0 sigcode=128 addr=0x0
runtime.futexwakeup → runtime/lock_futex.go:161 → runtime.unlock2Wake
  → runtime/lock_spinbit.go:461 → runtime.unlock2 → runtime.mcommoninit
```

**排查过程**（绕了不少弯）：

| 假设 | 做法 | 结果 |
|---|---|---|
| 并发度太高 | `GOMAXPROCS=1` 重编 | 仍 `futexwakeup ... returned -89` ❌ |
| 编译参数有问题 | 去掉 `-buildid=`、改 `GO386=` | 仍崩 ❌ |
| 是我改的代码有问题 | 跑 `--version` | 也崩（栈还没到 `main`）❌ |
| 是不是产物本身有问题 | 用旧产物 `runtime: go1.22.12 linux/mipsle` | 正常 ✅ |
| 换回 go1.22 编 | docker 里 go1.22.12 编出 7,274,662 B | 真机正常 ✅ |

**根因**：go1.23+ 的 runtime 用 `lock_spinbit` 自旋锁，在 MT7621 + uClibc 上
`futex` 返回 `-89`（ENOSYS），`mcommoninit` 阶段就崩。

**解法**：Makefile 加版本守卫：

```make
GO_VER=$$(go version | sed -E 's/.*go([0-9]+)\.([0-9]+).*/\1 \2/'); \
GO_MAJ=$$(echo $$GO_VER | cut -d" " -f1); \
GO_MIN=$$(echo $$GO_VER | cut -d" " -f2); \
if [ "$$GO_MAJ" -gt 1 ] || { [ "$$GO_MAJ" -eq 1 ] && [ "$$GO_MIN" -ge 23 ]; }; then \
    echo "ERROR: 必须用 go1.22 编译（当前 $$(go version)）。"; \
    exit 1; \
fi
```

**教训**：这类「一启动就崩、栈在 runtime 里」的问题，先怀疑**编译器版本**，
而不是自己的代码。特别是 Go 的 runtime 在小端 MIPS + 老 libc 上很容易踩到。

### 1.2 必须 `GOARCH=mipsle`，`GOARCH=mips` 是大端

MT7621 是 MIPS32r2 **小端**（工具链是 `mipsel-*`）。`GOARCH=mips` 产出大端程序，
上机直接 `exec format error`。

```sh
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
go build -trimpath -ldflags "-s -w -buildid= -X .../version.Version=$(UNM_VER)" \
    -o bin/UnblockNeteaseMusic .
```

产物用 `file` 验证：应该是 `ELF 32-bit LSB executable, MIPS, MIPS32 version 1 (SYSV), statically linked`。

### 1.3 UPX：压缩本身有效，但构建时会崩

**两件不同的事，别混在一起**：

**① 压缩后的二进制能不能跑**——能。`upx -9 --lzma` 把 7.34 MB 压到 1.76 MB，
在 NEWIFI3(D2) / 4.4.198 真机上验过 `-v` 和 `:18080` 代理都正常。

> 早先这里记着「mipsel 解压器会让 futex 报 -89，压缩版一样 SIGSEGV」。
> 2026-11-01 实测推翻了：**当时踩坑的是宿主机 go1.26 编的 8.7MB 产物**，
> 不是 UPX 本身。这条历史结论是错的，别再传下去。

**② 完整固件构建时 upx 自己会崩**——会。`make` 子进程里 upx 稳定 SIGSEGV：

- 单独跑同一条命令：8/8 通过
- 在 `make` 子进程里：稳定崩
- core 显示崩在 upx **自身**的 startup 段，RIP/RSP 落在所有已映射区间之外
  —— 是 upx 自己 memcpy 到坏地址，与输入文件无关

**当前选择**：`UNM_UPX_ENABLE := n`，产物保持未压缩。想恢复改回 `y` 即可，
其余逻辑都在。

**还有一个坑**：upx 的路径。宿主的 `~/.local/bin/upx` 在 docker 容器里**不可见**
（没挂载），所以回退路径要指向随源码提交的静态版 `trunk/tools/bin/upx`。

**UPX 与 squashfs 的叠加不是乘法**：UPX 把二进制从 7.34 MB 压到 1.76 MB，
但 trx 只从 12,319,520 降到 12,271,872（**−47 KB**）。因为固件本来就层层压缩
（squashfs/xz），UPX 省下的空间大部分会被 squashfs 吃掉。

### 1.4 `make` 只做清理不编译

Makefile 里第一条规则会变成默认目标。如果第一条是 `coreclean`，裸跑 `make`
就只清理不编译。解决：`.DEFAULT_GOAL := all`。

### 1.5 编译中途被 kill 会留下 core 文件

工具链在包目录留下 `core.NNNN`，每个约 4.8 MB。构建链会反复跑这个包，
不清理会越积越多。用户当时的原话：「这个目录下怎么产生了一堆 core.xxx 的文件」。

```make
coreclean:
	@rm -f $(THISDIR)/core.* $(THISDIR)/bin/*.tmp $(THISDIR)/bin/*.raw
all: coreclean fetch_src build
```

### 1.5.1 `upx && mv` 会把上一轮的旧压缩包打进固件

**现象**：upx 偶发段错误后，脚本里 `upx ... && mv ...` 的后半段不执行，
但**上一轮留下的压缩产物还在原地**——于是被打进固件。

**根因**：并行编译占满内存时 upx 抢不到内存段错误。

**解法**：每轮从 `.raw` 副本重压，最多重试 5 次，并用 `upx -l` 真验证产物：

```sh
for i in 1 2 3 4 5; do
  cp -f $$BIN.raw $$BIN.tmp
  "$$UPX_BIN" -9 --lzma -q $$BIN.tmp
  if ! "$$UPX_BIN" -l $$BIN.tmp | grep -q "NotPacked"; then ok=1; break; fi
  sleep 3
done
```

顺带：原来的判定写成了 `grep -qv "NotPacked"`（多了一个取反的 `v`），
**永远为真**——等于没验证。找不到 upx 时直接 `ERROR: upx not found` 退出。

### 1.5.2 `upx -d` 解未压缩文件返回 0，`||` 回退永不执行

```
upx -d -o /tmp/t2.tmp /tmp/t2
  decompressed:
Unpacked 0 files.
stat: cannot statx '/tmp/t2.tmp': No such file or directory
```

`upx -d` 对未打包的二进制输出 `Unpacked 0 files.` 且 **exit code 是 0**。
原写法 `upx -d ... || cp -f $BIN.raw $BIN.tmp` 的回退分支永远不会触发。

**解法**：无条件先 `cp -f $BIN.raw $BIN.tmp` 再压，不依赖 `-d` 的返回值。

### 1.6 源码：别在解包目录里改代码

早期做法是「下载固定 commit 的 tar.gz + 叠加补丁」。两个问题：

- 补丁越叠越多，每次改代码都要重新生成 diff
- **补丁打崩了只能靠 `.rej` 事后兜底**，顺序稍错就静默编出不含修复的二进制

实际发生过：`patch_test` 因为补丁锚点行号全错（一半对原始 tar、一半对已打补丁文件），
dry-run 必然失败、被静默跳过，**2026-11-01 就是这么发出过一版不含修复的二进制**，
日志里只看得到「Applying patch」一次都没出现。

现在改为 `git clone --depth=1` 直接拉 fork 源码，`UNM_REF` 可钉 commit 复现旧固件。
fetch 失败时沿用本地缓存，不中断整个固件构建（固件构建链很长，不该因为一个
可选组件拉不到就全盘失败）。

### 1.7 `-buildid=` 只能写进 ldflags

`-buildid` 是**链接器**参数，不是 `go build` 参数：

```sh
go build -buildid=        # ❌ flag provided but not defined: -buildid
go build -ldflags "-buildid="   # ✅
```

顺带一个实测数据：去掉 buildid 只省约 200 字节，别指望它减小体积。

### 1.8 cgo 交叉编译：`CC_FOR_TARGET` 没用，静态链接才是答案

```text
# runtime/cgo
gcc: error: unrecognized argument in option '-mabi=32'
gcc: note: valid arguments to '-mabi=' are: ms sysv
```

**根因**：cgo **只认 `CC`，不认 `CC_FOR_TARGET`**——设了 `CC_FOR_TARGET` 等于没设，
落到宿主机 x86 gcc 上。

显式设 `CC=/opt/toolchain-mipsel/toolchain-4.4.x/bin/mipsel-linux-uclibc-gcc` 后能编，
但链接器刷出大量警告：

```text
ld: warning: a.out uses -mhard-float (set by .../sysroot/usr/lib/crt1.o), go.o uses -msoft-float
```

uClibc 的 `crt1.o` 是硬浮点 ABI，和 `GOMIPS=softfloat` 天然冲突。

**结论**：最终选 **纯 Go + `CGO_ENABLED=0`**（静态、无 libc ABI 依赖、容器内可逐字节复现）。
cgo 版虽然真机也能跑，但引入了 `libc.so.0` / `ld-uClibc.so.0` 依赖，得不偿失。

### 1.9 go.mod 里改 go 版本号会连带炸掉别的项目

给 xray 把 `go.mod` 的 `1.21.4` 改成 `1.23.0` 后，除了 `futex -89`（见 1.1），还额外报：

```text
invalid reference to net.errNoSuchInterface
```

**根因**：go1.23 的 `net` 包符号布局变了，sagernet/sing 里的 `//go:linkname`
引用失效。

**但不要因此认定「go1.23+ 一律不能用」**：v2ray 5.31.0 用 go1.24.2 编完全正常。
这跟 runtime 里是否走到自旋锁路径有关，**每个二进制都要真机验**。

---

## 2. 证书链（坑最多的地方）

### 2.1 出厂证书不能直接放 romfs 的 `/etc/storage`

`/etc/storage` 是 tar 整包管理的**持久化分区**——load 时会被备份包整个盖掉。
把文件放进 romfs 的 `/etc/storage`，刷完固件就没了。

**正解**：

1. 出厂证书放只读的 `/etc/unm-cert-default`（romfs）
2. 由 `mtd_storage.sh` 的 `func_fill` 在 `/etc/storage/unblockneteasemusic`
   **不存在**时拷一次（私钥 600、证书 644）
3. 目录已存在时**一个字节都不动**——保证用户自己签的证书、以及客户端已装的
   `ca.crt` 在升级后继续有效，不会每次刷机都逼所有人重装证书

### 2.2 重签是破坏性的，必须先备份再动手

`unm_check_cert()` 四分支：

| 情况 | 处理 |
|---|---|
| 证书可用且匹配 | 一个字节不动 |
| 只是 server 快到期（30 天内） | **只重签 server**，CA 不变 → 客户端无需重装 |
| CA 也不可用 | 整套挪成 `*.bak-unm` → 重签 → 成功才删备份，失败自动还原 |

`unm_cert_usable()` 用 `openssl x509 -checkend 2592000`（30 天余量），避免证书
恰好卡在下次重启的边界上失效。

还必须有 `unm_cert_matches_ca()` / `unm_key_matches_cert()`：文件齐、能解析、
私钥与证书配对、签发链对得上，才复用。否则会出现「CA 换了、服务器证书还是旧的」
这种**客户端必然报错**的半残状态。

### 2.3 不要 `mkdir -p` 出不存在的挂载点

U 盘拔掉后 `/media/UDISK` 不存在。如果代码 `mkdir -p "$certdir"`，会在**根文件系统**上
凭空造出这个目录并写入一套新证书——等 U 盘插回来又是另一套 CA，客户端装的就对不上了。

**正解**：父目录不存在时直接按「U 盘未挂载」处理并报错返回；只补建最后一层。

### 2.4 切换证书目录前必须先拷再改 nvram

顺序反了会出事：先改 nvram → 下次 `unm_check_cert` 在**空的**新目录里生成全新 CA →
客户端装的 `ca.crt` 立刻失效，而且用户完全看不出原因。

**正解**：`unm_prepare_cert_dir()` 在改 nvram **之前**把默认目录那套拷到目标目录
（只在目标目录「没有任何证书」时拷），拷完用 `openssl verify` 自检。

### 2.5 临时文件必须跟证书目录走

`dn.cnf` / `ext.cnf` / `server.csr` 原来写死 `/etc/storage`。当证书指到 U 盘时，
这些临时文件仍要去写只读的根文件系统，**直接失败**。

改到 `wyybin_cert` 指定的目录。另外 `cat >"$cfg"` 要检查返回值——不检查的话
用户只能看到 openssl 的 `Can't open ... for reading`，完全不知道是写权限问题。

### 2.6 SAN 里的 IP 别写死

原来证书 SAN 里写死 `192.168.3.1`。换到别的网段后客户端就校验不过。
要取实际的 LAN 地址（`unm_getip`）。

### 2.7 CA 缺 SubjectKeyIdentifier → 客户端报 error 19 / 20

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

### 2.8 证书目录 vs MITM CA 目录是两套

业务证书跟随 `wyybin_cert`（可以指到 U 盘），而 MITM CA（`mitm-ca.crt`/`mitm-ca.key`）
的路径是用 `UNM_DIR` 拼的。这两个不是同一个变量——排查时很容易找错地方。

### 2.9 vfat 不支持 rename 覆盖

U 盘是 vfat 时，重签证书会报：

```
mv: can't rename '/media/AiDisk/.../server.crt.new': File exists
```

vfat 没有原子 rename。**正解**：先写到 `/tmp` 再 `cat >` 覆盖。

### 2.10 csr 与 key 不配对

手工重签时如果用了旧 csr，`openssl` 校验会报 `MISMATCH`。要重新生成 csr：

```sh
openssl req -new -key server.key -out server.csr
```

### 2.11 客户端信任（各平台）

| 平台 | 关键点 |
|---|---|
| **iOS** | 装完描述文件**还必须在「设置 → 通用 → 关于本机 → 证书信任设置」里手动打开开关**。这一步最容易漏，不打开时服务端日志是 `remote error: tls: unknown certificate` |
| **Windows** | `certutil -addstore -user Root ca.crt`（当前用户）或 `certutil -addstore Root ca.crt`（需管理员）。装完要**重启客户端**——很多程序只在启动时读证书 |
| **Linux** | 文件名**必须是 `.crt`**（`update-ca-certificates` 只认这个后缀）。`sudo cp ca.crt /usr/local/share/ca-certificates/xxx.crt && sudo update-ca-certificates`，成功标志 `1 added, 0 removed`。Electron 客户端可能还需要 `SSL_CERT_FILE=/path/ca.crt` |
| **Android** | 7+ 默认不信任用户 CA，要装到系统凭证（需 root）或用 Magisk 模块 |

---

## 3. DNS 劫持

### 3.1 `interfacepc.music.163.com` 一定要加

新版 PC 客户端走 `interfacepc`，不劫持它的话电脑端的播放地址请求根本不到本机，
全部直连网易拿试听片段。四行一起写：

```
address=/music.163.com/$ip
address=/interface.music.163.com/$ip
address=/interface3.music.163.com/$ip
address=/interfacepc.music.163.com/$ip
```

### 3.2 解析网易地址不能问本机 dnsmasq

转发模式下 dnsmasq 里挂着 `address=/music.163.com/<本机IP>`。如果程序用**系统解析器**
去解析网易的域名（Padavan 上等价于问本机 dnsmasq），拿到的就是本机 IP，于是
「请求网易 API」变成「请求自己」，服务端授权信息拿不到，**任何 VIP 歌都解锁不了**。

实测报错：`timeout awaiting response headers`。

**正解**：优先 `queryPublicDns(223.5.5.5)`，仅在失败或返回非公网地址时回退系统解析器。

### 3.3 启动顺序：wyybin 必须在 SmartDNS 之前

SmartDNS 只在**启动那一刻**读一遍 `dnsmasq.conf`。如果 wyybin 在它之后才写劫持规则，
当轮就不生效，要等下次重启。

**正解**：`autostart.sh` 里把 wyybin 提到 SmartDNS 之前，并加一条兜底：
「两个都开且都在跑，就补一次 SmartDNS 重启」。

### 3.4 别同时改 `/etc/hosts` 和 dnsmasq

两种机制会互相覆盖，谁后写谁赢，排查时根本看不出是谁在起作用。

**正解**：只保留一种。现在转发模式**只写 dnsmasq 的 `address=`**，不再动 `/etc/hosts`，
`unm_hook_hosts` / `unm_unhook_hosts` 一并删除。

### 3.5 停止服务时必须清理劫持规则

否则 failover 后域名仍指向本机、服务已停、客户端连不上——表现像断网。

`unm_stop()` 最后一定要调 `unm_unhook_dns`。

### 3.6 用 SIGHUP 而不是重启 DHCP

`wyybin_apply_dns()` 调 `restart_dns()` 而不是 `restart_dhcpd()`——后者会顺带把
DHCP 租约一起重置。启动和停止都要调（停止时恰好需要丢掉劫持规则）。

---

## 4. 运行与进程管理

### 4.0 证书缺失时上游**什么都不说**

三种表现，一种比一种难查：

| 表现 | 日志 |
|---|---|
| A. 明确报错 | `启动失败：监听 HTTPS 端口 127.0.0.1:5201（证书 ...）：open .../server.crt: no such file or directory` |
| B. panic | `panic: open /tmp/server.crt: no such file or directory`（`proxy.startTlsServer`），HTTP 端口已监听但进程直接退出 |
| C. **最坑** | 日志打完 `-------------------Init Host-------------------` 和 `HostDomain: map[...]` 后**戛然而止**，进程消失、netstat 无监听。不细看看不出是证书问题 |

**解法**：证书缺失不再 panic，改为 `警告：证书文件不可用，TLS 端口不启动（HTTP 端口正常工作）`；
脚本侧 `unm_check_cert` 失败时降级 `-insecure`；启动后 `sleep 2` + `kill -0` 自检，
失败时把原因写进 syslog。

### 4.1 `/proc/<pid>/comm` 被截断到 15 字节

**现象**：`pidof` 按名字找不到进程 → stop / status / watch 全部失灵 →
cron 守护以为进程没了，重复拉起，抢端口。

**根因**：内核把 `/proc/<pid>/comm` 截断到 15 字节。如果二进制文件名多出后缀
（比如 U 盘版的 `UnblockNeteaseMusic.new`），名字就对不上。

**正解**：遍历 `/proc/[0-9]*/cmdline` 匹配命令行，`pidof` 只作兜底。

### 4.2 MITM 端口别和 `-sp` 撞车

`-sp` 已经在 `0.0.0.0:$sslport` 上监 TLS。MITM 再绑 `127.0.0.1:$sslport` 会撞：

```
bind: address already in use
```

而且下游 `startTlsServer` 的 fatal 会**把整个进程带走**。

**正解**：`mitm_port=$((sslport + 1))`，绑 `127.0.0.1` 不对外暴露。

### 4.2.1 `-sp 443` 会抢掉路由器管理页的 443

用户给的是 `-sp 443`（想「正好和客户端连的端口一致」）。结果：

- 路由器 `netstat -tln` 里只有 `:::5200`，**根本没有 443** —— 因为 padavan 管理页
  已经占了 443，UNM 的 TLS 端口压根没起来
- 手机端 20 秒抓包：`132 次 119.188.212.41:443`、`99 次 113.201.9.14:443`、
  `10 次 220.197.30.75:443` —— **一次都没连过路由器上的 UNM**

**正解**：`wyybin_sslport` 默认值是 5201，不要改 443。
如果非要 443，先把管理页挪走。

### 4.3 `-mtls` 的参数是监听地址不是证书

```
-mtls 127.0.0.1:5202        # ✅
-mtls /path/to/cert.crt     # ❌ listen tcp: address ...: missing port
```

### 4.4 `-c` 指向非 CA 证书时，MITM 会在内存里另生成一套

`ensureMitmCA()` 的复用条件是「**ECDSA 且 `IsCA`**」。`server.crt` 是 `CA:FALSE`，
`server.key` 是 RSA —— 都复用不了，于是每次启动都现生成一套新 CA，客户端装的
`ca.crt` 永远对不上。

启动日志会提示：`提示：xxx/server.crt 不是 CA 证书，改用在内存里生成的 CA`。

磁盘 `ca.crt` 的指纹和内存里 `mitm-ca.crt` 的指纹完全是两套，排查时要分别看。

**正解**：要么给一套真正的 ECDSA CA，要么让 `mitm-ca.key` 落盘持久化
（重启能重建出**逐字节相同**的 CA，客户端装一次即可）。

### 4.5 `unm_start` 不要自己调 `unm_stop`

否则 `restart` 会连续停两次，每次都要等满 10 秒的优雅退出超时。
停旧进程交给调用方（`start`/`restart` 分支）负责。

### 4.5.1 页面上 `wyybin_mode` 是个死开关

**现象**：`nvram show | grep wyybin` 显示 `wyybin_mode=1`，但
`/proc/<pid>/cmdline` 里是 `... -o pyncmd -m 0 ...` —— **永远是 0**。

**根因**：脚本为了避开上游自动改 `/etc/hosts`（会覆盖系统自己的 `RT-N56U.lan` 记录），
把 `-m` 硬编码成 0，却仍然在页面暴露了这个下拉框。

**正解**：脚本内归一化 `unm_forward`：

```sh
case "$mode" in
  1|2) unm_forward=1 ;;
  *)   unm_forward=0 ;;
esac
```

**永远不让上游看到 1**，劫持由 `unm_hook_dns` 写 dnsmasq 规则完成。非法值记一条
syslog。启停后都要调 `wyybin_apply_dns()` → `restart_dns()`（发 SIGHUP，不重置 DHCP 租约）。

### 4.6 守护只认「进程不在」，别做端口探测

`watch` 分支三道保护：

1. 总开关关闭时立即返回
2. 5 分钟冷却
3. **只认「进程不在」，不做端口探测**

第 3 条的理由：端口探测本身要发请求，会打乱崩溃日志；而且进程活着但慢的时候
容易被误杀重拉。

**但崩溃循环也要防**：多音源版本曾经因为并发写 map（见 5.1）**每次播放必崩**，
守护能拉起但会陷入「崩 → 拉 → 崩」的死循环。所以守护只是兜底，根因还得修。

### 4.7 不用 `/sbin/check_crontab.sh`

它有两个问题：只认 `/usr/bin/<脚本名>` 的调用形式；而且**只增不删**——服务关掉后
那行还在，每 5 分钟白白 fork 一次。

**正解**：自己管 crontab，带标记：

```
*/5 * * * * /usr/bin/unblockneteasemusic.sh watch   #unblockneteasemusic-watch
```

crond 每 60 秒重读 crontabs 目录，增删都不用重启 crond。

### 4.8 `-wl` 会把 `/tmp` 撑爆

`-wl` 把每个请求和响应的完整 JSON 写进日志，单条几 KB 到几十 KB。**放开写十几分钟
就能把 `/tmp`（内存盘）填满**，进而拖垮同样写 `/tmp` 的 smartdns、dnsmasq。

**正解**：默认关闭 `-wl`；并用日志自限——启动时超过 10 MB 就截到最近 8 MB
（保留上下文以便回看，而不是直接清空）。

### 4.9 Go 的 `flag.Bool` 只能「传=开、不传=关」

`-bu` / `-ba` / `-lv` / `-sef` / `-wl` 都是无值开关。两个坑：

- 写成 `-wl=1` 或 `-wl true` → `flag.Parse` 报错，**进程直接退出**
- 重复写两次 `-wl -wl` → 也 ParseError（bool flag 已被包成 `"true"`，再次赋值即「多次设置」）

**正解**：从用户填的「附加参数」里剔除这几个，由页面开关统一追加。

### 4.10 `flag.Parse` 失败会 `os.Exit(2)`

默认的 `flag.Parse` 失败退出方式是 `os.Exit(2)`：进程直接消失，页面上只能看到
「启动失败（进程已退出）」，真正的原因丢在无人接收的 stderr 里。

**正解**：`config.ValidParams` 改用 `flag.ContinueOnError`，把错误抓出来记日志。

### 4.11 设备上没有 `timeout` 命令

Padavan 的 busybox 没编 `timeout`。要自己用 `sleep` + `kill` 实现。
另外 `kill` 后进程常残留，要 `kill -9`。

### 4.12 `/etc/hosts` 残留指向 127.0.0.1 会让程序 panic

如果 `/etc/hosts` 里还留着 `music.163.com → 127.0.0.1`（旧方案留下的），
程序解析自己时 panic。要检测并回退到内置 IP。

---

## 5. 音源与换源逻辑

### 5.1 并发写 map → `fatal error`，`recover` 抓不住

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

### 5.2 超时定时器必须建在循环外

写成 `select { case <-time.After(...) }` 时，**每轮循环都重新计时**——收到一条
结果就把 deadline 往后推。

实测：本意 12 秒的窗口被拖到 **14.9 秒**；改用循环外 `time.NewTimer` 后精确
**12.006 秒**。

### 5.3 全局 5 秒响应头超时会误杀慢接口

`network/network.go` 的 `Transport` 写死 `ResponseHeaderTimeout: 5s`，而 pyncmd
这个第三方 API 要 **4.5~9.4 秒**才回响应头——每次都被截断重试。

日志表现：`http2: timeout awaiting response headers`。

**解法**：给 `ClientRequest` 加 `ResponseHeaderTimeout` 字段单独放宽（改成 20s），
`maxAttempts` 从 3 降到 2。

### 5.4 pyncmd 直连会偶发挂到 17 秒

实测：直连 1.2s / 3.3s / 2.9s 之后会**挂起**，也出现过 12.4s、17.9s
（`pyncmd consumed: 17.907148332s`）。

走本机代理 `127.0.0.1:7890` → **0.71s / 0.52s / 1.11s** 全部 200。

**解法**：启动时探测 `7890/7891/1080/8118/10808/20171`（TCP dial 300ms 超时），
探到就走代理（单次 6s），探不到走直连（单次 20s）。

**注意**：不要用 `HTTP_PROXY` 环境变量——会影响 kuwo/kugou 这些本来就快的音源。
用单独的 `-proxy` 参数：留空=自动，`none`=强制直连。

### 5.5 `ForceAttemptHTTP2` 千万别关

关掉之后每次请求报：

```
net/http: HTTP/1.x transport connection broken: malformed HTTP response "\x00\x00\x12\x04\x00\x00..."
```

`\x00\x00\x12\x04` 是 h2 帧头。代理在隧道里直接发 h2 帧，Go 却按 HTTP/1.x
解析明文响应——必然失败。

> 这条和 `troubleshooting.md` 里的 ALPN 坑是同一个家族的问题：**HTTP/2 在隧道里
> 很容易被当成 HTTP/1.1 处理**。

### 5.6 `ClientRequest` 的两个字段要合并成一次 `Transport.Clone()`

加 `HTTPProxy` 和 `ResponseHeaderTimeout` 时，如果分成两次写各自的 `Transport.Clone()`，
后写会覆盖前面的**整个** Transport 配置。要合并成一次赋值。

### 5.7 音源分隔符必须是冒号

上游用 `strings.Split(*Source, ":")` 拆分音源。写竖线或逗号会被当成一个整体名字：

```
providers=["pyncmd|kuwo"]   # 一个源都没注册上
```

而且老配置里存的是竖线，配置脚本要**先归一（`tr` 换成冒号）再判断**，顺序反了就
会误以为没写 pyncmd。

### 5.8 匹配阈值 0.75 是刻意的，不是 bug

`CalScore` = `歌名分*0.55 + 歌手分*0.35 + 首位加分*0.1`，另加歌名必须 ≥ 0.7。

实测《晴天》：

- 搜 `晴天` → 第 1 条就是周杰伦原唱
- 搜 `晴天 周杰伦`（**程序实际用的**）→ 全是翻唱：`(KTV版伴奏)`、`(原唱:周杰伦)激情吃力版`、
  `（吉他弹唱）Cover 周杰伦`

所以「有原唱却播翻唱」是**酷我搜索行为**，不是解析 bug。翻唱得分约 0.64，
被 0.75 正确拦下。

**取舍是有意的**：宁可放不出来，也不要放错歌。

### 5.9 音质被锁死在 128k

上游 `unifiedMusicQuality` 只认字符串 `br`，而网易客户端发的是**数字**——类型断言
失败后 `MusicQuality` 一直停在默认的 Lossless。

后果是反的：**手机要 128k，也照样回一份 37 MB 的 flac**。

另一个 bug：上游写成 `if level {} else if br {}`，这两个字段**恒同时出现**，
所以 br 分支永远进不去。

**解法**：两者独立判断、br 优先。

### 5.10 普通 `/api/` 请求的 query 从未被解析

上游只处理 eapi 和 linux/forward，普通 `/api/` GET 的 query 从未解析。
注意 `netease.Path` 来自 `request.URL.Path`（**不含 query**），
必须从 `request.URL.RawQuery` 取。

### 5.11 酷我音质降级：请求 320k 拿回 128k

原来无条件按 `format:mp3 & bitrate:320` 请求，但有些歌最高只有 192k 或 flac，
结果拿到 M500（128k）。

**解法**：保留搜索结果里的 `MINFO`，只请求**真实存在的最高档位**。

### 5.12 原唱被前 5 条截断

酷我 mobi 接口每页条数少、字段不全。改成 PC 接口（固定 30 条、字段更全），
扫描上限从 `min(len/2+1, 5)` 放宽到 `kuwoMaxScan = 8`。

PC 接口返回的是**单引号伪 JSON**，得手写解析（`pcparse.go`），按 `rid` 去重。

### 5.13 QQ 音源默认别开

QQ 的取址接口对**未开通绿钻的账号一律返回 `104003` / purl 为空**。硬塞进音源列表
只会每次多走一次无效请求。

另外曾经做过「页面编辑 QQ cookie」的功能，后来整体 revert 了——cookie 方案在
未登录账号上不解决问题，维护成本大于收益。

---

## 6. Web 页面（httpd）

### 6.1 ej 模板只认「一个函数调用」

`<% if ...: %>` / `<% else %>` / `<% endif %>` 这种写法 **ej 不认识**。`ej.c` 只认
「一个函数调用」，匹配不到就把**原文输出**——整段代码直接显示在页面上。

而且两参的 `nvram_match` **没有注册进 ej_handlers**，也不能用。

**正解**：统一用四参 `nvram_match_x`。

### 6.2 `event_mask` 左移 ≥64 是未定义行为

`common.h` 里：

```c
#define EVM_RESTART_REBOOT  1ULL << 64
#define EVM_BLOCK_UNSAFE    1ULL << 65
```

`event_mask` 的类型是 `u64`，左移 64 位在 C 里是**未定义行为**（gcc 报
`-Wshift-count-overflow`），实际结果是**恒为 0**。

后果很隐蔽：

- `EVM_BLOCK_UNSAFE == 0` → `web_ex.c:887` 的
  `if (!get_login_safe() && (v->event_mask & EVM_BLOCK_UNSAFE)) continue;`
  **永远不成立** → `http_username`、`http_passwd`、web 启动脚本、证书文件这些
  本该「未登录不许改」的变量**全部形同虚设**
- `EVM_RESTART_REBOOT == 0` → 改运行模式（`sw_mode`）、改 `usb3_disable` 后
  **不会重启**，只写 nvram 就返回

**正解**：改成空闲位 `1ULL << 56` / `1ULL << 16`。改完 53 个位宏无重复、无越界。

### 6.3 `variables.c` 第二列是 longname，不是默认值

```c
{ "wyybin_bu", "启用屏蔽升级提示", ... }
```

把 `"1"` / `"0"` 写在这一列**既没起到设默认值的作用**，又会让 `web_ex.c` 里
`atoi` 成输入框宽度——列表控件宽度变成 **1 或 0 像素**。

**正解**：布尔默认值挪进 `defaults.c` 的 `APP_WYYBIN` 段。

但 `defaults.c` **只在 nvram 首次初始化时写入**，升级路径不会补。所以脚本启动前
还要把仍为空的 `wyybin_bu/ba/lv/sef/wl/qq_enable` 补成默认值再 `commit` 一次，
让页面显示与脚本行为一致。

### 6.4 `start_apply.htm` 的 `action_mode=' Restart '` 是分组重启

本页没有分组控件、`group_id` 为空串 → `restart_needed_bits` 恒为 0 →
`notify_services()` 直接返回。

**表现**：关掉总开关后进程仍在跑、状态栏永远「运行中」，而且**只在「只翻了总开关」时复现**。

**正解**：保存后显式 POST `wyybin_cert_action=AppliedWYYBIN` 触发一次按需重启。

### 6.5 AJAX 拿 `start_apply.htm` 会拿到整页 HTML

`start_apply.htm` 命中 `mime_handlers` 的 `**.htm*` → `do_ej`，**只渲染模板不执行提交**。
AJAX 请求回来的是整页 HTML，不是预期结果。

**正解**：证书查询/生成走 `update.cgi?output=wyybin_cert`。

### 6.6 保存后遮罩不消失

原来靠 `start_apply.htm` 反向调父窗口收尾：

- 没有实际 nvram 变化时不吐 `done_committing`，`page_modified` 停在 0，遮罩没人收
- 有变化时 `parent.showLoading(0, "waiting")` 因 waiting 标志跳过自动 `hideLoading`

**正解**：改自建 AJAX，成功后自己 `hideLoading` + 刷新。

### 6.7 `popen` 管道拿不到退出码

`wyybin_prepcert_hook` 需要知道脚本的**退出码**才能决定是否继续保存新路径，
但 `popen` 只能读到 stdout，`pclose` 的返回值也不可靠。

**正解**：把输出重定向到临时文件再由 C 读回，退出码单独写一行。

### 6.8 路径参数要白名单校验

`prepcert` 的路径会被拼进 shell 命令。必须拒绝引号、分号、`$`、通配符等。

### 6.9 `.gitignore` 会吃掉你的文件

三条踩过的：

- 第 74 行 `trunk/user/*/*/` 会忽略 `trunk/romfs/etc/unm-cert-default/`（本意是忽略
  各用户包的解包目录），要 `git add -f`
- `trunk/user/www/*` 会忽略 `n56u_ribbon_fixed` 目录，已跟踪文件的改动要 `git add -f`
- 测试放在 `_testdata/tests/`（`_` 开头 Go 工具链会忽略）时同样要 `git add -f`

---

## 7. 路由器环境

### 7.1 `/tmp` 是 24 MB 内存盘

8.7 MB 的二进制塞不下：

```
scp: /tmp/a.bin: No space left on device
Segmentation fault
```

往 `/tmp` 传大文件前先 `df -h /tmp`。

### 7.2 `restart_dns` 有时不会真正重启

改完 dnsmasq 配置调 `restart_dns`，但 PID 一直没变（还是旧的）。要强杀重拉验证。

### 7.3 编译产物别用 sstrip 之后再 UPX

上游的 `rstrip.sh` 会用 sstrip 砍掉 ELF 节头。这会**破坏 UPX 的解壳能力**：
`upx -t` 报 `NotPackedException: not packed by UPX`。

不影响运行，但如果你指望 `upx -d` 解壳就失败了。

### 7.4 固件构建与刷机

| 坑 | 现象原文 | 正解 |
|---|---|---|
| 刷错分区名 | `mtd_write write /tmp/fw.trx Firmware_Stub` → `Could not open mtd device: Firmware_Stub` / `Can't open device for writing!` | `/proc/mtd` 里是 `mtd3 017b0000 "firmware"`，用 `mtd_write write /tmp/fw.trx firmware` |
| `rm -rf` 包目录 | `cd: can't cd to iptables-1.8.10` | `iptables-1.8.10` 是 git 跟踪的源码目录，清理用 `git clean` |
| 删目录留下标记 | `No targets specified and no makefile found` | 源码目录没了但 buildroot 的 `config_done` 还在 → 认为「配置没变」跳过 configure。删包目录要连 `config_done` 一起删 |
| 容器内单独 make | `web_ex.c:989/1062/2979` 报错 | `-DUSE_WID_2G= -DUSE_WID_5G=` 是空宏，`#if defined(USE_WID_2G)` 成立但表达式为空。必须走顶层完整构建 |
| `mount -t tmpfs /usr/bin` | 遮蔽 squashfs 里 88 个文件 | `/usr/bin` 是只读 squashfs，别往上面挂东西；靠 `umount /usr/bin` 才恢复 |
| linetool 静态库残留 | `mipsel-linux-uclibc-gcc: error: ../libxtables/.libs/libxtables.a: No such file or directory` | 增量构建残留，重生成 libtool 产物 |
| 脚本把自身当编译进程 | 误报「已有编译在跑」 | 进程匹配正则会匹配到自己的命令行，收紧匹配条件 |

### 7.5 CI 里的固件构建

| 坑 | 现象原文 | 正解 |
|---|---|---|
| Pages 系统工作流 | `Run actions/jekyll-build-pages@v1` → `Error: No such file or directory @ dir_chdir0 - /github/workspace/docs` | 仓库设为 Pages 源 = `docs/` 但目录不存在。这条跑的是 `pages-build-deployment`，**不是**自己的 `build.yml`——「为什么推上去不编固件」多半是它 |
| SSP stub 写 sysroot | `[✗] 无法写入 sysroot: …/sysroot/lib`，且 SSP 检查跑了两次 | `setup_ssp_stubs()` 被调用两次，第二次前 `pre_build_fix` 把 `toolchain-mipsel/` 换成 `ln -sf`，目标路径算错 → 指向不存在的目录。**别急着 `chmod -R u+w`**，那是对不存在的目标做 chmod，修不到点上；要改成真实试写检测 |
| `_is_ci()` 不装依赖 | `[✗] CI 缺少依赖: ccache go bison flex gawk` | CI 分支直接报错不尝试 apt，workflow 的安装步骤必须列全。缺 `bc` 挂在内核 `timeconst.h Error 127`，缺 `ccache` make 一启动就挂，缺 `go` 要编到 `user/xray` 才报 `go: Permission denied` |
| `upload-artifact@v3` | `This request has been automatically failed because it uses a deprecated version` | 升到 v7 |
| `toolchain-mipsel` 变成 symlink | `git status` 出现上千条假删除 | `git rm -r --cached toolchain-mipsel` |

CI 侧**没有**网易云插件本身的失败（无 go 下载、clone、upx、证书类报错）；镜像也没超大小
（trx 12.3 MB vs firmware 分区 25.8 MB）。插件的真实失败全部发生在**本机构建与真机**上。

---

## 8. 速查表

| 类别 | 坑 | 现象 | 正解 |
|---|---|---|---|
| 编译 | go1.23+ | `futexwakeup returned -89` + SIGSEGV | 用 go1.22 |
| 编译 | `GOARCH=mips` | `exec format error` | `mipsle` |
| 编译 | UPX 在 make 里崩 | 单独跑 8/8 过，子进程稳定崩 | `UNM_UPX_ENABLE := n` |
| 编译 | upx 路径 | 容器内找不到 | 用 `trunk/tools/bin/upx` |
| 编译 | `make` 只清理 | 没编译 | `.DEFAULT_GOAL := all` |
| 编译 | core 堆积 | 每个 4.8MB | `coreclean` |
| 编译 | 补丁静默失败 | 发出不含修复的二进制 | `-p1 --dry-run` + 检查 `.rej` |
| 证书 | romfs `/etc/storage` | 刷机后证书没了 | 放 `/etc/unm-cert-default` |
| 证书 | 重签无备份 | 客户端全要重装 | 先挪 `*.bak-unm` |
| 证书 | `mkdir -p` 造挂载点 | 两套 CA | 父目录不存在就报错 |
| 证书 | 先改 nvram 再拷 | 客户端 CA 失效 | 先 `prepcert` 再改 |
| 证书 | 临时文件写死路径 | `Can't open ... for reading` | 跟证书目录走 |
| 证书 | 缺 SKI | error 19/20 | 显式写 `subjectKeyIdentifier` |
| 证书 | vfat rename | `File exists` | 写 `/tmp` 再 `cat >` |
| 证书 | iOS 信任开关 | `unknown certificate` | 设置里手动打开 |
| 证书 | Linux 后缀 | `update-ca-certificates` 无效 | 必须 `.crt` |
| DNS | 漏 `interfacepc` | PC 端不生效 | 四行全写 |
| DNS | 问本机 dnsmasq | `timeout awaiting response headers` | 用公共 DNS |
| DNS | 启动顺序 | 当轮不生效 | wyybin 在 SmartDNS 之前 |
| DNS | hosts + dnsmasq 双写 | 互相覆盖 | 只用 dnsmasq |
| 运行 | `/proc/comm` 15 字节 | stop/watch 失灵 | 遍历 `/proc` 匹配命令行 |
| 运行 | MITM 撞 `-sp` | `bind: address already in use` | `sslport + 1` |
| 运行 | `-mtls` 传文件 | `missing port` | 传监听地址 |
| 运行 | `-c` 不是 CA | 每次启动换 CA | 用 ECDSA CA 或持久化 key |
| 运行 | restart 停两次 | 等 20 秒 | `unm_start` 不调 `unm_stop` |
| 运行 | 端口探测守护 | 误杀慢进程 | 只认进程不在 |
| 运行 | `-wl` 撑爆 `/tmp` | 拖垮 dnsmasq | 默认关闭 + 日志自限 |
| 运行 | `flag.Bool` | `-wl=1` 进程退出 | 只能「传=开」 |
| 运行 | `flag.Parse` `os.Exit(2)` | 页面只看到「已退出」 | `ContinueOnError` |
| 运行 | 证书缺失 | 日志戛然而止、进程消失 | 不 panic + `-insecure` 降级 |
| 运行 | `-sp 443` | 管理页占了 443，TLS 没起来 | 用 5201 |
| 运行 | `wyybin_mode` 死开关 | 页面显示 1，实际 `-m 0` | 脚本内归一化 |
| 编译 | `upx && mv` | 旧压缩包被打进固件 | 每轮从 `.raw` 重压 |
| 编译 | `upx -d` 未压缩文件 | exit 0 但不产出 | 无条件先 cp |
| 编译 | cgo `CC_FOR_TARGET` | `-mabi=32` | 用 `CC`，最终用 `CGO_ENABLED=0` |
| 编译 | go.mod 改版本 | `linkname` 引用失效 | 别乱升，逐二进制验证 |
| 音源 | 并发写 map | `fatal error` | 删掉多余写入 |
| 音源 | 循环内 `time.After` | 超时被无限推后 | 循环外 `NewTimer` |
| 音源 | 5s 响应头超时 | `timeout awaiting response headers` | 单独放宽 |
| 音源 | pyncmd 直连 | 挂到 17s | 走本机代理 |
| 音源 | 关 `ForceAttemptHTTP2` | `malformed HTTP response` | 别关 |
| 音源 | 竖线分隔 | 音源名被当成一个整体，一个都没注册上 | 用冒号 |
| 音源 | 阈值 0.75 | 翻唱被拒 | 刻意设计，不是 bug |
| 音源 | br 类型 | 128k 请求回 37MB flac | 数字/字符串都认 |
| 音源 | `/api/` query | 参数恒空 | 从 `RawQuery` 取 |
| Web | ej 语法 | 代码显示在页面上 | 用 `nvram_match_x` |
| Web | `1ULL << 64` | 登录保护失效 | 用 `<< 56` / `<< 16` |
| Web | variables 第二列 | 控件宽度 1 像素 | 默认值放 `defaults.c` |
| Web | `Restart` 分组 | 开关关不掉 | 显式发 `AppliedWYYBIN` |
| Web | AJAX 打 `start_apply` | 拿到整页 HTML | 走 `update.cgi` |
| Web | 遮罩不消失 | 卡在 saving | 自建 AJAX |
| Web | `popen` 退出码 | 判断不了成败 | 重定向到临时文件 |
| 环境 | `/tmp` 24MB | `No space left` | 先 `df` |
| 环境 | `restart_dns` 无效 | PID 不变 | 强杀重拉 |
| 环境 | sstrip + UPX | `upx -d` 失败 | 不影响运行，别指望解壳 |
| 固件 | `mtd_write` 写 `Firmware_Stub` | `Could not open mtd device` | 分区名是 `firmware` |
| 固件 | `rm -rf` 包目录 | `cd: can't cd to iptables-1.8.10` | 用 `git clean` |
| 固件 | 删目录后 `config_done` 残留 | `No targets specified and no makefile found` | 一起删 `config_done` |
| 固件 | 容器内单独 `make user/httpd` | `web_ex.c` 空宏报错 | 走顶层完整构建 |
| 固件 | `/usr/bin` 挂 tmpfs | 遮蔽 squashfs 里 88 个文件 | 别这么干 |
| CI | `jekyll-build-pages@v1` | `No such file or directory @ dir_chdir0 - /github/workspace/docs` | 禁用 Pages 或建 `docs/` |
| CI | SSP stub 写 sysroot | `[✗] 无法写入 sysroot` | 查 symlink 目标，别乱 chmod |
| CI | `upload-artifact@v3` | `uses a deprecated version` | 升 v7 |
| CI | 缺 `bc`/`ccache`/`go` | `timeconst.h Error 127` / `go: Permission denied` | workflow 里装全 |

---

## 9. 仍未解决的

| 问题 | 状态 |
|---|---|
| ALPN 只协商 `http/1.1`（新版客户端只有 h2） | **已修**，见 [`troubleshooting.md`](troubleshooting.md) 第 4 节 |
| `handshakeWithRetry` 重试 1/2、2/2 连续两次 `bad record MAC` | 存在。同一条连接上连续两次同样错误，说明该次不是独立抖动。客户端会自动重连，暂接受 |
| iOS 走 `xeapi` 加密接口 | 未做。UNM 只认老的明文 eapi 路径，iOS 新版可能整条链路拦不到 |
| 安卓证书安装 | 没有真机记录，只有页面帮助文本 |
| RSA vs ECDSA 实测耗时 | 只有源码注释（「RSA-2048 几百毫秒 / P-256 几毫秒」），没有毫秒级实测 |
| UPX 到底能不能用 | 压缩版能跑（已验证），但**构建时 upx 自己会崩**（原因未查明）。当前 `UNM_UPX_ENABLE := n` |
| 根文件系统只剩 9 MB | `df -h` 显示 `/dev/root 9.0M 9.0M 0 100%`。NEWIFI3 的 `partition@50000` 是 23 MB 硬上限，trx 只用了 12.5 MB，**空间不是镜像问题而是运行期问题**——日志、`/tmp` 之外的临时文件都会撞上 |

---

## 10. 一条贯穿始终的教训

这些坑里有一半的共同点是：**失败是静默的**。

- 补丁打不上 → 不报错，直接往下走，编出不含修复的二进制
- `event_mask` 恒为 0 → 不报错，只是功能悄悄失效
- `flag.Parse` 失败 → 进程消失，页面上只剩一句「已退出」
- MITM 握手失败 → 客户端重连，日志里只有一串 CONNECT
- 证书换了 → 客户端拒绝，但用户只看到「歌放不了」

所以后来加的所有保护都是同一个思路：**能报错就别静默**。版本闸门、补丁 `.rej` 检查、
证书自检、`ContinueOnError`、MITM 启动时的警告——都是为了让失败**看得见**。
