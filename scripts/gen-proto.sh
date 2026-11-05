#!/usr/bin/env bash
# Regenerates internal/agentapi/*.pb.go from agent.proto using pinned plugins.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin="$root/bin"
mkdir -p "$bin"

if [[ ! -x "$bin/protoc-gen-go" ]]; then
  GOBIN="$bin" go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
fi
if [[ ! -x "$bin/protoc-gen-go-grpc" ]]; then
  GOBIN="$bin" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
fi

export PATH="$bin:$PATH"
cd "$root"
protoc \
  --proto_path=internal/agentapi \
  --go_out=internal/agentapi --go_opt=paths=source_relative \
  --go-grpc_out=internal/agentapi --go-grpc_opt=paths=source_relative \
  internal/agentapi/agent.proto
echo "generated internal/agentapi"
