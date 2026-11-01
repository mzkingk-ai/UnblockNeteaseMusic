# UnblockNeteaseMusic

解锁网易云音乐客户端变灰歌曲 (Golang)

> 这是 [`cnsilvan/UnblockNeteaseMusic`](https://github.com/cnsilvan/UnblockNeteaseMusic)
> 的 fork，主要用于在 Padavan / MT7621 路由器上跑。上游原版见
> [上游仓库](https://github.com/cnsilvan/UnblockNeteaseMusic)，
> 配套 LuCI 见 [luci-app-unblockneteasemusic](https://github.com/cnsilvan/luci-app-unblockneteasemusic)。

## 本 fork 相对上游的改动

| 方面 | 说明 |
|---|---|
| module 路径 | `github.com/mzkingk-ai/UnblockNeteaseMusic`，所有包内引用同步改过，不再跨仓库引用上游路径 |
| TLS 中间人 | `proxy/mitm.go` 用 CA 现场签证书，解出来的 HTTP/1.1 复用同一套改写逻辑 |
| 握手重试 | MT7621 上 `crypto/tls` 偶发 `bad record MAC`，同一条 TCP 上重握最多 2 次（失败率 1/3 → 约 1/27） |
| 音源 | 新增 `pyncmd`；酷我改走 PC 接口解析（`provider/kuwo/pcparse.go`） |
| 路由与解锁链路 | 音质选档、翻唱阈值、证书失败不退出、eapi 容错、provider 初始化顺序等 |
| 多源稳定性 | 修掉并发写 map 崩溃；pyncmd 改为并发竞速，不再被全局 5 秒响应头超时误杀 |

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

## 上游原版说明（以下为上游内容）

### 特性

* 就是快
* 较为精准的歌曲匹配
* 低内存、高效率
* 暂时支持酷狗、酷我 、咪咕的源
* 支持客户端选择音质（暂时支持酷我、咪咕）
* 支持在搜索页直接显示其他平台搜索结果
* 学习过程中的产物，随缘更新

### 运行

> [release页面](https://github.com/cnsilvan/UnblockNeteaseMusic/releases)中0.2.7及其之后的zip包将默认自带证书，该证书相对比较可靠。  

为了你的安全，还是建议你自己生成证书

```shell
./createCertificate.sh
```

运行程序（由于m=1时 会自动修改hosts生效 所以需要sudo）

```shell
sudo ./UnblockNeteaseMusic
```

### 具体参数说明

```shell
./UnblockNeteaseMusic -h

  -a	specify server listen address,such as : "0.0.0.0"
  -b	force the best music quality
  -c string
    	specify server cert,such as : "server.crt" (default "./server.crt")
  -e	enable replace song url
  -k string
    	specify server cert key ,such as : "server.key" (default "./server.key")
  -l string
    	specify log file ,such as : "/var/log/unblockNeteaseMusic.log"
  -m int
    	specify running mode（1:hosts） ,such as : "1" (default 1)
  -o string
    	specify server source,such as : "kuwo" (default "kuwo")
  -p int
    	specify server port,such as : "80" (default 80)
  -sl int
    	specify the number of songs searched on other platforms(the range is 0 to 3) ,such as : "1"
  -sp int
    	specify server tls port,such as : "443" (default 443)
  -v	display version info

```

### 重要提示

1. 应用通过本机dns获取域名ip，请注意本地hosts文件
2. 受限于歌曲md5的计算时间，耐心等待一会儿再点击下载歌曲吧
3. 网易云APP能用就别升级，不保证新版本可以使用
4. 开启多个源会自动选择最优匹配歌曲，并发的支持使得同时多个源获取并不会增加多少耗时，如果发现一直获取很慢，使用排除法来检查是否某一个源无法使用
6. 0.2.3版本后默认音质将根据客户端选择的音质，启动参数加上`-b`可以设置强制音质优先
7. 0.2.5版本后支持直接在搜索页面显示其他平台搜索结果，如遇错误，请关闭该功能(参数为 `-sl 0`)

### IOS信任证书步骤

1. 安装证书--设置-描述文件-安装
2. 通用-关于本机-证书信任设置-启动完全信任

### 已知

1. windows版本的网易云音乐需要在应用内 设置代理 Http地址为「HttpProxy」下任意地址 端口 80
2. Linux 客户端 (1.2 版本以上需要在终端启动网易云客户端时增加 --ignore-certificate-errors 参数)
3. ios客户端需要信任根证书且运行UnblockNeteaseMusic时 加上 -e 参数
4. android客户端使用咪咕源下载歌曲需要在运行UnblockNeteaseMusic时 加上 -e 参数（其他情况无法使用时，尝试加上 -e 参数）
5. 咪咕源貌似部分宽带无法使用
6. 最新版app采用第三方登录会失败，失败的同学选择手机号登录吧
7. Android7.0后默认不信任用户证书，解决办法自行谷歌
8. 咪咕已无法使用，需要登录，没空写了。且用且珍惜。

### 感谢

[NodeJs版本](https://github.com/nondanee/UnblockNeteaseMusic)以及为它贡献的所有coder

### 声明
该项目只能用作学习，请自行开通会员以支持平台购买更多的版权
