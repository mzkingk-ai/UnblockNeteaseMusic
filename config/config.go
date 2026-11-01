package config

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mzkingk-ai/UnblockNeteaseMusic/common"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/utils"
	"github.com/mzkingk-ai/UnblockNeteaseMusic/version"
)

var (
	Addr    = flag.String("a", "0.0.0.0", "specify server listen address,such as : \"0.0.0.0\"")
	Port    = flag.Int("p", 80, "specify server port,such as : \"80\"")
	TLSPort = flag.Int("sp", 443, "specify server tls port,such as : \"443\"")
	// 多个音源用冒号分隔（见下方 strings.Split(..., ":")），不是逗号也不是竖线。
	Source             = flag.String("o", "pyncmd:kuwo", "specify server source,separated by colon,such as : \"pyncmd:kuwo\"")
	CertFile           = flag.String("c", "./server.crt", "specify server cert,such as : \"server.crt\"")
	KeyFile            = flag.String("k", "./server.key", "specify server cert key ,such as : \"server.key\"")
	LogFile            = flag.String("l", "", "specify log file ,such as : \"/var/log/unblockNeteaseMusic.log\"")
	Mode               = flag.Int("m", 1, "specify running mode（1:hosts） ,such as : \"1\"")
	V                  = flag.Bool("v", false, "display version info")
	EndPoint           = flag.Bool("e", false, "enable replace song url")
	ForceBestQuality   = flag.Bool("b", false, "force the best music quality")
	SearchLimit        = flag.Int("sl", 0, "specify the number of songs searched on other platforms(the range is 0 to 3) ,such as : \"1\"")
	BlockUpdate        = flag.Bool("bu", false, "block version update message")
	BlockAds           = flag.Bool("ba", false, "block advertising requests")
	EnableLocalVip     = flag.Bool("lv", false, "enable local vip")
	UnlockSoundEffects = flag.Bool("sef", false, "unlock SoundEffects")
	QQCookieFile       = flag.String("qc", "./qq.cookie", "specify cookies file ,such as : \"qq.cookie\"")
	LogWebTraffic      = flag.Bool("wl", false, "log request url and response")
	MinScore           = flag.Float64("ms", 0.75, "minimum match score (0-1), lower results are rejected, such as : \"0.75\"")
	// Proxy 指定 pyncmd 走的本机代理。留空则自动探测常见端口（7890 等），
	// 探测到就用，探测不到就走直连。传 "none" 可强制禁用代理。
	Proxy    = flag.String("proxy", "", "http proxy for pyncmd, such as : \"http://127.0.0.1:7890\", \"none\" to disable")
	Insecure = flag.Bool("insecure", false, "listen TLS without a certificate (for clients that plain-connect to the TLS port)")
	// MITMAddr 指定 TLS 中间人的监听地址，形如 127.0.0.1:5201。
	// 为空则不启用：此时客户端把网易域名解析回本机、CONNECT 落进来的隧道只能
	// 原样透传，换源拿不到明文响应，播放地址永远是试听片段。
	MITMAddr = flag.String("mtls", "", "mitm listen address such as : \"127.0.0.1:5201\" (empty disables)")
	// MITMKeyFile 是自动生成 CA 时私钥落盘的位置，重启复用同一把 key，
	// 客户端只需信任一次根证书。
	MITMKeyFile = flag.String("mk", "./mitm-ca.key", "mitm ca private key file")
)

func ValidParams() bool {
	// 所有参数都注册在包级 var 里的默认 FlagSet（flag.CommandLine）上，
	// 所以这里只能改它的行为，不能另建 NewFlagSet（那样里面一个参数都没有）。
	// 默认的退出方式是 os.Exit(2)：参数写错时进程直接消失，页面上只能看到
	// 「启动失败（进程已退出）」，真正的原因丢在无人接收的 stderr 里。
	// 改成 ContinueOnError 后解析失败会返回 error，把它写进日志再按正常
	// 流程返回 false，原因就能留在 /tmp/unblockneteasemusic.log。
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stderr)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		log.SetOutput(os.Stderr)
		log.Println("--------------------Invalid Params--------------------")
		log.Printf("启动失败：参数解析错误：%v\n", err)
		return false
	}
	if flag.NArg() > 0 {
		log.Println("--------------------Invalid Params------------------------")
		log.Printf("Invalid params=%s, num=%d\n", flag.Args(), flag.NArg())
		for i := 0; i < flag.NArg(); i++ {
			log.Printf("arg[%d]=%s\n", i, flag.Arg(i))
		}
	}
	if *V {
		// active call should use fmt
		fmt.Println(version.FullVersion())
		return false
	}
	sources := strings.Split(strings.ToLower(*Source), ":")
	if len(sources) < 1 {
		log.Printf("source param invalid: %v \n", *Source)
		return false
	}
	if *SearchLimit < 0 || *SearchLimit > 3 {
		log.Printf("searchLimit param invalid （0-3）: %v \n", *SearchLimit)
		return false
	}
	// 上游这里不清空 common.Source，每调一次 ValidParams 就追加一遍：
	// main() 先调一次，proxy.InitProxy() 里又调一次，结果 Source 变成
	// ["kuwo","kuwo"]，Init() 里 providers[source] 互相覆盖，官方的
	// kugou 源也会被冲掉。先重置再填。
	common.Source = common.Source[:0]
	for _, source := range sources {
		common.Source = append(common.Source, source)
	}
	if *MinScore < 0 || *MinScore > 1 {
		log.Printf("minScore param invalid （0-1）: %v \n", *MinScore)
		return false
	}
	// 低于阈值的匹配会被 provider 层直接丢弃，这里只在启动日志里公示一次
	common.MinMatchScore = float32(*MinScore)

	// pyncmd 的代理策略在启动时定死一次：
	//   探测到本机代理 → 走代理（超时 6s，实测 0.5~1.1s 且不挂）
	//   探测不到       → 走直连（超时 20s，实测 1.2~3.3s 但偶尔挂到 18s）
	// 不在每次请求时探测：每次播放都要多一次 dial，且端口状态在运行中很少变。
	common.ProxyAddr = resolveProxy(*Proxy)
	if common.ProxyAddr != "" {
		log.Println("pyncmd 将经代理访问：" + common.ProxyAddr)
	} else {
		log.Println("pyncmd 将直连访问（未探测到本机代理）")
	}

	currentPath, err := utils.GetCurrentPath()
	if err != nil {
		log.Println(err)
		currentPath = ""
	}
	// log.Println(currentPath)
	certFile, _ := filepath.Abs(*CertFile)
	keyFile, _ := filepath.Abs(*KeyFile)
	_, err = os.Open(certFile)
	if err != nil {
		certFile, _ = filepath.Abs(currentPath + *CertFile)
	}
	_, err = os.Open(keyFile)
	if err != nil {
		keyFile, _ = filepath.Abs(currentPath + *KeyFile)
	}
	*CertFile = certFile
	*KeyFile = keyFile
	log.SetFlags(log.LstdFlags)
	if len(strings.TrimSpace(*LogFile)) > 0 {
		logFilePath, _ := filepath.Abs(*LogFile)
		logFile, logErr := os.OpenFile(logFilePath, os.O_CREATE|os.O_RDWR|os.O_SYNC|os.O_APPEND, 0666)
		if logErr != nil {
			// log.Println("Fail to find unblockNeteaseMusic.log start Failed")
			// panic(logErr)
			logFilePath, _ = filepath.Abs(currentPath + *LogFile)
		} else {
			logFile.Close()
		}
		*LogFile = logFilePath
		logFile, logErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_RDWR|os.O_SYNC|os.O_APPEND, 0666)
		if logErr != nil {
			log.Println("Fail to find " + logFilePath + " start Failed")
			panic(logErr)
		}
		os.Stdout = logFile
		os.Stderr = logFile
		fileInfo, err := logFile.Stat()
		if err != nil {
			panic(err)
		}
		if (fileInfo.Size() >> 20) > 2 { // 2M
			logFile.Seek(0, io.SeekStart)
			logFile.Truncate(0)
		}
		log.SetOutput(logFile)
	} else {
		log.SetOutput(os.Stdout)
	}
	return true
}

// proxyCandidates 是本机常见 HTTP 代理端口，按优先级排列。
// 第一个是 Clash 系的 mixed-port（ShellCrash 默认 7890），
// 其余覆盖 v2ray/ssr/privoxy 等常见配置。
var proxyCandidates = []int{7890, 7891, 1080, 8118, 10808, 20171}

// resolveProxy 决定 pyncmd 用哪个代理。
//
//	explicit == "none"  → 强制直连
//	explicit 非空       → 用它（不再探测）
//	explicit 为空       → 依次探测 proxyCandidates，命中即返回
//
// 探测用 TCP dial 而不是发 HTTP 请求：快（超时 300ms），且不依赖代理是否要求认证。
func resolveProxy(explicit string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit == "none" {
		return ""
	}
	if explicit != "" {
		if !strings.HasPrefix(explicit, "http://") && !strings.HasPrefix(explicit, "https://") {
			explicit = "http://" + explicit
		}
		return explicit
	}
	for _, port := range proxyCandidates {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err != nil {
			continue
		}
		conn.Close()
		return "http://" + addr
	}
	return ""
}
