#!/bin/sh
# 把 _testdata/tests/ 里的测试临时放回各自包目录跑一遍，跑完删掉。
#
# 为什么测试不直接放在包目录：这个仓库是 fork，会给固件构建链直接拉取使用，
# 包目录里塞测试文件会让每个使用者都带上它们。放在 _ 开头的目录里，
# Go 工具链会整个忽略，既不参与构建也不进 go test ./...
#
# 文件名编码了原位置，下划线分隔：
#   proxy_replay_test.go        -> proxy/replay_test.go
#   provider_base_score_test.go -> provider/base/score_test.go
# 规则：最后一个下划线之前的部分逐级映射成目录，之后是文件名。
# 由于包目录可能有两级（provider/base），这里显式列出映射表更不容易出错。
set -e
cd "$(dirname "$0")/../.."

map() {
	case "$1" in
		proxy_*)           echo "proxy/${1#proxy_}" ;;
		processor_*)       echo "processor/${1#processor_}" ;;
		provider_base_*)   echo "provider/base/${1#provider_base_}" ;;
		provider_kuwo_*)   echo "provider/kuwo/${1#provider_kuwo_}" ;;
		*)                 echo "" ;;
	esac
}

placed=""
cleanup() { rm -f $placed; }
trap cleanup EXIT

for f in _testdata/tests/*_test.go; do
	base=$(basename "$f")
	target=$(map "$base")
	if [ -z "$target" ]; then
		echo "跳过 $base：没有对应的映射" >&2
		continue
	fi
	cp "$f" "$target"
	placed="$placed $target"
done

PATH=/home/mzking/software/go1.22.12/bin:$PATH go test ./... 2>&1 | grep -v "no test files"
