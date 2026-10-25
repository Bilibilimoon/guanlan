#!/bin/bash
# 观澜完整构建：前端 → 嵌入 → 双架构二进制 → 组装 pkg → fnpack 打 fpk
set -euo pipefail
cd "$(dirname "$0")"

# Go 工具链：优先 PATH；本机开发可在环境变量 GUANLAN_GOBIN 指定（路径含中文/私有目录时不进仓库）
if [ -n "${GUANLAN_GOBIN:-}" ] && [ -d "${GUANLAN_GOBIN}" ]; then
  export PATH="${GUANLAN_GOBIN}:$PATH"
fi
command -v go >/dev/null || { echo "缺少 Go 工具链（≥1.23）：请安装或在 GUANLAN_GOBIN 指定 bin 目录"; exit 1; }
export GOPROXY=https://goproxy.cn,direct GOSUMDB=off
export npm_config_registry=https://registry.npmmirror.com

echo "[1/5] 前端构建"
(cd web && npm install --no-fund --no-audit && npm run build)

echo "[2/5] 嵌入静态资源"
rm -rf server/internal/api/dist
cp -r web/dist server/internal/api/dist
touch server/internal/api/dist/.gitkeep

echo "[3/5] 编译后端（arm64 + amd64）"
(cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../pkg/guanlan/app/server/guanlan-arm64 ./cmd/guanlan)
(cd server && CGO_ENABLED=0 GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../pkg/guanlan/app/server/guanlan-amd64 ./cmd/guanlan)
# 本机直跑用的单架构产物
cp "pkg/guanlan/app/server/guanlan-$(uname -m | sed 's/aarch64/arm64/;s/x86_64/amd64/')" ./guanlan

echo "[4/5] 图标（若缺失则生成）"
if [ ! -f pkg/guanlan/ICON.PNG ]; then
  (cd tools/genicon && go run . ../..)
fi
chmod +x pkg/guanlan/cmd/*

echo "[5/5] fnpack 打包"
(cd pkg/guanlan && rm -f guanlan.fpk && fnpack build -d .)
echo "完成：$(pwd)/pkg/guanlan/guanlan.fpk 与 $(pwd)/guanlan"
