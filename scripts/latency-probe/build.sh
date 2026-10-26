#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
mkdir -p bin/latency-probe
for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${platform%/*}
  arch=${platform#*/}
  suffix=""
  [ "$os" != windows ] || suffix=.exe
  echo "Building $platform"
  (cd backend && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "../bin/latency-probe/latency-probe-$os-$arch$suffix" ./cmd/latency-probe)
  gzip -9 -c "bin/latency-probe/latency-probe-$os-$arch$suffix" > "bin/latency-probe/latency-probe-$os-$arch$suffix.gz"
done
