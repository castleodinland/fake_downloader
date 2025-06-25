#!/bin/bash

# 如果任何命令执行失败，则立即退出脚本
set -e

# --- 配置 ---
# 设置最终生成的可执行文件的名字
BINARY_NAME="fake_server"
# ----------------

echo "==> 开始构建独立可执行文件..."

# --- 设置编译环境和参数 ---
# CGO_ENABLED=0: 禁用 CGO，构建纯静态链接的 Go 二进制文件，移除对系统 C 库的依赖。
# GOOS=linux:    明确指定目标操作系统为 Linux。
# GOARCH=amd64:  明确指定目标处理器架构为 amd64 (最常见的64位架构)。
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=amd64

echo "==> 环境变量设置完毕:"
echo "    CGO_ENABLED=${CGO_ENABLED}"
echo "    GOOS=${GOOS}"
echo "    GOARCH=${GOARCH}"
echo ""
echo "==> 正在编译 Go 应用..."

# --- 执行编译命令 ---
# go build:         编译命令。
# -o ${BINARY_NAME}: 指定输出文件的名称。
# -ldflags="-s -w": 链接器标志，用于减小文件大小。
#   -s: 移除符号表。
#   -w: 移除 DWARF 调试信息。
# .:                指定要编译的包为当前目录。
go build -ldflags="-s -w" -o ${BINARY_NAME} .

# --- 完成 ---
echo ""
echo "=========================================================="
echo "==> 构建成功！"
echo "==> 已生成可执行文件: ./${BINARY_NAME}"
echo "==> 文件大小: $(du -h ./${BINARY_NAME} | awk '{print $1}')"
echo "==> 您现在可以将 ./${BINARY_NAME} 文件复制到任何干净的 Linux (amd64) 环境中运行。"
echo "==> 警告: /reannounce 功能仍然需要目标环境中安装 python3"
echo "=========================================================="

